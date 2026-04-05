package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/deanle/optivian-webhook/db"
	"github.com/deanle/optivian-webhook/models"
	"github.com/google/uuid"
)

type WebhookHandler struct {
	store   db.EventStore
	eventCh chan<- *models.Event
}

func NewWebhookHandler(store db.EventStore, eventCh chan<- *models.Event) *WebhookHandler {
	return &WebhookHandler{store: store, eventCh: eventCh}
}

type createRequest struct {
	EventType string          `json:"event_type"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
}

func (h *WebhookHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.EventType == "" || req.Source == "" {
		writeError(w, http.StatusBadRequest, "event_type and source are required")
		return
	}
	if len(req.Payload) == 0 || string(req.Payload) == "null" {
		writeError(w, http.StatusBadRequest, "payload must not be empty")
		return
	}

	event := &models.Event{
		ID:        uuid.New(),
		EventType: req.EventType,
		Source:    req.Source,
		Payload:   req.Payload,
		Status:    models.StatusPending,
		CreatedAt: time.Now().UTC(),
	}

	if err := h.store.CreateEvent(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store event")
		return
	}

	// Non-blocking: if the channel is full the event stays pending in the DB.
	select {
	case h.eventCh <- event:
	default:
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": event.ID.String()})
}

func (h *WebhookHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event ID")
		return
	}

	event, err := h.store.GetEvent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch event")
		return
	}
	if event == nil {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}

	writeJSON(w, http.StatusOK, event)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
