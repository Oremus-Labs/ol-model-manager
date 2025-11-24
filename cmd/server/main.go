// Package main is the entry point for the model manager service.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oremus-labs/ol-model-manager/config"
	"github.com/oremus-labs/ol-model-manager/internal/api"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/handlers"
	"github.com/oremus-labs/ol-model-manager/internal/hfcache"
	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
	"github.com/oremus-labs/ol-model-manager/internal/kube"
	"github.com/oremus-labs/ol-model-manager/internal/queue"
	"github.com/oremus-labs/ol-model-manager/internal/recommendations"
	"github.com/oremus-labs/ol-model-manager/internal/redisx"
	"github.com/oremus-labs/ol-model-manager/internal/status"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/validator"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"k8s.io/client-go/kubernetes"
)

const (
	version         = "0.4.18-go"
	shutdownTimeout = 5 * time.Second
)

var (
	weightUsageBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "model_manager_weights_used_bytes",
		Help: "Used bytes on the Venus PVC",
	})
)

func main() {
	// Initialize logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Model Manager v%s", version)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Load configuration
	cfg := config.Load()
	log.Printf("Configuration loaded - Catalog: %s/%s, Namespace: %s, InferenceService: %s",
		cfg.CatalogRoot, cfg.CatalogModelsDir, cfg.Namespace, cfg.InferenceServiceName)

	// Initialize catalog
	cat := catalog.New(cfg.CatalogRoot, cfg.CatalogModelsDir)
	if err := cat.Load(); err != nil {
		if errors.Is(err, catalog.ErrModelsDirMissing) {
			log.Printf("Catalog directory not ready yet (git-sync warming up): %v", err)
		} else {
			log.Fatalf("Failed to load catalog: %v", err)
		}
	} else {
		log.Printf("Loaded %d models from catalog", cat.Count())
	}

	// Build Kubernetes clients once so we can share configuration.
	kubeConfig, err := kube.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load Kubernetes config: %v", err)
	}

	// Initialize KServe client
	ksClient, err := kserve.NewClientWithConfig(kubeConfig, cfg.Namespace, cfg.InferenceServiceName, cfg.InferenceModelRoot)
	if err != nil {
		log.Fatalf("Failed to initialize KServe client: %v", err)
	}

	coreClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("Failed to initialize Kubernetes clientset: %v", err)
	}

	// Initialize weights/vLLM services
	weightManager := weights.New(cfg.WeightsStoragePath)
	vllmDiscovery := vllm.New(
		vllm.WithGitHubToken(cfg.GitHubToken),
		vllm.WithHuggingFaceToken(cfg.HuggingFaceToken),
		vllm.WithHuggingFaceCacheTTL(cfg.HuggingFaceCacheTTL),
		vllm.WithVLLMCacheTTL(cfg.VLLMCacheTTL),
	)

	stateStore, err := store.Open(cfg.DataStoreDSN, cfg.DataStoreDriver)
	if err != nil {
		log.Fatalf("Failed to initialize state store: %v", err)
	}
	defer stateStore.Close()

	if cat.Count() == 0 {
		if snapshot, updatedAt, err := stateStore.LoadCatalogSnapshot(); err == nil && len(snapshot) > 0 {
			cat.Restore(snapshot)
			log.Printf("Restored %d catalog entries from datastore snapshot updated at %s", len(snapshot), updatedAt.Format(time.RFC3339))
		} else if err != nil {
			log.Printf("Catalog snapshot not available: %v", err)
		}
	}
	if cat.Count() > 0 {
		if err := stateStore.SaveCatalogSnapshot(cat.All()); err != nil {
			log.Printf("Failed to persist initial catalog snapshot: %v", err)
		}
	}

	redisClient, err := redisx.NewClient(redisx.Config{
		Addr:        cfg.RedisAddr,
		Username:    cfg.RedisUsername,
		Password:    cfg.RedisPassword,
		DB:          cfg.RedisDB,
		TLSEnabled:  cfg.RedisTLSEnabled,
		TLSInsecure: cfg.RedisTLSInsecure,
	})
	if err != nil {
		log.Fatalf("Failed to initialize Redis client: %v", err)
	}
	if redisClient != nil {
		defer redisClient.Close()
	}
	eventBus := events.NewBus(events.Options{
		Client:  redisClient,
		Logger:  log.Default(),
		Channel: cfg.EventsChannel,
	})

	var runtimeStatus status.Provider
	statusManager, err := status.NewManager(kubeConfig, cfg.Namespace, cfg.InferenceServiceName, eventBus)
	if err != nil {
		log.Printf("Failed to initialize runtime status manager: %v", err)
	} else {
		runtimeStatus = statusManager
		go func() {
			if err := statusManager.Run(rootCtx); err != nil && err != context.Canceled {
				log.Printf("Status manager exited: %v", err)
			}
		}()
	}

	hfCache := hfcache.New(hfcache.Options{
		Store:    stateStore,
		Redis:    redisClient,
		Logger:   log.Default(),
		TTL:      cfg.HuggingFaceCacheTTL,
		KeySpace: "hf:models",
	})

	var jobQueue *queue.Producer
	if redisClient != nil {
		jobQueue = queue.NewProducer(redisClient, cfg.RedisJobStream)
	}

	jobManager := jobs.New(jobs.Options{
		Store:              stateStore,
		Weights:            weightManager,
		HuggingFaceToken:   cfg.HuggingFaceToken,
		WeightsPVCName:     cfg.WeightsPVCName,
		InferenceModelRoot: cfg.InferenceModelRoot,
		EventPublisher:     eventBus,
	})

	// Initialize catalog validator
	catalogValidator, err := validator.New(validator.Options{
		SchemaPath:         cfg.CatalogSchemaPath,
		Namespace:          cfg.ValidationNamespace,
		KubernetesClient:   coreClient,
		WeightsPVCName:     cfg.WeightsPVCName,
		InferenceModelRoot: cfg.InferenceModelRoot,
		GPUProfilePath:     cfg.GPUProfilesPath,
	})
	if err != nil {
		log.Fatalf("Failed to initialize catalog validator: %v", err)
	}

	var advisor *recommendations.Engine
	if cfg.GPUProfilesPath != "" {
		profiles, err := recommendations.LoadProfiles(cfg.GPUProfilesPath)
		if err != nil {
			log.Printf("Failed to load GPU profiles: %v", err)
		} else {
			advisor = recommendations.New(profiles)
		}
	}

	var catWriter *catalogwriter.Writer
	if cfg.CatalogRepo != "" {
		catWriter, err = catalogwriter.New(catalogwriter.Options{
			Root:        cfg.CatalogRoot,
			ModelsDir:   cfg.CatalogModelsDir,
			RepoSlug:    cfg.CatalogRepo,
			BaseBranch:  cfg.CatalogBaseBranch,
			AuthorName:  cfg.GitAuthorName,
			AuthorEmail: cfg.GitAuthorEmail,
		})
		if err != nil {
			log.Fatalf("Failed to initialize catalog writer: %v", err)
		}
	} else {
		log.Println("Catalog writer disabled (CATALOG_REPO not set)")
	}

	// Initialize handlers
	h := handlers.New(cat, ksClient, weightManager, vllmDiscovery, catalogValidator, catWriter, advisor, stateStore, jobManager, eventBus, jobQueue, hfCache, runtimeStatus, handlers.Options{
		CatalogTTL:             cfg.CatalogRefreshInterval,
		WeightsInstallTimeout:  cfg.WeightsInstallTimeout,
		HuggingFaceToken:       cfg.HuggingFaceToken,
		GitHubToken:            cfg.GitHubToken,
		WeightsPVCName:         cfg.WeightsPVCName,
		InferenceModelRoot:     cfg.InferenceModelRoot,
		HistoryLimit:           100,
		Version:                version,
		CatalogRoot:            cfg.CatalogRoot,
		CatalogModelsDir:       cfg.CatalogModelsDir,
		WeightsPath:            cfg.WeightsStoragePath,
		StatePath:              cfg.StatePath,
		AuthEnabled:            cfg.APIToken != "",
		HuggingFaceCacheTTL:    cfg.HuggingFaceCacheTTL,
		VLLMCacheTTL:           cfg.VLLMCacheTTL,
		RecommendationCacheTTL: cfg.RecommendationCacheTTL,
		DataStoreDriver:        cfg.DataStoreDriver,
		DataStoreDSN:           cfg.DataStoreDSN,
		DatabasePVCName:        cfg.DatabasePVCName,
		GPUProfilesPath:        cfg.GPUProfilesPath,
		GPUInventorySource:     cfg.GPUInventorySource,
		SlackWebhookURL:        cfg.SlackWebhookURL,
		PVCAlertThreshold:      cfg.PVCAlertThreshold,
	})

	startWeightMonitor(rootCtx, weightManager)

	// Setup HTTP server
	server := api.NewServer(h, api.Options{APIToken: cfg.APIToken})
	srv := server.Start(":" + cfg.ServerPort)
	log.Printf("Server listening on :%s", cfg.ServerPort)

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	rootCancel()
	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped")
}

func startWeightMonitor(ctx context.Context, wm *weights.Manager) {
	if wm == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stats, err := wm.GetStats()
				if err != nil {
					log.Printf("Failed to collect weight stats: %v", err)
					continue
				}
				weightUsageBytes.Set(float64(stats.UsedBytes))
			}
		}
	}()
}
