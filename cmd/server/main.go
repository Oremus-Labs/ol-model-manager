// Package main is the entry point for the model manager service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/config"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/handlers"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

const (
	version         = "0.3.2-go"
	shutdownTimeout = 5 * time.Second
)

func main() {
	// Initialize logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Model Manager v%s", version)

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

	// Initialize KServe client
	ksClient, err := kserve.NewClient(cfg.Namespace, cfg.InferenceServiceName)
	if err != nil {
		log.Fatalf("Failed to initialize KServe client: %v", err)
	}

	// Initialize weights/vLLM services
	weightManager := weights.New(cfg.WeightsStoragePath)
	vllmDiscovery := vllm.New(
		vllm.WithGitHubToken(cfg.GitHubToken),
		vllm.WithHuggingFaceToken(cfg.HuggingFaceToken),
	)

	// Initialize handlers
	h := handlers.New(cat, ksClient, weightManager, vllmDiscovery, handlers.Options{
		CatalogTTL:            cfg.CatalogRefreshInterval,
		WeightsInstallTimeout: cfg.WeightsInstallTimeout,
		HuggingFaceToken:      cfg.HuggingFaceToken,
		WeightsPVCName:        cfg.WeightsPVCName,
		InferenceModelRoot:    cfg.InferenceModelRoot,
	})

	// Setup HTTP server
	router := setupRouter(h)
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

	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped")
}

func setupRouter(h *handlers.Handler) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestLogger())

	// Health check
	router.GET("/healthz", h.Health)

	// Model endpoints
	router.GET("/models", h.ListModels)
	router.GET("/models/:id", h.GetModel)
	router.POST("/models/activate", h.ActivateModel)
	router.POST("/models/deactivate", h.DeactivateModel)
	router.GET("/active", h.GetActiveModel)
	router.POST("/refresh", h.RefreshCatalog)

	// Weight endpoints
	router.GET("/weights", h.ListWeights)
	router.GET("/weights/usage", h.GetWeightUsage)
	router.GET("/weights/:name/info", h.GetWeightInfo)
	router.DELETE("/weights/:name", h.DeleteWeights)
	router.POST("/weights/install", h.InstallWeights)

	// vLLM discovery endpoints
	router.GET("/vllm/supported-models", h.ListVLLMArchitectures)
	router.POST("/vllm/discover", h.DiscoverModel)

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

		log.Printf("%s %s %d %s", method, path, statusCode, latency)
	}
}
