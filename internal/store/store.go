// Package store persists Investigation records (a saved snapshot +
// diagnosis pair) to Postgres, for the history/demo feature in
// Section 9 of the plan ("Postgres stays (investigation history for
// demos)").
//
// WHY database/sql (STDLIB) + A DRIVER, RATHER THAN AN ORM?
// Go's standard library ships database/sql — a generic interface for
// relational databases — and you plug in a specific driver (here,
// github.com/jackc/pgx's stdlib-compatible driver) underneath it. This
// is different from, say, Python's SQLAlchemy or a JS ORM: Go's
// ecosystem culturally favors writing SQL directly over query-building
// abstractions, because the type safety Go gives you at compile time
// (struct fields, function signatures) already catches many of the bugs
// an ORM protects against in more dynamically-typed languages, while
// direct SQL keeps performance and query behavior fully transparent.
// For a project this size, an ORM would be net negative: more magic,
// more to learn, no meaningful safety win.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // driver registration via side-effect import — see below

	"github.com/yourname/kubeassist/backend-go/internal/model"
)

// Store wraps a *sql.DB connection pool.
//
// IMPORTANT GO CONCEPT: *sql.DB is NOT a single connection — it's a
// connection POOL, managed entirely by database/sql itself. You never
// manually open/close individual connections; you call methods on *DB
// and it borrows a connection from the pool, uses it, and returns it
// automatically. This is why constructing a Store is cheap and safe to
// do once at startup and share across every request handler (it's
// concurrency-safe internally — many goroutines can use the same *DB
// simultaneously without additional locking on your part).
type Store struct {
	db *sql.DB
}

// New opens a connection pool and verifies connectivity with a ping.
// The blank import `_ "github.com/jackc/pgx/v5/stdlib"` above registers
// the "pgx" driver name with database/sql via an init() function inside
// that package — we never call anything from that package directly by
// name, we just need its side effect of registering itself, hence the
// blank identifier `_` instead of a normal import name.
func New(databaseURL string) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: opening connection: %w", err)
	}

	// sql.Open does NOT actually connect — it just validates the DSN
	// format and prepares the pool lazily. We explicitly Ping with a
	// bounded context so a misconfigured DATABASE_URL fails LOUDLY at
	// startup (per our "fail fast" philosophy from config.go) instead
	// of only surfacing on the first real query, potentially deep into
	// a demo.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: pinging database: %w", err)
	}

	return &Store{db: db}, nil
}

// Close releases the connection pool. Should be called once at
// application shutdown (see main.go).
func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate creates the investigations table if it doesn't exist. For an
// MVP, a single idempotent CREATE TABLE IF NOT EXISTS is appropriate —
// a real production system would use a proper migration tool (e.g.
// golang-migrate) with versioned, ordered migration files, but that's
// unnecessary complexity for a portfolio project with one table.
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
CREATE INDEX IF NOT EXISTS idx_investigations_namespace ON investigations (namespace);
CREATE INDEX IF NOT EXISTS idx_investigations_created_at ON investigations (created_at DESC);
`
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("store: running migration: %w", err)
	}
	return nil
}

// Save persists an Investigation and returns its generated ID.
//
// WHY STORE Snapshot/Diagnosis AS JSONB INSTEAD OF NORMALIZED COLUMNS?
// These are deeply nested, evolving structs (we WILL add fields to them
// as detectors mature). Normalizing them into relational columns/tables
// would mean a schema migration every time model.DiagnosticSnapshot
// changes shape — a lot of ceremony for data that's always read back
// as a whole object, never queried by its internal fields (we query by
// namespace/pod_name/created_at, all of which ARE real columns). JSONB
// (Postgres's binary JSON type) gives us schema flexibility for the
// deeply nested part while keeping real indexes on what we actually
// filter/sort by.
func (s *Store) Save(ctx context.Context, namespace, podName string, snapshot model.DiagnosticSnapshot, diagnosis model.DiagnosisResponse) (string, error) {
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("store: marshaling snapshot: %w", err)
	}
	diagnosisJSON, err := json.Marshal(diagnosis)
	if err != nil {
		return "", fmt.Errorf("store: marshaling diagnosis: %w", err)
	}

	var id string
	// QueryRowContext + RETURNING id is the idiomatic way to get a
	// database-generated value (here, the UUID default) back after an
	// INSERT in a single round-trip, rather than INSERT then a separate
	// SELECT.
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO investigations (namespace, pod_name, snapshot, diagnosis)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		namespace, podName, snapshotJSON, diagnosisJSON,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store: inserting investigation: %w", err)
	}

	return id, nil
}

// Get retrieves a single investigation by ID.
func (s *Store) Get(ctx context.Context, id string) (*model.Investigation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, namespace, pod_name, snapshot, diagnosis, created_at
		 FROM investigations WHERE id = $1`, id)

	return scanInvestigation(row)
}

// List returns recent investigations, optionally filtered by namespace.
func (s *Store) List(ctx context.Context, namespace string, limit int) ([]model.Investigation, error) {
	if limit <= 0 {
		limit = 20
	}

	var rows *sql.Rows
	var err error
	if namespace != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, namespace, pod_name, snapshot, diagnosis, created_at
			 FROM investigations WHERE namespace = $1
			 ORDER BY created_at DESC LIMIT $2`, namespace, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, namespace, pod_name, snapshot, diagnosis, created_at
			 FROM investigations ORDER BY created_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("store: listing investigations: %w", err)
	}
	// Always close *sql.Rows when done reading, even on early return —
	// otherwise the underlying connection can't be returned to the pool,
	// eventually exhausting it under load. defer handles this even if
	// we return an error partway through the loop below.
	defer rows.Close()

	var results []model.Investigation
	for rows.Next() {
		inv, err := scanInvestigationRow(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *inv)
	}
	// Checking rows.Err() after the loop is required — rows.Next()
	// returning false means EITHER "no more rows" OR "an error occurred
	// while reading"; you can't tell which without this check.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterating investigation rows: %w", err)
	}

	return results, nil
}

// scanner is a minimal interface satisfied by both *sql.Row and
// *sql.Rows, letting Get and List share one scanning implementation
// instead of duplicating the same field list twice.
type scanner interface {
	Scan(dest ...any) error
}

func scanInvestigation(row scanner) (*model.Investigation, error) {
	return scanInvestigationRow(row)
}

func scanInvestigationRow(row scanner) (*model.Investigation, error) {
	var (
		inv           model.Investigation
		snapshotJSON  []byte
		diagnosisJSON []byte
		createdAt     time.Time
	)

	if err := row.Scan(&inv.ID, &inv.Namespace, &inv.PodName, &snapshotJSON, &diagnosisJSON, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: investigation not found: %w", err)
		}
		return nil, fmt.Errorf("store: scanning row: %w", err)
	}

	if err := json.Unmarshal(snapshotJSON, &inv.Snapshot); err != nil {
		return nil, fmt.Errorf("store: unmarshaling snapshot: %w", err)
	}
	if err := json.Unmarshal(diagnosisJSON, &inv.Diagnosis); err != nil {
		return nil, fmt.Errorf("store: unmarshaling diagnosis: %w", err)
	}
	inv.CreatedAt = createdAt.Format(time.RFC3339)

	return &inv, nil
}
