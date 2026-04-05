package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deanle/optivian-webhook/handler"
	"github.com/deanle/optivian-webhook/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type mockStore struct {
	mock.Mock
}

func (m *mockStore) CreateEvent(ctx context.Context, e *models.Event) error {
	return m.Called(ctx, e).Error(0)
}

func (m *mockStore) GetEvent(ctx context.Context, id uuid.UUID) (*models.Event, error) {
	args := m.Called(ctx, id)
	event, _ := args.Get(0).(*models.Event)
	return event, args.Error(1)
}

func (m *mockStore) UpdateEventStatus(ctx context.Context, id uuid.UUID, status models.Status, processedAt *time.Time) error {
	return m.Called(ctx, id, status, processedAt).Error(0)
}

func newHandler(store *mockStore) (*handler.WebhookHandler, chan *models.Event) {
	ch := make(chan *models.Event, 10)
	return handler.NewWebhookHandler(store, ch), ch
}

func TestCreate_Success(t *testing.T) {
	store := new(mockStore)
	store.On("CreateEvent", mock.Anything, mock.AnythingOfType("*models.Event")).Return(nil)
	h, ch := newHandler(store)

	body := `{"event_type":"order.created","source":"checkout","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	assert.NotEmpty(t, resp["id"])

	select {
	case <-ch:
	default:
		t.Fatal("expected event to be queued")
	}
	store.AssertExpectations(t)
}

func TestCreate_MissingEventType(t *testing.T) {
	store := new(mockStore)
	h, _ := newHandler(store)

	body := `{"source":"checkout","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	store.AssertNotCalled(t, "CreateEvent")
}

func TestCreate_MissingSource(t *testing.T) {
	store := new(mockStore)
	h, _ := newHandler(store)

	body := `{"event_type":"order.created","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	store.AssertNotCalled(t, "CreateEvent")
}

func TestCreate_EmptyPayload(t *testing.T) {
	store := new(mockStore)
	h, _ := newHandler(store)

	body := `{"event_type":"order.created","source":"checkout","payload":null}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	store.AssertNotCalled(t, "CreateEvent")
}

func TestCreate_StoreError(t *testing.T) {
	store := new(mockStore)
	store.On("CreateEvent", mock.Anything, mock.AnythingOfType("*models.Event")).Return(context.DeadlineExceeded)
	h, _ := newHandler(store)

	body := `{"event_type":"order.created","source":"checkout","payload":{"id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.Create(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	store.AssertExpectations(t)
}

func TestGet_Success(t *testing.T) {
	store := new(mockStore)
	id := uuid.New()
	event := &models.Event{
		ID:        id,
		EventType: "order.created",
		Source:    "checkout",
		Payload:   json.RawMessage(`{"id":1}`),
		Status:    models.StatusPending,
		CreatedAt: time.Now(),
	}
	store.On("GetEvent", mock.Anything, id).Return(event, nil)
	h, _ := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/"+id.String(), nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		ID     uuid.UUID     `json:"id"`
		Status models.Status `json:"status"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, id, resp.ID)
	assert.Equal(t, models.StatusPending, resp.Status)
	store.AssertExpectations(t)
}

func TestGet_NotFound(t *testing.T) {
	store := new(mockStore)
	id := uuid.New()
	store.On("GetEvent", mock.Anything, id).Return((*models.Event)(nil), nil)
	h, _ := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/"+id.String(), nil)
	req.SetPathValue("id", id.String())
	w := httptest.NewRecorder()

	h.Get(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	store.AssertExpectations(t)
}

func TestGet_InvalidUUID(t *testing.T) {
	store := new(mockStore)
	h, _ := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/webhooks/not-a-uuid", nil)
	req.SetPathValue("id", "not-a-uuid")
	w := httptest.NewRecorder()

	h.Get(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	store.AssertNotCalled(t, "GetEvent")
}
