package syncsvc

import (
	"context"
	"log"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/vllm"
)

// Service periodically refreshes Hugging Face metadata (scaffolding for Phase 2).
type Service struct {
	discovery *vllm.Discovery
	logger    *log.Logger
	interval  time.Duration
}

// Options configure the Service.
type Options struct {
	Discovery *vllm.Discovery
	Logger    *log.Logger
	Interval  time.Duration
}

// New creates a new sync service.
func New(opts Options) *Service {
	interval := opts.Interval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	return &Service{
		discovery: opts.Discovery,
		logger:    opts.Logger,
		interval:  interval,
	}
}

// Run starts the refresh loop. Future phases will persist HF snapshots.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Println("huggingface sync service started — waiting for next refresh window")

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Println("sync service shutting down")
			return ctx.Err()
		case <-ticker.C:
			s.logger.Println("sync service heartbeat: awaiting implementation of HF cache")
			if s.discovery != nil {
				if _, err := s.discovery.ListSupportedArchitectures(); err != nil {
					s.logger.Printf("sync service: discovery probe failed: %v", err)
				}
			}
		}
	}
}
