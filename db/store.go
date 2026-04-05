package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/deanle/optivian-webhook/models"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// EventStore is the persistence interface used by the handler and worker.
// Keeping it here (rather than in each consumer) avoids import cycles and
// makes the contract explicit alongside its implementation.
type EventStore interface {
	CreateEvent(ctx context.Context, e *models.Event) error
	GetEvent(ctx context.Context, id uuid.UUID) (*models.Event, error)
	UpdateEventStatus(ctx context.Context, id uuid.UUID, status models.Status, processedAt *time.Time) error
}

// Store implements EventStore against a Postgres database.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Connect opens and verifies a Postgres connection.
// It reads DATABASE_URL from the environment, falling back to a local default.
func Connect(ctx context.Context) (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return db, nil
}

// RunMigrations executes the SQL migration file at startup.
func RunMigrations(db *sql.DB) error {
	sql, err := os.ReadFile("migrations/001_create_events.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := db.Exec(string(sql)); err != nil {
		return fmt.Errorf("run migration: %w", err)
	}
	return nil
}

func (s *Store) CreateEvent(ctx context.Context, e *models.Event) error {
	const q = `
		INSERT INTO events (id, event_type, source, payload, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := s.db.ExecContext(ctx, q, e.ID, e.EventType, e.Source, e.Payload, e.Status, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

func (s *Store) GetEvent(ctx context.Context, id uuid.UUID) (*models.Event, error) {
	const q = `
		SELECT id, event_type, source, payload, status, created_at, processed_at
		FROM events WHERE id = $1
	`
	var e models.Event
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&e.ID, &e.EventType, &e.Source, &e.Payload,
		&e.Status, &e.CreatedAt, &e.ProcessedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan event: %w", err)
	}
	return &e, nil
}

func (s *Store) UpdateEventStatus(ctx context.Context, id uuid.UUID, status models.Status, processedAt *time.Time) error {
	const q = `UPDATE events SET status = $2, processed_at = $3 WHERE id = $1`
	_, err := s.db.ExecContext(ctx, q, id, status, processedAt)
	if err != nil {
		return fmt.Errorf("update event status: %w", err)
	}
	return nil
}
