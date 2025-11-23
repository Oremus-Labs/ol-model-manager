// Package main is the entry point for the model manager service.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/oremus-labs/ol-model-manager/config"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/handlers"
	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
	"github.com/oremus-labs/ol-model-manager/internal/kube"
	"github.com/oremus-labs/ol-model-manager/internal/recommendations"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/validator"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
)

const (
	version         = "0.4.7-go"
	shutdownTimeout = 5 * time.Second
)

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "model_manager_http_requests_total",
		Help: "Total HTTP requests processed by the model manager",
	}, []string{"method", "path", "status"})
	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "model_manager_http_request_duration_seconds",
		Help:    "HTTP request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
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
		log.Fatalf("Failed to load catalog: %v", err)
	}
	log.Printf("Loaded %d models from catalog", cat.Count())

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

	jobManager := jobs.New(stateStore, weightManager, cfg.HuggingFaceToken)

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
	h := handlers.New(cat, ksClient, weightManager, vllmDiscovery, catalogValidator, catWriter, advisor, stateStore, jobManager, handlers.Options{
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
	router := setupRouter(h, cfg.APIToken)
	srv := &http.Server{
		Addr:         ":" + cfg.ServerPort,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.Printf("Server listening on :%s", cfg.ServerPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

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

func setupRouter(h *handlers.Handler, apiToken string) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Recovery(), requestIDMiddleware(), metricsMiddleware(), requestLogger())

	// Health check
	router.GET("/healthz", h.Health)
	router.GET("/system/info", h.SystemInfo)
	router.GET("/openapi", h.OpenAPISpec)
	router.GET("/docs", h.APIDocs)
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Model endpoints
	router.GET("/models", h.ListModels)
	router.GET("/models/:id", h.GetModel)
	router.GET("/models/:id/compatibility", h.ModelCompatibility)
	router.GET("/models/:id/manifest", h.GetModelManifest)
	router.GET("/active", h.GetActiveModel)
	router.POST("/catalog/generate", h.GenerateCatalogEntry)
	router.GET("/recommendations/:gpuType", h.GPURecommendations)
	router.GET("/recommendations/profiles", h.ListProfiles)

	// Weight endpoints
	router.GET("/weights", h.ListWeights)
	router.GET("/weights/usage", h.GetWeightUsage)
	router.GET("/weights/:name/info", h.GetWeightInfo)

	// HuggingFace discovery
	router.GET("/huggingface/search", h.SearchHuggingFace)
	router.GET("/huggingface/models/*id", h.GetHuggingFaceModel)

	// vLLM discovery endpoints
	router.GET("/vllm/supported-models", h.ListVLLMArchitectures)
	router.GET("/vllm/model/:architecture", h.GetVLLMArchitecture)
	router.POST("/vllm/discover", h.DiscoverModel)
	router.POST("/vllm/model-info", h.DescribeVLLMModel)

	protected := router.Group("/")
	protected.Use(authMiddleware(apiToken))
	protected.POST("/models/activate", h.ActivateModel)
	protected.POST("/models/deactivate", h.DeactivateModel)
	protected.POST("/models/test", h.TestModel)
	protected.POST("/catalog/preview", h.PreviewCatalog)
	protected.POST("/refresh", h.RefreshCatalog)
	protected.POST("/catalog/validate", h.ValidateCatalog)
	protected.POST("/catalog/pr", h.CreateCatalogPR)
	protected.POST("/weights/install", h.InstallWeights)
	protected.DELETE("/weights/:name", h.DeleteWeights)
	protected.GET("/weights/install/status/:id", h.GetJob)
	protected.GET("/jobs", h.ListJobs)
	protected.GET("/jobs/:id", h.GetJob)
	protected.GET("/history", h.ListHistory)

	return router
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()

		requestID, _ := c.Get("requestID")
		log.Printf("%s %s %d %s request_id=%v", method, path, statusCode, latency, requestID)
	}
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		c.Set("requestID", id)
		c.Writer.Header().Set("X-Request-ID", id)
		c.Next()
	}
}

func metricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		status := fmt.Sprintf("%d", c.Writer.Status())
		httpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}

func authMiddleware(token string) gin.HandlerFunc {
	if token == "" {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if strings.HasPrefix(header, "Bearer ") {
			header = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		}
		if header == "" {
			header = c.GetHeader("X-API-Key")
		}

		if header != token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
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
