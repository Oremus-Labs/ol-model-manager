package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Event represents a domain event emitted by the control plane.
type Event struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data,omitempty"`
}

// Bus multiplexes events to connected clients (local + Redis backed).
type Bus struct {
	client redis.UniversalClient
	logger *log.Logger
	ch     string

	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
}

// Options configure the bus.
type Options struct {
	Client  redis.UniversalClient
	Logger  *log.Logger
	Channel string
}

// NewBus creates a new event bus.
func NewBus(opts Options) *Bus {
	channel := opts.Channel
	if channel == "" {
		channel = "model-manager-events"
	}
	bus := &Bus{
		client:      opts.Client,
		logger:      opts.Logger,
		ch:          channel,
		subscribers: make(map[chan Event]struct{}),
	}
	if bus.client != nil {
		go bus.observeRedis()
	}
	return bus
}

// Publish broadcasts an event to all subscribers and Redis.
func (b *Bus) Publish(ctx context.Context, evt Event) error {
	if evt.ID == "" {
		evt.ID = uuid.NewString()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}

	if b.client != nil {
		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		if err := b.client.Publish(ctx, b.ch, payload).Err(); err != nil {
			return fmt.Errorf("redis publish: %w", err)
		}
	}

	b.broadcast(evt)
	return nil
}

// Subscribe registers a subscriber and returns a channel plus a cancel func.
func (b *Bus) Subscribe(ctx context.Context) (<-chan Event, func(), error) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}

	go func() {
		<-ctx.Done()
		cancel()
	}()

	return ch, cancel, nil
}

func (b *Bus) broadcast(evt Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.subscribers {
		select {
		case ch <- evt:
		default:
			if b.logger != nil {
				b.logger.Printf("events: dropping event %s (subscriber backlog)", evt.ID)
			}
		}
	}
}

func (b *Bus) observeRedis() {
	ctx := context.Background()
	pubsub := b.client.Subscribe(ctx, b.ch)
	defer pubsub.Close()

	for {
		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			if b.logger != nil {
				b.logger.Printf("events: redis subscriber error: %v", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}

		var evt Event
		if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
			if b.logger != nil {
				b.logger.Printf("events: invalid payload: %v", err)
			}
			continue
		}
		b.broadcast(evt)
	}
}
