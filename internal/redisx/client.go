package redisx

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config configures the Redis client.
type Config struct {
	Addr        string
	Username    string
	Password    string
	DB          int
	TLSEnabled  bool
	TLSInsecure bool
}

// NewClient returns a configured Redis client or nil when no address is provided.
func NewClient(cfg Config) (redis.UniversalClient, error) {
	if cfg.Addr == "" {
		return nil, nil
	}

	opts := &redis.Options{
		Addr:     cfg.Addr,
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	}
	if cfg.TLSEnabled {
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: cfg.TLSInsecure, // #nosec G402 – intentional opt-in
		}
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return client, nil
}
