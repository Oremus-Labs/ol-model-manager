package syncsvc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/logutil"
	"github.com/oremus-labs/ol-model-manager/internal/metrics"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
)

type cacheProvider interface {
	Save(context.Context, []vllm.HuggingFaceModel) error
}

type eventPublisher interface {
	Publish(context.Context, events.Event) error
}

// Service periodically refreshes Hugging Face metadata.
type Service struct {
	discovery *vllm.Discovery
	cache     cacheProvider
	events    eventPublisher
	logger    *log.Logger
	interval  time.Duration
	queries   []vllm.SearchOptions
}

// Options configure the Service.
type Options struct {
	Discovery *vllm.Discovery
	Cache     cacheProvider
	EventBus  eventPublisher
	Logger    *log.Logger
	Interval  time.Duration
	Queries   []vllm.SearchOptions
}

// New creates a new sync service.
func New(opts Options) *Service {
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	queries := opts.Queries
	if len(queries) == 0 {
		queries = []vllm.SearchOptions{
			{PipelineTag: "text-generation", Sort: "downloads", Direction: "-1", Limit: 50},
			{PipelineTag: "text2text-generation", Sort: "downloads", Direction: "-1", Limit: 50},
		}
	}
	return &Service{
		discovery: opts.Discovery,
		cache:     opts.Cache,
		events:    opts.EventBus,
		logger:    opts.Logger,
		interval:  interval,
		queries:   queries,
	}
}

// Run starts the refresh loop.
func (s *Service) Run(ctx context.Context) error {
	s.logger.Println("huggingface sync service started")

	if err := s.refresh(ctx); err != nil {
		s.logger.Printf("initial refresh failed: %v", err)
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Println("sync service shutting down")
			return ctx.Err()
		case <-ticker.C:
			if err := s.refresh(ctx); err != nil {
				s.logger.Printf("sync refresh failed: %v", err)
			}
		}
	}
}

func (s *Service) refresh(ctx context.Context) error {
	if s.discovery == nil || s.cache == nil {
		return fmt.Errorf("sync service not configured")
	}
	started := time.Now().UTC()
	s.emitEvent(ctx, "hf.refresh.started", map[string]interface{}{
		"queryCount": len(s.queries),
	})
	seen := make(map[string]vllm.HuggingFaceModel)
	for _, query := range s.queries {
		results, err := s.discovery.SearchModels(query)
		if err != nil {
			s.logger.Printf("search failed for %v: %v", query, err)
			continue
		}
		for _, model := range results {
			if model == nil || model.HFModel == nil {
				continue
			}
			key := strings.ToLower(model.HFModel.ModelID)
			if key == "" {
				key = strings.ToLower(model.HFModel.ID)
			}
			if key == "" {
				continue
			}
			seen[key] = *model.HFModel
		}
	}
	if len(seen) == 0 {
		err := fmt.Errorf("no models discovered during refresh")
		s.emitEvent(ctx, "hf.refresh.failed", map[string]interface{}{
			"error": err.Error(),
		})
		metrics.ObserveHFRefresh(time.Since(started), 0, false)
		logutil.Error("hf_refresh_failed", err, map[string]interface{}{
			"queryCount": len(s.queries),
		})
		return err
	}
	models := make([]vllm.HuggingFaceModel, 0, len(seen))
	for _, model := range seen {
		models = append(models, model)
	}
	if err := s.cache.Save(ctx, models); err != nil {
		s.emitEvent(ctx, "hf.refresh.failed", map[string]interface{}{
			"error": err.Error(),
		})
		metrics.ObserveHFRefresh(time.Since(started), len(models), false)
		logutil.Error("hf_refresh_failed", err, map[string]interface{}{
			"count": len(models),
		})
		return err
	}
	s.emitEvent(ctx, "hf.refresh.completed", map[string]interface{}{
		"count":    len(models),
		"duration": time.Since(started).String(),
	})
	metrics.ObserveHFRefresh(time.Since(started), len(models), true)
	s.logger.Printf("refreshed %d Hugging Face models", len(models))
	logutil.Info("hf_refresh_completed", map[string]interface{}{
		"count":    len(models),
		"duration": time.Since(started).String(),
	})
	return nil
}

func (s *Service) emitEvent(ctx context.Context, eventType string, data map[string]interface{}) {
	if s.events == nil || eventType == "" {
		return
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	if err := s.events.Publish(ctx, events.Event{
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}); err != nil {
		s.logger.Printf("sync service: failed to publish %s event: %v", eventType, err)
	}
}
