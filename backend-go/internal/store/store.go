// Package store persists investigations (a saved snapshot + diagnosis
// pair) and remote-cluster registrations to Postgres.
//
// We use database/sql with the pgx driver and plain SQL rather than an
// ORM: there are two small tables, and the nested snapshot/diagnosis
// documents are stored as JSONB because they are always read back
// whole, never queried field-by-field beyond a few summary values.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// LocalCluster is the cluster key for investigations of the backend's
// own (local-mode) cluster. Remote clusters use their cluster ID.
const LocalCluster = "local"

// ErrNotFound is returned when an investigation doesn't exist (or
// exists but belongs to a different cluster — callers can't tell the
// difference, by design).
var ErrNotFound = errors.New("investigation not found")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Store wraps a *sql.DB connection pool (safe for concurrent use).
type Store struct {
	db *sql.DB
}

// New opens a connection pool and waits up to connectTimeout for the
// database to accept connections. Retrying matters in practice: under
// docker compose the backend can start before Postgres is ready, and
// serverless Postgres (e.g. Neon) can take seconds to wake up.
func New(databaseURL string, connectTimeout time.Duration) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: opening connection: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(5 * time.Minute)

	deadline := time.Now().Add(connectTimeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return &Store{db: db}, nil
		}
		if time.Now().After(deadline) {
			db.Close()
			return nil, fmt.Errorf("store: pinging database: %w", err)
		}
		time.Sleep(time.Second)
	}
}

// Close releases the connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping reports whether the database is reachable (used by /api/health).
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Migrate creates or upgrades the schema. Every statement is idempotent,
// so this runs on every startup; a project with more tables would want a
// versioned migration tool, but this keeps deployment to one binary.
func (s *Store) Migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS investigations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    namespace TEXT NOT NULL,
    pod_name TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    diagnosis JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE investigations ADD COLUMN IF NOT EXISTS cluster_id TEXT NOT NULL DEFAULT 'local';
CREATE INDEX IF NOT EXISTS idx_investigations_namespace ON investigations (namespace);
CREATE INDEX IF NOT EXISTS idx_investigations_created_at ON investigations (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_investigations_cluster_created ON investigations (cluster_id, created_at DESC);

CREATE TABLE IF NOT EXISTS clusters (
    cluster_id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("store: running migration: %w", err)
	}
	return nil
}

// Save persists an investigation and returns its generated ID.
func (s *Store) Save(ctx context.Context, clusterID, namespace, podName string, snapshot model.DiagnosticSnapshot, diagnosis model.DiagnosisResponse) (string, error) {
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("store: marshaling snapshot: %w", err)
	}
	diagnosisJSON, err := json.Marshal(diagnosis)
	if err != nil {
		return "", fmt.Errorf("store: marshaling diagnosis: %w", err)
	}

	var id string
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO investigations (cluster_id, namespace, pod_name, snapshot, diagnosis)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		clusterID, namespace, podName, snapshotJSON, diagnosisJSON,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store: inserting investigation: %w", err)
	}
	return id, nil
}

// Get retrieves one investigation, scoped to clusterID — an ID that
// belongs to a different cluster is reported as not found.
func (s *Store) Get(ctx context.Context, clusterID, id string) (*model.Investigation, error) {
	if !uuidPattern.MatchString(id) {
		return nil, ErrNotFound
	}

	var (
		inv                         model.Investigation
		snapshotJSON, diagnosisJSON []byte
		createdAt                   time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, cluster_id, namespace, pod_name, snapshot, diagnosis, created_at
		 FROM investigations WHERE id = $1 AND cluster_id = $2`, id, clusterID,
	).Scan(&inv.ID, &inv.ClusterID, &inv.Namespace, &inv.PodName, &snapshotJSON, &diagnosisJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: getting investigation: %w", err)
	}
	if err := json.Unmarshal(snapshotJSON, &inv.Snapshot); err != nil {
		return nil, fmt.Errorf("store: unmarshaling snapshot: %w", err)
	}
	if err := json.Unmarshal(diagnosisJSON, &inv.Diagnosis); err != nil {
		return nil, fmt.Errorf("store: unmarshaling diagnosis: %w", err)
	}
	inv.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return &inv, nil
}

// List returns recent investigation summaries for one cluster,
// optionally filtered by namespace. Summary fields are extracted from
// the JSONB in SQL so listing never ships whole snapshots.
func (s *Store) List(ctx context.Context, clusterID, namespace string, limit int) ([]model.InvestigationSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, cluster_id, namespace, pod_name,
		        COALESCE(diagnosis->>'failureType', ''),
		        COALESCE(diagnosis->>'issueSummary', ''),
		        COALESCE((diagnosis->>'confidenceScore')::float8, 0),
		        COALESCE(diagnosis->>'mode', ''),
		        created_at
		 FROM investigations
		 WHERE cluster_id = $1 AND ($2 = '' OR namespace = $2)
		 ORDER BY created_at DESC
		 LIMIT $3`, clusterID, namespace, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing investigations: %w", err)
	}
	defer rows.Close()

	results := make([]model.InvestigationSummary, 0)
	for rows.Next() {
		var (
			sum       model.InvestigationSummary
			createdAt time.Time
		)
		if err := rows.Scan(&sum.ID, &sum.ClusterID, &sum.Namespace, &sum.PodName,
			&sum.FailureType, &sum.IssueSummary, &sum.ConfidenceScore, &sum.Mode, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scanning row: %w", err)
		}
		sum.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		results = append(results, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterating investigation rows: %w", err)
	}
	return results, nil
}

// DeleteForCluster removes all investigations for a cluster (used when a
// cluster registration is revoked).
func (s *Store) DeleteForCluster(ctx context.Context, clusterID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM investigations WHERE cluster_id = $1`, clusterID)
	return err
}

// --- tunnel.ClusterStore implementation ---

// SaveCluster persists a registration (token hash only).
func (s *Store) SaveCluster(ctx context.Context, clusterID, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO clusters (cluster_id, token_hash) VALUES ($1, $2)`, clusterID, tokenHash)
	if err != nil {
		return fmt.Errorf("store: saving cluster: %w", err)
	}
	return nil
}

// DeleteCluster removes a registration and that cluster's history.
func (s *Store) DeleteCluster(ctx context.Context, clusterID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM clusters WHERE cluster_id = $1`, clusterID); err != nil {
		return fmt.Errorf("store: deleting cluster: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM investigations WHERE cluster_id = $1`, clusterID); err != nil {
		return fmt.Errorf("store: deleting cluster investigations: %w", err)
	}
	return tx.Commit()
}

// LoadClusters returns every persisted clusterID -> token hash.
func (s *Store) LoadClusters(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, token_hash FROM clusters`)
	if err != nil {
		return nil, fmt.Errorf("store: loading clusters: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		out[id] = hash
	}
	return out, rows.Err()
}
