package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deanle/optivian-webhook/models"
	"github.com/deanle/optivian-webhook/worker"
	"github.com/google/uuid"
)

// mockStore records status transitions for assertions.
type mockStore struct {
	mu       sync.Mutex
	statuses []models.Status
}

func (m *mockStore) CreateEvent(_ context.Context, _ *models.Event) error { return nil }

func (m *mockStore) GetEvent(_ context.Context, _ uuid.UUID) (*models.Event, error) {
	return nil, nil
}

func (m *mockStore) UpdateEventStatus(_ context.Context, _ uuid.UUID, status models.Status, _ *time.Time) error {
	m.mu.Lock()
	m.statuses = append(m.statuses, status)
	m.mu.Unlock()
	return nil
}

func (m *mockStore) lastStatus() models.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.statuses) == 0 {
		return ""
	}
	return m.statuses[len(m.statuses)-1]
}

func (m *mockStore) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.statuses)
}

func runEvent(t *testing.T, store *mockStore, srv *httptest.Server) {
	t.Helper()
	ch := make(chan *models.Event, 1)
	pool := worker.NewPool(store, ch, 1, worker.WithAPIURL(srv.URL))
	pool.Start(context.Background())

	ch <- &models.Event{ID: uuid.New(), Status: models.StatusPending}
	close(ch)
	pool.Wait()
}

func TestProcess_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &mockStore{}
	runEvent(t, store, srv)

	if got := store.lastStatus(); got != models.StatusCompleted {
		t.Fatalf("expected completed, got %s", got)
	}
	// processing + completed = 2 status updates, 1 API attempt
	if n := store.callCount(); n != 2 {
		t.Fatalf("expected 2 store calls, got %d", n)
	}
}

func TestProcess_AllFail(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := &mockStore{}
	runEvent(t, store, srv)

	if got := store.lastStatus(); got != models.StatusFailed {
		t.Fatalf("expected failed, got %s", got)
	}
	if n := int(attempts.Load()); n != 3 {
		t.Fatalf("expected 3 API attempts, got %d", n)
	}
}

func TestProcess_RetryThenSucceed(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	store := &mockStore{}
	runEvent(t, store, srv)

	if got := store.lastStatus(); got != models.StatusCompleted {
		t.Fatalf("expected completed, got %s", got)
	}
	if n := int(attempts.Load()); n != 3 {
		t.Fatalf("expected 3 API attempts, got %d", n)
	}
}
