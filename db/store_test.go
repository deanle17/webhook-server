package db_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/deanle/optivian-webhook/db"
	"github.com/deanle/optivian-webhook/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectTestDB connects to Postgres and runs migrations.
// It skips the test if DATABASE_URL is not set.
func connectTestDB(t *testing.T) *db.Store {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}

	ctx := context.Background()
	database, err := db.Connect(ctx)
	require.NoError(t, err, "connect to test database")

	_, err = database.Exec(`CREATE TABLE IF NOT EXISTS events (
		id           UUID        PRIMARY KEY,
		event_type   TEXT        NOT NULL,
		source       TEXT        NOT NULL,
		payload      JSONB       NOT NULL,
		status       TEXT        NOT NULL DEFAULT 'pending',
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		processed_at TIMESTAMPTZ
	)`)
	require.NoError(t, err, "create events table")

	t.Cleanup(func() {
		database.Exec("DELETE FROM events")
		database.Close()
	})

	return db.NewStore(database)
}

func newTestEvent() *models.Event {
	return &models.Event{
		ID:        uuid.New(),
		EventType: "order.created",
		Source:    "checkout",
		Payload:   json.RawMessage(`{"item_id":42}`),
		Status:    models.StatusPending,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestStore_CreateEvent(t *testing.T) {
	store := connectTestDB(t)
	ctx := context.Background()
	event := newTestEvent()

	err := store.CreateEvent(ctx, event)

	require.NoError(t, err)
	got, err := store.GetEvent(ctx, event.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, event.ID, got.ID)
	assert.Equal(t, event.EventType, got.EventType)
	assert.Equal(t, event.Source, got.Source)
	assert.Equal(t, event.Status, got.Status)
	assert.Nil(t, got.ProcessedAt)
}

func TestStore_GetEvent_NotFound(t *testing.T) {
	store := connectTestDB(t)
	ctx := context.Background()

	got, err := store.GetEvent(ctx, uuid.New())

	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStore_UpdateEventStatus(t *testing.T) {
	store := connectTestDB(t)
	ctx := context.Background()

	event := newTestEvent()
	require.NoError(t, store.CreateEvent(ctx, event))
	processedAt := time.Now().UTC().Truncate(time.Microsecond)

	err := store.UpdateEventStatus(ctx, event.ID, models.StatusCompleted, &processedAt)

	require.NoError(t, err)
	got, err := store.GetEvent(ctx, event.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, models.StatusCompleted, got.Status)
	require.NotNil(t, got.ProcessedAt)
	assert.WithinDuration(t, processedAt, *got.ProcessedAt, time.Millisecond)
}

func TestStore_UpdateEventStatus_NilProcessedAt(t *testing.T) {
	store := connectTestDB(t)
	ctx := context.Background()
	event := newTestEvent()
	require.NoError(t, store.CreateEvent(ctx, event))

	err := store.UpdateEventStatus(ctx, event.ID, models.StatusProcessing, nil)

	require.NoError(t, err)
	got, err := store.GetEvent(ctx, event.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, models.StatusProcessing, got.Status)
	assert.Nil(t, got.ProcessedAt)
}
