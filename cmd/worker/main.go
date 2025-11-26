// Package main bootstraps the background worker service that will eventually
// process queued jobs (weight installs, cleanups, etc.).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oremus-labs/ol-model-manager/config"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/logutil"
	"github.com/oremus-labs/ol-model-manager/internal/queue"
	"github.com/oremus-labs/ol-model-manager/internal/redisx"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
	"github.com/oremus-labs/ol-model-manager/internal/worker"
)

const workerVersion = "0.5.29-go"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Model Manager worker v%s", workerVersion)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := config.Load()
	logutil.Info("worker_bootstrap", map[string]interface{}{
		"version":        workerVersion,
		"redisAddr":      cfg.RedisAddr,
		"redisJobStream": cfg.RedisJobStream,
		"redisJobGroup":  cfg.RedisJobGroup,
	})
	stateStore, err := store.Open(cfg.DataStoreDSN, cfg.DataStoreDriver)
	if err != nil {
		log.Fatalf("worker: failed to open datastore: %v", err)
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
		log.Fatalf("worker: failed to connect to redis: %v", err)
	}
	if redisClient != nil {
		defer redisClient.Close()
	}

	eventBus := events.NewBus(events.Options{
		Client:  redisClient,
		Logger:  log.Default(),
		Channel: cfg.EventsChannel,
	})

	weightManager := weights.New(cfg.WeightsStoragePath)
	jobManager := jobs.New(jobs.Options{
		Store:              stateStore,
		Weights:            weightManager,
		HuggingFaceToken:   cfg.HuggingFaceToken,
		WeightsPVCName:     cfg.WeightsPVCName,
		InferenceModelRoot: cfg.InferenceModelRoot,
		EventPublisher:     eventBus,
	})

	var jobConsumer *queue.Consumer
	if redisClient != nil {
		host, _ := os.Hostname()
		consumerName := fmt.Sprintf("%s-%d", host, time.Now().UnixNano())
		jobConsumer = queue.NewConsumer(redisClient, cfg.RedisJobStream, cfg.RedisJobGroup, consumerName)
	}

	runner := worker.New(worker.Options{
		Store:    stateStore,
		Jobs:     jobManager,
		Logger:   log.Default(),
		Interval: 1 * time.Minute,
		Queue:    jobConsumer,
	})

	if err := runner.Run(ctx); err != nil && err != context.Canceled {
		log.Printf("worker stopped: %v", err)
		os.Exit(1)
	}
	log.Println("worker exited cleanly")
}
