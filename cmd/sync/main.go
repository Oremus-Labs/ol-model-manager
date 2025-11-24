// Package main boots the HuggingFace/vLLM synchronization service scaffold.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/oremus-labs/ol-model-manager/config"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/hfcache"
	"github.com/oremus-labs/ol-model-manager/internal/redisx"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/syncsvc"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
)

const syncVersion = "0.4.18-go"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Model Manager sync service v%s", syncVersion)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := config.Load()
	discovery := vllm.New(
		vllm.WithGitHubToken(cfg.GitHubToken),
		vllm.WithHuggingFaceToken(cfg.HuggingFaceToken),
		vllm.WithHuggingFaceCacheTTL(cfg.HuggingFaceCacheTTL),
		vllm.WithVLLMCacheTTL(cfg.VLLMCacheTTL),
	)

	stateStore, err := store.Open(cfg.DataStoreDSN, cfg.DataStoreDriver)
	if err != nil {
		log.Fatalf("failed to initialize datastore: %v", err)
	}
	defer stateStore.Close()

	redisClient, err := redisx.NewClient(redisx.Config{
		Addr:        cfg.RedisAddr,
		Username:    cfg.RedisUsername,
		Password:    cfg.RedisPassword,
		DB:          cfg.RedisDB,
		TLSEnabled:  cfg.RedisTLSEnabled,
		TLSInsecure: cfg.RedisTLSInsecure,
	})
	if err != nil {
		log.Fatalf("failed to initialize redis: %v", err)
	}
	if redisClient != nil {
		defer redisClient.Close()
	}

	eventBus := events.NewBus(events.Options{
		Client:  redisClient,
		Logger:  log.Default(),
		Channel: cfg.EventsChannel,
	})

	hfCache := hfcache.New(hfcache.Options{
		Store:    stateStore,
		Redis:    redisClient,
		Logger:   log.Default(),
		TTL:      cfg.HuggingFaceCacheTTL,
		KeySpace: "hf:models",
	})

	service := syncsvc.New(syncsvc.Options{
		Discovery: discovery,
		Cache:     hfCache,
		EventBus:  eventBus,
		Logger:    log.Default(),
		Interval:  cfg.HuggingFaceSyncInterval,
		Queries:   buildSyncQueries(cfg),
	})

	if err := service.Run(ctx); err != nil && err != context.Canceled {
		log.Printf("sync service stopped: %v", err)
		os.Exit(1)
	}
	log.Println("sync service exited cleanly")
}

func buildSyncQueries(cfg *config.Config) []vllm.SearchOptions {
	limit := cfg.HuggingFaceSyncLimit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	seen := make(map[string]struct{})
	var queries []vllm.SearchOptions

	appendQuery := func(key string, opt vllm.SearchOptions) {
		if _, ok := seen[key]; ok {
			return
		}
		opt.Limit = limit
		seen[key] = struct{}{}
		queries = append(queries, opt)
	}

	appendQuery("global", vllm.SearchOptions{
		Sort:      "downloads",
		Direction: "-1",
	})

	for _, tag := range cfg.HuggingFaceSyncPipelineTags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		appendQuery("pipeline:"+tag, vllm.SearchOptions{
			PipelineTag: tag,
			Sort:        "downloads",
			Direction:   "-1",
		})
	}

	for _, term := range cfg.HuggingFaceSyncSearchTerms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		appendQuery("query:"+strings.ToLower(term), vllm.SearchOptions{
			Query:     term,
			Sort:      "downloads",
			Direction: "-1",
		})
	}

	return queries
}
