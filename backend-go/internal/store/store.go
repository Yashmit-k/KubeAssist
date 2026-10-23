// Package store persists investigations (a saved snapshot + diagnosis
// pair), remote-cluster registrations, and user accounts and sessions to
// Postgres.
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

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/alerts"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/fixpr"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
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

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider TEXT NOT NULL,
    provider_user_id TEXT NOT NULL,
    login TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_user_id)
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at);

ALTER TABLE clusters ADD COLUMN IF NOT EXISTS owner_id UUID REFERENCES users(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_clusters_owner ON clusters (owner_id);

CREATE TABLE IF NOT EXISTS github_tokens (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_sealed BYTEA NOT NULL,
    scopes TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cluster_repos (
    cluster_id TEXT PRIMARY KEY REFERENCES clusters(cluster_id) ON DELETE CASCADE,
    repo TEXT NOT NULL,
    branch TEXT NOT NULL,
    path TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alert_configs (
    cluster_id TEXT PRIMARY KEY REFERENCES clusters(cluster_id) ON DELETE CASCADE,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    url TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
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

// SaveCluster persists a registration (token hash only). An empty
// ownerID stores NULL (operator-only cluster).
func (s *Store) SaveCluster(ctx context.Context, clusterID, tokenHash, ownerID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO clusters (cluster_id, token_hash, owner_id) VALUES ($1, $2, NULLIF($3, '')::uuid)`,
		clusterID, tokenHash, ownerID)
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

// LoadClusters returns every persisted registration.
func (s *Store) LoadClusters(ctx context.Context) (map[string]tunnel.StoredCluster, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, token_hash, COALESCE(owner_id::text, '') FROM clusters`)
	if err != nil {
		return nil, fmt.Errorf("store: loading clusters: %w", err)
	}
	defer rows.Close()
	out := make(map[string]tunnel.StoredCluster)
	for rows.Next() {
		var id string
		var c tunnel.StoredCluster
		if err := rows.Scan(&id, &c.TokenHash, &c.OwnerID); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// --- auth.Store implementation ---

// UpsertUser creates or refreshes the user identified by (Provider,
// ProviderUserID).
func (s *Store) UpsertUser(ctx context.Context, u model.User) (model.User, error) {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO users (provider, provider_user_id, login, name, avatar_url)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (provider, provider_user_id) DO UPDATE
		   SET login = EXCLUDED.login, name = EXCLUDED.name, avatar_url = EXCLUDED.avatar_url, last_login_at = now()
		 RETURNING id::text`,
		u.Provider, u.ProviderUserID, u.Login, u.Name, u.AvatarURL,
	).Scan(&u.ID)
	if err != nil {
		return model.User{}, fmt.Errorf("store: upserting user: %w", err)
	}
	return u, nil
}

// CreateSession stores a session (by token hash) and sweeps expired ones.
func (s *Store) CreateSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("store: sweeping sessions: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, tokenHash, userID, expiresAt)
	if err != nil {
		return fmt.Errorf("store: creating session: %w", err)
	}
	return nil
}

// SessionUser returns the user behind a live session, or (nil, nil).
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (*model.User, error) {
	var u model.User
	err := s.db.QueryRowContext(ctx,
		`SELECT u.id::text, u.provider, u.provider_user_id, u.login, u.name, u.avatar_url
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash,
	).Scan(&u.ID, &u.Provider, &u.ProviderUserID, &u.Login, &u.Name, &u.AvatarURL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading session: %w", err)
	}
	return &u, nil
}

// DeleteSession ends a session (logout).
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// --- alerts.Store implementation ---

// GetAlertConfig returns a cluster's alert config, or (nil, nil).
func (s *Store) GetAlertConfig(ctx context.Context, clusterID string) (*alerts.Config, error) {
	var c alerts.Config
	err := s.db.QueryRowContext(ctx,
		`SELECT cluster_id, owner_id::text, kind, url, enabled, updated_at FROM alert_configs WHERE cluster_id = $1`, clusterID,
	).Scan(&c.ClusterID, &c.OwnerID, &c.Kind, &c.URL, &c.Enabled, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading alert config: %w", err)
	}
	return &c, nil
}

// PutAlertConfig creates or replaces a cluster's alert config.
func (s *Store) PutAlertConfig(ctx context.Context, c alerts.Config) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO alert_configs (cluster_id, owner_id, kind, url, enabled) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (cluster_id) DO UPDATE
		   SET owner_id = EXCLUDED.owner_id, kind = EXCLUDED.kind, url = EXCLUDED.url, enabled = EXCLUDED.enabled, updated_at = now()`,
		c.ClusterID, c.OwnerID, c.Kind, c.URL, c.Enabled)
	if err != nil {
		return fmt.Errorf("store: saving alert config: %w", err)
	}
	return nil
}

// DeleteAlertConfig removes a cluster's alert config.
func (s *Store) DeleteAlertConfig(ctx context.Context, clusterID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_configs WHERE cluster_id = $1`, clusterID)
	return err
}

// ListAlertConfigs returns every alert config.
func (s *Store) ListAlertConfigs(ctx context.Context) ([]alerts.Config, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cluster_id, owner_id::text, kind, url, enabled, updated_at FROM alert_configs`)
	if err != nil {
		return nil, fmt.Errorf("store: listing alert configs: %w", err)
	}
	defer rows.Close()
	out := []alerts.Config{}
	for rows.Next() {
		var c alerts.Config
		if err := rows.Scan(&c.ClusterID, &c.OwnerID, &c.Kind, &c.URL, &c.Enabled, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- auth.TokenStore implementation (sealed GitHub repository tokens) ---

func (s *Store) SaveGitHubToken(ctx context.Context, userID string, sealed []byte, scopes string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO github_tokens (user_id, token_sealed, scopes) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE SET token_sealed = EXCLUDED.token_sealed, scopes = EXCLUDED.scopes, updated_at = now()`,
		userID, sealed, scopes)
	if err != nil {
		return fmt.Errorf("store: saving GitHub token: %w", err)
	}
	return nil
}

func (s *Store) GitHubToken(ctx context.Context, userID string) ([]byte, string, error) {
	var sealed []byte
	var scopes string
	err := s.db.QueryRowContext(ctx, `SELECT token_sealed, scopes FROM github_tokens WHERE user_id = $1`, userID).Scan(&sealed, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("store: reading GitHub token: %w", err)
	}
	return sealed, scopes, nil
}

func (s *Store) DeleteGitHubToken(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_tokens WHERE user_id = $1`, userID)
	return err
}

// --- fixpr.LinkStore implementation ---

func (s *Store) GetRepoLink(ctx context.Context, clusterID string) (*fixpr.RepoLink, error) {
	var l fixpr.RepoLink
	err := s.db.QueryRowContext(ctx, `SELECT cluster_id, repo, branch, path, updated_at FROM cluster_repos WHERE cluster_id = $1`, clusterID).
		Scan(&l.ClusterID, &l.Repo, &l.Branch, &l.Path, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading repo link: %w", err)
	}
	return &l, nil
}

func (s *Store) PutRepoLink(ctx context.Context, l fixpr.RepoLink) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cluster_repos (cluster_id, repo, branch, path) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (cluster_id) DO UPDATE SET repo = EXCLUDED.repo, branch = EXCLUDED.branch, path = EXCLUDED.path, updated_at = now()`,
		l.ClusterID, l.Repo, l.Branch, l.Path)
	if err != nil {
		return fmt.Errorf("store: saving repo link: %w", err)
	}
	return nil
}

func (s *Store) DeleteRepoLink(ctx context.Context, clusterID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cluster_repos WHERE cluster_id = $1`, clusterID)
	return err
}
