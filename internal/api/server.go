package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/internal/handlers"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Options configures the HTTP server wiring.
type Options struct {
	APIToken       string
	GraphQLHandler http.Handler
}

// Server wraps the Gin engine and associated configuration.
type Server struct {
	engine *gin.Engine
}

// NewServer constructs a Server with all HTTP routes configured.
func NewServer(handler *handlers.Handler, opts Options) *Server {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.Use(gin.Recovery(), requestIDMiddleware(), metricsMiddleware(), requestLogger())

	// Health + meta
	engine.GET("/healthz", handler.Health)
	engine.GET("/system/info", handler.SystemInfo)
	engine.GET("/system/summary", handler.SystemSummary)
	engine.GET("/openapi", handler.OpenAPISpec)
	engine.GET("/docs", handler.APIDocs)
	engine.GET("/events", handler.StreamEvents)
	engine.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// Models
	engine.GET("/models", handler.ListModels)
	engine.GET("/models/:id", handler.GetModel)
	engine.GET("/models/:id/compatibility", handler.ModelCompatibility)
	engine.GET("/models/:id/manifest", handler.GetModelManifest)
	engine.GET("/models/status", handler.GetRuntimeStatus)
	engine.GET("/active", handler.GetActiveModel)
	engine.POST("/catalog/generate", handler.GenerateCatalogEntry)
	engine.GET("/recommendations/:gpuType", handler.GPURecommendations)
	engine.GET("/recommendations/profiles", handler.ListProfiles)

	// Weights
	engine.GET("/weights", handler.ListWeights)
	engine.GET("/weights/usage", handler.GetWeightUsage)
	engine.GET("/weights/info", handler.GetWeightInfo)

	// HuggingFace discovery
	engine.GET("/huggingface/search", handler.SearchHuggingFace)
	engine.GET("/huggingface/models/*id", handler.GetHuggingFaceModel)

	if opts.GraphQLHandler != nil {
		engine.GET("/graphql", gin.WrapH(opts.GraphQLHandler))
		engine.POST("/graphql", gin.WrapH(opts.GraphQLHandler))
	}

	// vLLM discovery
	engine.GET("/vllm/supported-models", handler.ListVLLMArchitectures)
	engine.GET("/vllm/model/:architecture", handler.GetVLLMArchitecture)
	engine.POST("/vllm/discover", handler.DiscoverModel)
	engine.POST("/vllm/model-info", handler.DescribeVLLMModel)

	protected := engine.Group("/")
	protected.Use(authMiddleware(opts.APIToken))

	protected.POST("/models/activate", handler.ActivateModel)
	protected.POST("/models/deactivate", handler.DeactivateModel)
	protected.POST("/runtime/activate", handler.RuntimeActivate)
	protected.POST("/runtime/deactivate", handler.RuntimeDeactivate)
	protected.POST("/runtime/promote", handler.RuntimePromote)
	protected.POST("/models/test", handler.TestModel)
	protected.POST("/catalog/preview", handler.PreviewCatalog)
	protected.POST("/refresh", handler.RefreshCatalog)
	protected.POST("/catalog/validate", handler.ValidateCatalog)
	protected.POST("/catalog/pr", handler.CreateCatalogPR)
	protected.POST("/weights/install", handler.InstallWeights)
	protected.DELETE("/weights", handler.DeleteWeights)
	protected.GET("/weights/install/status/:id", handler.GetJob)
	protected.GET("/jobs", handler.ListJobs)
	protected.GET("/jobs/:id", handler.GetJob)
	protected.GET("/jobs/:id/logs", handler.JobLogs)
	protected.POST("/jobs/:id/cancel", handler.CancelJob)
	protected.POST("/jobs/:id/retry", handler.RetryJob)
	protected.DELETE("/jobs", handler.DeleteJobs)
	protected.GET("/history", handler.ListHistory)
	protected.DELETE("/history", handler.ClearHistory)
	protected.GET("/secrets", handler.ListSecrets)
	protected.GET("/secrets/:name", handler.GetSecret)
	protected.PUT("/secrets/:name", handler.ApplySecret)
	protected.DELETE("/secrets/:name", handler.DeleteSecret)
	protected.GET("/notifications", handler.ListNotifications)
	protected.PUT("/notifications/:name", handler.ApplyNotification)
	protected.DELETE("/notifications/:name", handler.DeleteNotification)
	protected.POST("/notifications/test", handler.TestNotification)
	protected.GET("/tokens", handler.ListTokens)
	protected.POST("/tokens", handler.IssueToken)
	protected.DELETE("/tokens/:id", handler.DeleteToken)
	protected.GET("/policies", handler.ListPolicies)
	protected.PUT("/policies/:name", handler.ApplyPolicy)
	protected.DELETE("/policies/:name", handler.DeletePolicy)
	protected.GET("/playbooks", handler.ListPlaybooks)
	protected.GET("/playbooks/:name", handler.GetPlaybook)
	protected.PUT("/playbooks/:name", handler.ApplyPlaybook)
	protected.DELETE("/playbooks/:name", handler.DeletePlaybook)
	protected.POST("/playbooks/:name/run", handler.RunPlaybook)
	protected.GET("/backups", handler.ListBackups)
	protected.POST("/backups", handler.RecordBackup)
	protected.POST("/cleanup/weights", handler.CleanupWeights)

	return &Server{engine: engine}
}

// Engine exposes the underlying Gin engine for advanced use (testing, etc.).
func (s *Server) Engine() *gin.Engine {
	return s.engine
}

// Start launches the HTTP server on the provided address.
func (s *Server) Start(addr string) *http.Server {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.engine,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	return srv
}
