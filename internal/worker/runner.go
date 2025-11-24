package worker

import (
	"context"
	"log"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/store"
)

// Options configure the background worker process.
type Options struct {
	Store    *store.Store
	Jobs     *jobs.Manager
	Logger   *log.Logger
	Interval time.Duration
}

// Runner is a placeholder executor that will later consume Redis jobs.
type Runner struct {
	store    *store.Store
	jobs     *jobs.Manager
	logger   *log.Logger
	interval time.Duration
}

// New creates a new Runner.
func New(opts Options) *Runner {
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	return &Runner{
		store:    opts.Store,
		jobs:     opts.Jobs,
		logger:   opts.Logger,
		interval: interval,
	}
}

// Run starts the worker heartbeat loop. Phase 1 will replace this ticker with Redis Streams consumption.
func (r *Runner) Run(ctx context.Context) error {
	if r.logger == nil {
		r.logger = log.Default()
	}
	r.logger.Println("model-manager worker started — waiting for queued jobs")

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Println("worker shutting down")
			return ctx.Err()
		case <-ticker.C:
			count := r.pendingJobs()
			r.logger.Printf("worker heartbeat: %d jobs recorded (queue not yet enabled)", count)
		}
	}
}

func (r *Runner) pendingJobs() int {
	if r.store == nil {
		return 0
	}
	jobs, err := r.store.ListJobs(10)
	if err != nil {
		if r.logger != nil {
			r.logger.Printf("worker: failed to list jobs: %v", err)
		}
		return 0
	}
	return len(jobs)
}
