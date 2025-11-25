package worker

import (
	"context"
	"log"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/metrics"
	"github.com/oremus-labs/ol-model-manager/internal/queue"
	"github.com/oremus-labs/ol-model-manager/internal/store"
)

// Options configure the background worker process.
type Options struct {
	Store    *store.Store
	Jobs     *jobs.Manager
	Logger   *log.Logger
	Queue    *queue.Consumer
	Interval time.Duration
}

// Runner processes queued jobs.
type Runner struct {
	store    *store.Store
	jobs     *jobs.Manager
	logger   *log.Logger
	queue    *queue.Consumer
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
		queue:    opts.Queue,
		interval: interval,
	}
}

// Run starts the worker loop.
func (r *Runner) Run(ctx context.Context) error {
	if r.logger == nil {
		r.logger = log.Default()
	}

	if r.queue == nil {
		r.logger.Println("worker queue not configured; falling back to heartbeat")
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				count := r.pendingJobs()
				r.logger.Printf("worker heartbeat: %d jobs recorded", count)
			}
		}
	}

	if err := r.queue.EnsureGroup(ctx); err != nil {
		return err
	}
	r.logger.Println("worker connected to Redis queue; waiting for jobs")
	r.observeQueueDepth(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Println("worker shutting down")
			return ctx.Err()
		default:
			msg, msgID, err := r.queue.Next(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.logger.Printf("worker queue read error: %v", err)
				time.Sleep(time.Second)
				continue
			}
			if msg == nil {
				continue
			}

			job, err := r.jobs.GetJob(msg.JobID)
			if err != nil {
				r.logger.Printf("worker: job %s missing: %v", msg.JobID, err)
				_ = r.queue.Ack(ctx, msgID)
				continue
			}

			if job.Status == store.JobCancelled {
				r.logger.Printf("worker: job %s cancelled; skipping", job.ID)
				_ = r.queue.Ack(ctx, msgID)
				continue
			}
			if job.Status == store.JobDone {
				_ = r.queue.Ack(ctx, msgID)
				continue
			}
			if job.Status != store.JobPending && job.Status != store.JobRunning {
				r.logger.Printf("worker: job %s in status %s; skipping", job.ID, job.Status)
				_ = r.queue.Ack(ctx, msgID)
				continue
			}

			r.logger.Printf("worker: processing job %s (%s)", msg.JobID, msg.Request.ModelID)
			r.jobs.ProcessJob(job, msg.Request)

			if err := r.queue.Ack(ctx, msgID); err != nil {
				r.logger.Printf("worker: failed to ack message %s: %v", msgID, err)
			} else {
				r.observeQueueDepth(ctx)
			}
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

func (r *Runner) observeQueueDepth(ctx context.Context) {
	if r.queue == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	depth, err := r.queue.Pending(ctx)
	if err != nil {
		r.logger.Printf("worker: failed to inspect queue depth: %v", err)
		return
	}
	metrics.SetJobQueueDepth(depth)
}
