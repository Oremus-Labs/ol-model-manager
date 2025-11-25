package main

import (
	"context"
	"log"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/handlers"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

type automationOptions struct {
	Store      *store.Store
	Weights    *weights.Manager
	Handler    *handlers.Handler
	Interval   time.Duration
	JobTTL     time.Duration
	HistoryTTL time.Duration
	WeightTTL  time.Duration
}

func startAutomation(ctx context.Context, opts automationOptions) {
	if opts.Store == nil || opts.Interval <= 0 {
		return
	}
	log.Printf("Starting automation loop: interval=%s jobTTL=%s historyTTL=%s weightTTL=%s",
		opts.Interval, opts.JobTTL, opts.HistoryTTL, opts.WeightTTL)
	ticker := time.NewTicker(opts.Interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runAutomationSweep(opts)
			}
		}
	}()
}

func runAutomationSweep(opts automationOptions) {
	now := time.Now().UTC()
	if opts.JobTTL > 0 {
		before := now.Add(-opts.JobTTL)
		if removed, err := opts.Store.CleanupJobsBefore(before, store.JobDone, store.JobFailed, store.JobCancelled); err == nil && removed > 0 {
			log.Printf("automation: purged %d stale jobs", removed)
		}
	}
	if opts.HistoryTTL > 0 {
		before := now.Add(-opts.HistoryTTL)
		if removed, err := opts.Store.CleanupHistoryBefore(before); err == nil && removed > 0 {
			log.Printf("automation: purged %d history entries", removed)
		}
	}
	if opts.WeightTTL > 0 && opts.Weights != nil {
		if removed, err := opts.Weights.PruneOlderThan(opts.WeightTTL); err == nil && len(removed) > 0 {
			log.Printf("automation: pruned %d cached weight directories", len(removed))
		}
	}
}
