package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/redis/go-redis/v9"
)

// WeightInstallMessage wraps the payload pushed through Redis.
type WeightInstallMessage struct {
	JobID   string              `json:"jobId"`
	Request jobs.InstallRequest `json:"request"`
}

// Producer publishes jobs onto a Redis Stream.
type Producer struct {
	client redis.UniversalClient
	stream string
}

// NewProducer constructs a producer for the provided stream.
func NewProducer(client redis.UniversalClient, stream string) *Producer {
	if stream == "" {
		stream = "model-manager:jobs"
	}
	return &Producer{client: client, stream: stream}
}

// Enqueue pushes a weight install request to the stream.
func (p *Producer) Enqueue(ctx context.Context, jobID string, req jobs.InstallRequest) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("queue producer not configured")
	}
	if jobID == "" {
		jobID = uuid.NewString()
	}
	payload := WeightInstallMessage{
		JobID:   jobID,
		Request: req,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.client.XAdd(ctx, &redis.XAddArgs{
		Stream: p.stream,
		ID:     "*",
		Values: map[string]interface{}{
			"data": data,
		},
	}).Err()
}

// Consumer pulls jobs from a Redis Stream consumer group.
type Consumer struct {
	client   redis.UniversalClient
	stream   string
	group    string
	name     string
	blockDur time.Duration
}

// NewConsumer creates a consumer bound to a stream + group.
func NewConsumer(client redis.UniversalClient, stream, group, name string) *Consumer {
	if stream == "" {
		stream = "model-manager:jobs"
	}
	if group == "" {
		group = "weights-workers"
	}
	if name == "" {
		name = uuid.NewString()
	}
	return &Consumer{
		client:   client,
		stream:   stream,
		group:    group,
		name:     name,
		blockDur: 5 * time.Second,
	}
}

// EnsureGroup ensures the consumer group exists.
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("queue consumer not configured")
	}
	err := c.client.XGroupCreateMkStream(ctx, c.stream, c.group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		return err
	}
	return nil
}

// Next fetches the next message from the stream (blocking).
func (c *Consumer) Next(ctx context.Context) (*WeightInstallMessage, string, error) {
	if c == nil || c.client == nil {
		return nil, "", fmt.Errorf("queue consumer not configured")
	}
	args := &redis.XReadGroupArgs{
		Group:    c.group,
		Consumer: c.name,
		Streams:  []string{c.stream, ">"},
		Count:    1,
		Block:    c.blockDur,
	}
	res, err := c.client.XReadGroup(ctx, args).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, "", nil
		}
		return nil, "", err
	}
	for _, stream := range res {
		for _, msg := range stream.Messages {
			raw, ok := msg.Values["data"]
			if !ok {
				continue
			}
			bytes, ok := raw.(string)
			if !ok {
				continue
			}
			var payload WeightInstallMessage
			if err := json.Unmarshal([]byte(bytes), &payload); err != nil {
				return nil, msg.ID, err
			}
			return &payload, msg.ID, nil
		}
	}
	return nil, "", nil
}

// Ack confirms processing of a message.
func (c *Consumer) Ack(ctx context.Context, id string) error {
	if c == nil || c.client == nil || id == "" {
		return nil
	}
	return c.client.XAck(ctx, c.stream, c.group, id).Err()
}
