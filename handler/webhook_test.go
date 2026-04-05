package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/deanle/optivian-webhook/handler"
	"github.com/deanle/optivian-webhook/models"
	"github.com/google/uuid"
)

// mockStore is an in-memory EventStore for testing.
type mockStore struct {
	mu     sync.Mutex
	events map[uuid.UUID]*models.Event
	err    error // if set, all methods return this error
}

func newMockStore() *mockStore {
	return &mockStore{events: make(map[uuid.UUID]*models.Event)}
}

func (m *mockStore) CreateEvent(_ context.Context, e *models.Event) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	m.events[e.ID] = e
	m.mu.Unlock()
	return nil
}

func (m *mockStore) GetEvent(_ context.Context, id uuid.UUID) (*models.Event, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.mu.Lock()
	e := m.events[id]
	m.mu.Unlock()
	return e, nil
}

func (m *mockStore) UpdateEventStatus(_ context.Context, id uuid.UUID, status models.Status, processedAt *time.Time) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	if e, ok := m.events[id]; ok {
		e.Status = status
		e.ProcessedAt = processedAt
	}
	m.mu.Unlock()
	return nil
}

func newHandler(store *mockStore) (*handler.WebhookHandler, chan *models.Event) {
	ch := make(chan *models.Event, 10)
	return handler.NewWebhookHandler(store, ch), ch
}

func TestCreate_Success(t *testing.T) {
	h, ch := newHandler(newMockStore())

	body := `{"event_type":"order.created","source":"checkout","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["id"] == "" {
		t.Fatal("expected id in response")
	}

	select {
	case <-ch:
	default:
		t.Fatal("expected event to be queued")
	}
}

func TestCreate_MissingEventType(t *testing.T) {
	h, _ := newHandler(newMockStore())

	body := `{"source":"checkout","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreate_MissingSource(t *testing.T) {
	h, _ := newHandler(newMockStore())

	body := `{"event_type":"order.created","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreate_EmptyPayload(t *testing.T) {
	h, _ := newHandler(newMockStore())

	body := `{"event_type":"order.created","source":"checkout","payload":null}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreate_StoreError(t *testing.T) {
	store := newMockStore()
	store.err = context.DeadlineExceeded
	h, _ := newHandler(store)

	body := `{"event_type":"order.created","source":"checkout","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestGet_Success(t *testing.T) {
	store := newMockStore()
	id := uuid.New()
	store.events[id] = &models.Event{
		ID:        id,
		EventType: "order.created",
		Source:    "checkout",
		Payload:   json.RawMessage(`{"id":1}`),
		Status:    models.StatusPending,
		CreatedAt: time.Now(),
	}

	h, _ := newHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/webhooks/"+id.String(), nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp models.Event
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != id {
		t.Fatalf("expected id %s, got %s", id, resp.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	h, _ := newHandler(newMockStore())

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/webhooks/"+id.String(), nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestGet_InvalidUUID(t *testing.T) {
	h, _ := newHandler(newMockStore())

	req := httptest.NewRequest(http.MethodGet, "/webhooks/not-a-uuid", nil)
	req.SetPathValue("id", "not-a-uuid")
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
