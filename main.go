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
)

var (
	// Configuration from environment
	modelCatalogRoot       = getEnv("MODEL_CATALOG_ROOT", "/workspace/catalog")
	modelCatalogModelsSubdir = getEnv("MODEL_CATALOG_MODELS_SUBDIR", "models")
	activeNamespace        = getEnv("ACTIVE_NAMESPACE", "ai")
	activeInferenceServiceName = getEnv("ACTIVE_INFERENCESERVICE_NAME", "active-llm")
)

// Global instances
var (
	catalog       *ModelCatalog
	kserveClient  *KServeClient
)

func main() {
	// Initialize logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Model Manager (Go version)")
	log.Printf("Catalog root: %s", modelCatalogRoot)
	log.Printf("Models subdir: %s", modelCatalogModelsSubdir)
	log.Printf("Active namespace: %s", activeNamespace)
	log.Printf("Active InferenceService: %s", activeInferenceServiceName)

	// Initialize components
	catalog = NewModelCatalog(modelCatalogRoot, modelCatalogModelsSubdir)
	if err := catalog.LoadCatalog(); err != nil {
		log.Fatalf("Failed to load catalog: %v", err)
	}
	log.Printf("Loaded %d models from catalog", len(catalog.Models))

	var err error
	kserveClient, err = NewKServeClient(activeNamespace, activeInferenceServiceName)
	if err != nil {
		log.Fatalf("Failed to initialize KServe client: %v", err)
	}

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(ginLogger())

	// Register routes
	router.GET("/healthz", healthHandler)
	router.GET("/models", listModelsHandler)
	router.GET("/models/:id", getModelHandler)
	router.POST("/models/activate", activateModelHandler)
	router.POST("/models/deactivate", deactivateModelHandler)
	router.GET("/active", getActiveModelHandler)
	router.POST("/refresh", refreshCatalogHandler)

	// Start HTTP server
	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	// Graceful shutdown
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Println("Server started on :8080")

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func ginLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		latency := time.Since(start)
		log.Printf("%s %s %d %s", c.Request.Method, path, c.Writer.Status(), latency)
	}
}
