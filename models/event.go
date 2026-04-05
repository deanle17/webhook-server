package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

type Event struct {
	ID          uuid.UUID       `json:"id"`
	EventType   string          `json:"event_type"`
	Source      string          `json:"source"`
	Payload     json.RawMessage `json:"payload"`
	Status      Status          `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	ProcessedAt *time.Time      `json:"processed_at,omitempty"`
}
