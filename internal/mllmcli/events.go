package mllmcli

import (
	"encoding/json"
	"time"
)

// EventEnvelope mirrors the SSE payload emitted by /events.
type EventEnvelope struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}
