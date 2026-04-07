// Package e2e contains end-to-end tests that wire the full stack together:
// real Postgres, real worker pool, real HTTP server, and a mock external API.
// Tests are skipped when DATABASE_URL is not set.
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"context"
	"github.com/deanle/optivian-webhook/db"
	"github.com/deanle/optivian-webhook/handler"
	"github.com/deanle/optivian-webhook/models"
	"github.com/deanle/optivian-webhook/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupStack wires all components together and returns the test HTTP server URL.
// It registers cleanup with t so all resources are released after the test.
func setupStack(t *testing.T, apiURL string) string {
	t.Helper()

	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set — skipping E2E test")
	}

	ctx := context.Background()

	database, err := db.Connect(ctx)
	require.NoError(t, err, "connect to database")

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

	store := db.NewStore(database)

	eventCh := make(chan *models.Event, 100)
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	pool := worker.NewPool(store, eventCh, 2, worker.WithAPIURL(apiURL))
	pool.Start(workerCtx)

	wh := handler.NewWebhookHandler(store, eventCh)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks", wh.Create)
	mux.HandleFunc("GET /webhooks/{id}", wh.Get)

	srv := httptest.NewServer(mux)

	t.Cleanup(func() {
		srv.Close()
		cancelWorkers()
		close(eventCh)
		pool.Wait()
		database.Exec("DELETE FROM events")
		database.Close()
	})

	return srv.URL
}

func TestIntegration_HappyPath(t *testing.T) {
	// 1. Mock external API — counts calls and always returns 200.
	var apiCallCount atomic.Int32
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	baseURL := setupStack(t, apiServer.URL)

	// 2. POST /webhooks — create an event.
	body := `{"event_type":"order.created","source":"checkout","payload":{"item_id":42}}`
	resp, err := http.Post(baseURL+"/webhooks", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var createResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&createResp))
	rawID := createResp["id"]
	require.NotEmpty(t, rawID, "response must include an id")

	id, err := uuid.Parse(rawID)
	require.NoError(t, err, "returned id must be a valid UUID")

	// 3. GET /webhooks/{id} immediately — event should be pending or processing.
	getURL := fmt.Sprintf("%s/webhooks/%s", baseURL, id)
	getResp, err := http.Get(getURL)
	require.NoError(t, err)
	defer getResp.Body.Close()

	assert.Equal(t, http.StatusOK, getResp.StatusCode)

	var statusResp struct {
		ID     uuid.UUID     `json:"id"`
		Status models.Status `json:"status"`
	}

	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&statusResp))
	assert.Equal(t, id, statusResp.ID)
	assert.Contains(t, []models.Status{models.StatusPending, models.StatusProcessing, models.StatusCompleted}, statusResp.Status)

	// 4. Poll GET until status is completed (up to 15s).
	require.Eventually(t, func() bool {
		r, err := http.Get(getURL)
		if err != nil || r.StatusCode != http.StatusOK {
			return false
		}
		var poll struct {
			Status models.Status `json:"status"`
		}
		json.NewDecoder(r.Body).Decode(&poll)
		r.Body.Close()
		return poll.Status == models.StatusCompleted
	}, 15*time.Second, 200*time.Millisecond, "event did not reach completed status in time")

	// 5. Confirm the external API was actually called.
	assert.GreaterOrEqual(t, apiCallCount.Load(), int32(1), "external API must have been called")
}
