// Package handlers provides HTTP request handlers for the model manager API.
package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
	"github.com/oremus-labs/ol-model-manager/internal/logutil"
	"github.com/oremus-labs/ol-model-manager/internal/metrics"
	"github.com/oremus-labs/ol-model-manager/internal/openapi"
	"github.com/oremus-labs/ol-model-manager/internal/queue"
	"github.com/oremus-labs/ol-model-manager/internal/recommendations"
	"github.com/oremus-labs/ol-model-manager/internal/secrets"
	"github.com/oremus-labs/ol-model-manager/internal/status"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/validator"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
	"sigs.k8s.io/yaml"
)

// Options configures handler runtime behavior.
type Options struct {
	CatalogTTL             time.Duration
	WeightsInstallTimeout  time.Duration
	HuggingFaceToken       string
	GitHubToken            string
	WeightsPVCName         string
	InferenceModelRoot     string
	HistoryLimit           int
	Version                string
	CatalogRoot            string
	CatalogModelsDir       string
	WeightsPath            string
	StatePath              string
	AuthEnabled            bool
	HuggingFaceCacheTTL    time.Duration
	VLLMCacheTTL           time.Duration
	RecommendationCacheTTL time.Duration
	DataStoreDriver        string
	DataStoreDSN           string
	DatabasePVCName        string
	GPUProfilesPath        string
	GPUInventorySource     string
	SlackWebhookURL        string
	PVCAlertThreshold      float64
}

type weightStore interface {
	List() ([]weights.WeightInfo, error)
	Get(string) (*weights.WeightInfo, error)
	Delete(string) error
	GetStats() (*weights.StorageStats, error)
	InstallFromHuggingFace(context.Context, weights.InstallOptions) (*weights.WeightInfo, error)
}

type discoveryService interface {
	ListSupportedArchitectures() ([]vllm.ModelArchitecture, error)
	GetArchitectureDetail(string) (*vllm.ArchitectureDetail, error)
	GenerateModelConfig(vllm.GenerateRequest) (*catalog.Model, error)
	GetHuggingFaceModel(string) (*vllm.HuggingFaceModel, error)
	DescribeModel(string, bool) (*vllm.ModelInsight, error)
	SearchModels(vllm.SearchOptions) ([]*vllm.ModelInsight, error)
}

type catalogValidator interface {
	Validate(context.Context, []byte, *catalog.Model) validator.Result
}

type catalogWriter interface {
	Save(*catalog.Model) (*catalogwriter.SaveResult, error)
	CommitAndPush(context.Context, string, string, string, ...string) error
	CreatePullRequest(context.Context, catalogwriter.PullRequestOptions) (*catalogwriter.PullRequest, error)
}

type jobManager interface {
	EnqueueWeightInstall(jobs.InstallRequest) (*store.Job, error)
	CreateJob(jobs.InstallRequest) (*store.Job, error)
	ExecuteJob(*store.Job, jobs.InstallRequest)
}

type eventBus interface {
	Publish(context.Context, events.Event) error
	Subscribe(context.Context) (<-chan events.Event, func(), error)
}

type recommendationService interface {
	Compatibility(*catalog.Model, string) recommendations.CompatibilityReport
	Recommend(string) recommendations.Recommendation
	RecommendForModel(*catalog.Model, string) recommendations.Recommendation
	Profiles() []recommendations.GPUProfile
}

type secretManager interface {
	List(context.Context) ([]secrets.Meta, error)
	Get(context.Context, string) (*secrets.Record, error)
	Upsert(context.Context, string, map[string]string) (*secrets.Record, error)
	Delete(context.Context, string) error
}

// Handler encapsulates dependencies for HTTP handlers.
type huggingFaceCache interface {
	List(context.Context) ([]vllm.HuggingFaceModel, error)
	Get(context.Context, string) (*vllm.HuggingFaceModel, error)
}

type runtimeStatusProvider interface {
	CurrentStatus() status.RuntimeStatus
}

type Handler struct {
	catalog *catalog.Catalog
	kserve  *kserve.Client
	weights weightStore
	vllm    discoveryService
	checker catalogValidator
	writer  catalogWriter
	advisor recommendationService
	store   *store.Store
	jobs    jobManager
	events  eventBus
	queue   *queue.Producer
	hfCache huggingFaceCache
	runtime runtimeStatusProvider
	secrets secretManager
	opts    Options

	catalogMu          sync.Mutex
	lastCatalogRefresh time.Time
	catalogStatus      string
	catalogCacheTime   time.Time
}

type requestError struct {
	code    int
	message string
	err     error
}

func (e *requestError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *requestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newRequestError(code int, message string, err error) *requestError {
	return &requestError{code: code, message: message, err: err}
}

var errModelNotFound = errors.New("model not found")

// New creates a new Handler instance.
func New(cat *catalog.Catalog, ks *kserve.Client, wm weightStore, vdisc discoveryService, val catalogValidator, writer catalogWriter, advisor recommendationService, dataStore *store.Store, jobMgr jobManager, evt eventBus, q *queue.Producer, hfCache huggingFaceCache, runtime runtimeStatusProvider, secretMgr secretManager, opts Options) *Handler {
	if opts.CatalogTTL <= 0 {
		opts.CatalogTTL = time.Minute
	}
	if opts.WeightsInstallTimeout <= 0 {
		opts.WeightsInstallTimeout = 30 * time.Minute
	}
	if opts.InferenceModelRoot == "" {
		opts.InferenceModelRoot = "/mnt/models"
	}
	if opts.HistoryLimit <= 0 {
		opts.HistoryLimit = 50
	}
	if opts.WeightsPath == "" {
		opts.WeightsPath = opts.InferenceModelRoot
	}
	if opts.HuggingFaceCacheTTL <= 0 {
		opts.HuggingFaceCacheTTL = 5 * time.Minute
	}
	if opts.VLLMCacheTTL <= 0 {
		opts.VLLMCacheTTL = 10 * time.Minute
	}
	if opts.RecommendationCacheTTL <= 0 {
		opts.RecommendationCacheTTL = 15 * time.Minute
	}
	if opts.DataStoreDriver == "" {
		opts.DataStoreDriver = "bolt"
	}
	if opts.GPUInventorySource == "" {
		opts.GPUInventorySource = "k8s-nodes"
	}
	if opts.DatabasePVCName == "" {
		opts.DatabasePVCName = opts.WeightsPVCName
	}
	if opts.PVCAlertThreshold <= 0 {
		opts.PVCAlertThreshold = 0.85
	}

	if advisor != nil && isNilInterface(advisor) {
		advisor = nil
	}

	return &Handler{
		catalog:            cat,
		kserve:             ks,
		weights:            wm,
		vllm:               vdisc,
		checker:            val,
		writer:             writer,
		advisor:            advisor,
		store:              dataStore,
		jobs:               jobMgr,
		events:             evt,
		queue:              q,
		hfCache:            hfCache,
		runtime:            runtime,
		secrets:            secretMgr,
		opts:               opts,
		lastCatalogRefresh: time.Time{},
		catalogStatus:      "unknown",
	}
}

type activateRequest struct {
	ID string `json:"id" binding:"required"`
}

type runtimeActivateRequest struct {
	ModelID        string `json:"modelId" binding:"required"`
	Strategy       string `json:"strategy,omitempty"`
	TrafficPercent int    `json:"trafficPercent,omitempty"`
	Force          bool   `json:"force,omitempty"`
}

type runtimePromoteRequest struct {
	CandidateID    string `json:"candidateId" binding:"required"`
	CurrentID      string `json:"currentId,omitempty"`
	Strategy       string `json:"strategy,omitempty"`
	TrafficPercent int    `json:"trafficPercent,omitempty"`
	Force          bool   `json:"force,omitempty"`
}

type installWeightsRequest struct {
	HFModelID string   `json:"hfModelId" binding:"required"`
	Revision  string   `json:"revision,omitempty"`
	Target    string   `json:"target,omitempty"`
	Files     []string `json:"files,omitempty"`
	Overwrite bool     `json:"overwrite"`
}

type installScheduleResult struct {
	Async         bool
	Job           *store.Job
	Weight        *weights.WeightInfo
	Target        string
	StorageURI    string
	InferencePath string
}

type deleteWeightsRequest struct {
	Name string `json:"name" binding:"required"`
}

type modelInfoRequest struct {
	HFModelID  string `json:"hfModelId" binding:"required"`
	AutoDetect bool   `json:"autoDetect"`
}

type testModelRequest struct {
	ID             string `json:"id" binding:"required"`
	ReadinessURL   string `json:"readinessUrl,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type playbookApplyRequest struct {
	Description string          `json:"description,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Spec        json.RawMessage `json:"spec"`
}

type playbookSpec struct {
	Install  *playbookInstallStep  `json:"install,omitempty"`
	Activate *playbookActivateStep `json:"activate,omitempty"`
}

type playbookInstallStep struct {
	HFModelID string   `json:"hfModelId"`
	Revision  string   `json:"revision,omitempty"`
	Target    string   `json:"target,omitempty"`
	Files     []string `json:"files,omitempty"`
	Overwrite bool     `json:"overwrite"`
}

type playbookActivateStep struct {
	ModelID        string `json:"modelId"`
	Strategy       string `json:"strategy,omitempty"`
	WaitForInstall bool   `json:"waitForInstall"`
}

type generateCatalogRequest struct {
	HFModelID    string               `json:"hfModelId" binding:"required"`
	DisplayName  string               `json:"displayName,omitempty"`
	AutoDetect   bool                 `json:"autoDetect"`
	StorageURI   string               `json:"storageUri,omitempty"`
	Resources    *catalog.Resources   `json:"resources,omitempty"`
	NodeSelector map[string]string    `json:"nodeSelector,omitempty"`
	Tolerations  []catalog.Toleration `json:"tolerations,omitempty"`
	Env          []catalog.EnvVar     `json:"env,omitempty"`
}

type catalogPRRequest struct {
	Model    catalog.Model `json:"model" binding:"required"`
	Branch   string        `json:"branch,omitempty"`
	Base     string        `json:"base,omitempty"`
	Title    string        `json:"title,omitempty"`
	Body     string        `json:"body,omitempty"`
	Draft    bool          `json:"draft"`
	Validate bool          `json:"validate"`
}

// StreamEvents streams live control-plane events via SSE.
func (h *Handler) StreamEvents(c *gin.Context) {
	if h.events == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "event streaming unavailable"})
		return
	}

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	releaseGauge := metrics.TrackSSEConnection()
	logutil.Info("sse_stream_opened", map[string]interface{}{
		"clientIp":  c.ClientIP(),
		"userAgent": c.Request.UserAgent(),
	})
	defer func() {
		releaseGauge()
		fields := map[string]interface{}{
			"clientIp": c.ClientIP(),
		}
		if err := ctx.Err(); err != nil {
			fields["disconnectReason"] = err.Error()
		}
		logutil.Info("sse_stream_closed", fields)
	}()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	out := make(chan events.Event, 32)

	if h.store != nil {
		if jobs, err := h.store.ListJobs(5); err == nil && len(jobs) > 0 {
			seedID := fmt.Sprintf("seed-%d", time.Now().UnixNano())
			meta := gin.H{"count": len(jobs)}
			now := time.Now().UTC()
			out <- events.Event{
				ID:        seedID,
				Type:      "stream.seed.start",
				Timestamp: now,
				Data:      meta,
			}
			for i := len(jobs) - 1; i >= 0; i-- {
				job := jobs[i]
				evtTime := job.UpdatedAt
				if evtTime.IsZero() {
					evtTime = job.CreatedAt
				}
				out <- events.Event{
					ID:        job.ID,
					Type:      fmt.Sprintf("job.%s", job.Status),
					Timestamp: evtTime,
					Data:      job,
				}
			}
			out <- events.Event{
				ID:        seedID + ".complete",
				Type:      "stream.seed.complete",
				Timestamp: time.Now().UTC(),
				Data:      meta,
			}
		}
	}

	eventStream, unsubscribe, err := h.events.Subscribe(ctx)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to subscribe"})
		return
	}
	defer unsubscribe()

	go func() {
		for evt := range eventStream {
			select {
			case out <- evt:
			case <-ctx.Done():
				return
			}
		}
		close(out)
	}()

	c.Stream(func(w io.Writer) bool {
		select {
		case evt, ok := <-out:
			if !ok {
				return false
			}
			metrics.ObserveSSEEvent(evt.Type)
			c.Render(-1, sse.Event{
				Id:    evt.ID,
				Event: evt.Type,
				Data:  evt,
			})
			return true
		case <-ctx.Done():
			return false
		}
	})
}

// Health returns the health status of the service.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SystemInfo exposes metadata for UI bootstrapping.
func (h *Handler) SystemInfo(c *gin.Context) {
	if err := h.ensureCatalogFresh(false); err != nil {
		log.Printf("system info catalog refresh failed: %v", err)
	}

	catalogInfo := gin.H{
		"root":        h.opts.CatalogRoot,
		"modelsDir":   h.opts.CatalogModelsDir,
		"count":       0,
		"lastRefresh": h.lastCatalogRefresh,
		"status":      h.catalogStatus,
		"lastPersist": h.catalogCacheTime,
		"source":      "git",
	}
	if h.catalogStatus == "cache" {
		catalogInfo["source"] = "datastore"
	}
	if h.catalog != nil {
		catalogInfo["count"] = h.catalog.Count()
	}

	info := gin.H{
		"version": h.opts.Version,
		"catalog": catalogInfo,
		"weights": gin.H{
			"path":    h.opts.WeightsPath,
			"pvcName": h.opts.WeightsPVCName,
		},
		"state": gin.H{
			"path":    h.opts.StatePath,
			"enabled": h.store != nil,
		},
		"auth": gin.H{
			"enabled": h.opts.AuthEnabled,
		},
		"persistence": gin.H{
			"driver":   h.opts.DataStoreDriver,
			"dsn":      h.opts.DataStoreDSN,
			"pvcName":  h.opts.DatabasePVCName,
			"stateDir": h.opts.StatePath,
		},
		"cache": gin.H{
			"catalogTTL":         durationString(h.opts.CatalogTTL),
			"huggingfaceTTL":     durationString(h.opts.HuggingFaceCacheTTL),
			"vllmTTL":            durationString(h.opts.VLLMCacheTTL),
			"recommendationsTTL": durationString(h.opts.RecommendationCacheTTL),
		},
		"notifications": gin.H{
			"slackWebhookConfigured": h.opts.SlackWebhookURL != "",
			"pvcAlertThreshold":      h.opts.PVCAlertThreshold,
		},
		"gpu": gin.H{
			"profilesPath":    h.opts.GPUProfilesPath,
			"inventorySource": h.opts.GPUInventorySource,
		},
	}

	if h.weights != nil {
		if stats, err := h.weights.GetStats(); err == nil {
			info["storage"] = stats
		}
	}
	if h.advisor != nil {
		info["gpuProfiles"] = h.advisor.Profiles()
	}
	if h.store != nil {
		if jobs, err := h.store.ListJobs(10); err == nil {
			info["recentJobs"] = jobs
		}
		if history, err := h.store.ListHistory(5); err == nil {
			info["recentHistory"] = history
		}
	}

	c.JSON(http.StatusOK, info)
}

// SystemSummary aggregates key metrics for dashboards.
func (h *Handler) SystemSummary(c *gin.Context) {
	summary := gin.H{
		"version":   h.opts.Version,
		"timestamp": time.Now().UTC(),
	}

	if err := h.ensureCatalogFresh(false); err == nil && h.catalog != nil {
		summary["catalog"] = gin.H{
			"count":  h.catalog.Count(),
			"source": h.catalogStatus,
		}
	} else {
		summary["catalog"] = gin.H{"count": 0, "source": h.catalogStatus}
	}

	var storageStats *weights.StorageStats
	weightCard := gin.H{
		"path":    h.opts.WeightsPath,
		"pvcName": h.opts.WeightsPVCName,
	}
	if h.weights != nil {
		if stats, err := h.weights.GetStats(); err == nil && stats != nil {
			storageStats = stats
			weightCard["usage"] = stats
		}
		if infos, err := h.weights.List(); err == nil {
			weightCard["installed"] = len(infos)
		}
	}
	summary["weights"] = weightCard

	if h.runtime != nil {
		summary["runtime"] = h.runtime.CurrentStatus()
	}

	if h.store != nil {
		if counts, err := h.store.CountJobsByStatus(); err == nil {
			jobCard := gin.H{}
			for status, count := range counts {
				jobCard[string(status)] = count
			}
			summary["jobs"] = jobCard
		}
	} else {
		summary["jobs"] = gin.H{}
	}
	if _, ok := summary["jobs"]; !ok {
		summary["jobs"] = gin.H{}
	}

	if h.queue != nil {
		if depth, err := h.queue.Length(c.Request.Context()); err == nil {
			summary["queue"] = gin.H{"depth": depth}
		}
	}
	if _, ok := summary["queue"]; !ok {
		summary["queue"] = gin.H{"depth": 0}
	}

	if h.hfCache != nil {
		if models, err := h.hfCache.List(c.Request.Context()); err == nil {
			summary["huggingface"] = gin.H{"cachedModels": len(models)}
		}
	}
	if _, ok := summary["huggingface"]; !ok {
		summary["huggingface"] = gin.H{"cachedModels": 0}
	}

	summary["alerts"] = h.collectAlerts(storageStats)

	c.JSON(http.StatusOK, summary)
}

func (h *Handler) observeQueueDepth(ctx context.Context) {
	if h.queue == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if depth, err := h.queue.Length(ctx); err == nil {
		metrics.SetJobQueueDepth(depth)
	} else {
		log.Printf("failed to read queue depth: %v", err)
	}
}

func durationString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

// OpenAPISpec serves the OpenAPI document.
func (h *Handler) OpenAPISpec(c *gin.Context) {
	data, err := openapi.JSON()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to serialize OpenAPI document"})
		return
	}
	c.Data(http.StatusOK, "application/json", data)
}

// APIDocs serves a lightweight Swagger UI wrapper.
func (h *Handler) APIDocs(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(docsHTML))
}

// ListModels returns all available models.
func (h *Handler) ListModels(c *gin.Context) {
	if err := h.ensureCatalogFresh(false); err != nil {
		log.Printf("Failed to ensure catalog freshness: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model catalog"})
		return
	}

	c.JSON(http.StatusOK, h.catalog.All())
}

// GetModel returns details for a specific model.
func (h *Handler) GetModel(c *gin.Context) {
	if err := h.ensureCatalogFresh(false); err != nil {
		log.Printf("Failed to ensure catalog freshness: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model catalog"})
		return
	}

	modelID := c.Param("id")
	model := h.catalog.Get(modelID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}

	c.JSON(http.StatusOK, model)
}

// ActivateModel activates a model by creating/updating the InferenceService.
func (h *Handler) ActivateModel(c *gin.Context) {
	var req activateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	model, result, err := h.activateModelInternal(c.GetString("subject"), req.ID)
	if err != nil {
		h.respondActivationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"message":          "Model " + req.ID + " activated",
		"model":            model,
		"inferenceservice": result,
	})
}

// RuntimeActivate activates a model with runtime metadata/strategy hints.
func (h *Handler) RuntimeActivate(c *gin.Context) {
	var req runtimeActivateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	model, result, err := h.activateModelInternal(c.GetString("subject"), req.ModelID)
	if err != nil {
		h.respondActivationError(c, err)
		return
	}
	strategy := strings.TrimSpace(req.Strategy)
	if strategy == "" {
		strategy = "direct"
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"strategy":         strategy,
		"model":            model,
		"inferenceservice": result,
	})
}

// RuntimePromote promotes a staged model to active.
func (h *Handler) RuntimePromote(c *gin.Context) {
	var req runtimePromoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	currentID, err := h.currentRuntimeModelID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to inspect current runtime"})
		return
	}
	if req.CurrentID != "" && currentID != "" && req.CurrentID != currentID {
		c.JSON(http.StatusConflict, gin.H{
			"error":        "active model mismatch",
			"expected":     req.CurrentID,
			"currentModel": currentID,
		})
		return
	}
	model, result, err := h.activateModelInternal(c.GetString("subject"), req.CandidateID)
	if err != nil {
		h.respondActivationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":           "promoted",
		"previousModelId":  currentID,
		"model":            model,
		"inferenceservice": result,
	})
}

// RuntimeDeactivate deactivates the runtime for CLI/UI callers.
func (h *Handler) RuntimeDeactivate(c *gin.Context) {
	result, err := h.deactivateRuntime(c.GetString("subject"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Runtime deactivated",
		"result":  result,
	})
}

// DeactivateModel deactivates the active model.
func (h *Handler) DeactivateModel(c *gin.Context) {
	result, err := h.deactivateRuntime(c.GetString("subject"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Active model deactivated",
		"result":  result,
	})
}

func (h *Handler) activateModelInternal(subject, modelID string) (*catalog.Model, *kserve.Result, error) {
	if err := h.ensureCatalogFresh(true); err != nil {
		return nil, nil, err
	}
	model := h.catalog.Get(modelID)
	if model == nil {
		return nil, nil, errModelNotFound
	}
	meta := gin.H{
		"modelId":     modelID,
		"displayName": modelDisplayName(model),
		"storageUri":  model.StorageURI,
		"runtime":     model.Runtime,
		"hfModelId":   model.HFModelID,
		"requestedBy": subject,
		"requestedAt": time.Now().UTC(),
	}
	h.publishEvent("model.activation.started", meta)

	result, err := h.kserve.Activate(model)
	if err != nil {
		log.Printf("Failed to activate model %s: %v", modelID, err)
		failMeta := gin.H{
			"modelId":     modelID,
			"displayName": modelDisplayName(model),
			"error":       err.Error(),
		}
		h.publishEvent("model.activation.failed", failMeta)
		return nil, nil, err
	}

	successMeta := map[string]interface{}{
		"action":      result.Action,
		"modelId":     modelID,
		"displayName": modelDisplayName(model),
	}
	h.recordHistory("model_activated", modelID, successMeta)
	h.publishEvent("model.activation.completed", successMeta)
	return model, result, nil
}

func (h *Handler) respondActivationError(c *gin.Context, err error) {
	if errors.Is(err, errModelNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}
	if reqErr, ok := err.(*requestError); ok {
		c.JSON(reqErr.code, gin.H{"error": reqErr.message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func (h *Handler) deactivateRuntime(subject string) (*kserve.Result, error) {
	h.publishEvent("model.deactivation.started", gin.H{
		"requestedBy": subject,
		"requestedAt": time.Now().UTC(),
	})
	result, err := h.kserve.Deactivate()
	if err != nil {
		log.Printf("Failed to deactivate model: %v", err)
		h.publishEvent("model.deactivation.failed", gin.H{
			"error": err.Error(),
		})
		return nil, err
	}
	h.recordHistory("model_deactivated", "", map[string]interface{}{
		"action": result.Action,
	})
	h.publishEvent("model.deactivation.completed", gin.H{
		"action": result.Action,
	})
	return result, nil
}

func (h *Handler) currentRuntimeModelID() (string, error) {
	isvc, err := h.kserve.GetActive()
	if err != nil || isvc == nil {
		return "", err
	}
	meta, _ := isvc["metadata"].(map[string]interface{})
	if meta == nil {
		return "", nil
	}
	annotations, _ := meta["annotations"].(map[string]interface{})
	if annotations == nil {
		return "", nil
	}
	if val, ok := annotations["model-manager/model-id"].(string); ok {
		return val, nil
	}
	return "", nil
}

// GetActiveModel returns information about the currently active model.
func (h *Handler) GetActiveModel(c *gin.Context) {
	isvc, err := h.kserve.GetActive()
	if err != nil {
		log.Printf("Failed to get active model: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if isvc == nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "none",
			"message": "No active model",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "active",
		"inferenceservice": isvc,
	})
}

// RefreshCatalog forces a catalog reload.
func (h *Handler) RefreshCatalog(c *gin.Context) {
	log.Println("Manually refreshing model catalog")

	if err := h.ensureCatalogFresh(true); err != nil {
		log.Printf("Failed to refresh catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to refresh model catalog"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Catalog refreshed",
		"models":  h.catalog.All(),
	})
}

// ValidateCatalog runs schema/resource checks against a proposed catalog entry.
func (h *Handler) ValidateCatalog(c *gin.Context) {
	if h.checker == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "catalog validation is disabled"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var model catalog.Model
	if err := json.Unmarshal(body, &model); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model payload: " + err.Error()})
		return
	}

	result := h.checker.Validate(c.Request.Context(), body, &model)
	status := http.StatusOK
	if !result.Valid {
		status = http.StatusBadRequest
	}

	c.JSON(status, result)
}

// TestModel performs a dry-run activation (and optional readiness probe) for a model.
func (h *Handler) TestModel(c *gin.Context) {
	var req testModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.ensureCatalogFresh(false); err != nil {
		log.Printf("Failed to ensure catalog freshness: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model catalog"})
		return
	}

	model := h.catalog.Get(req.ID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}

	dryRun, err := h.kserve.DryRun(model)
	if err != nil {
		log.Printf("Dry-run failed for model %s: %v", req.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := gin.H{
		"status": "success",
		"dryRun": dryRun,
	}

	if req.ReadinessURL != "" {
		readiness := h.checkReadiness(c.Request.Context(), req.ReadinessURL, req.TimeoutSeconds)
		response["readiness"] = readiness
		if readiness["status"] != "ok" {
			response["status"] = "warning"
		}
	}

	c.JSON(http.StatusOK, response)
}

// GetRuntimeStatus returns the cached KServe/Knative runtime status.
func (h *Handler) GetRuntimeStatus(c *gin.Context) {
	if h.runtime == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "runtime status unavailable"})
		return
	}
	status := h.runtime.CurrentStatus()
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now().UTC()
	}
	c.JSON(http.StatusOK, status)
}

// ListWeights returns cached weights stored on Venus.
func (h *Handler) ListWeights(c *gin.Context) {
	if h.weights == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "weight management is disabled"})
		return
	}

	weights, err := h.weights.List()
	if err != nil {
		log.Printf("Failed to list weights: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list weights"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"weights": weights})
}

// GetWeightInfo returns information about a specific weight directory.
func (h *Handler) GetWeightInfo(c *gin.Context) {
	if h.weights == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "weight management is disabled"})
		return
	}

	name := strings.Trim(c.Query("name"), "/")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	info, err := h.weights.Get(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, info)
}

// DeleteWeights removes cached weights for a model.
func (h *Handler) DeleteWeights(c *gin.Context) {
	if h.weights == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "weight management is disabled"})
		return
	}

	var req deleteWeightsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.weights.Delete(req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Deleted weights for " + req.Name,
	})

	h.recordHistory("weight_deleted", req.Name, nil)
}

// DeleteJobs clears job records (optionally filtered by status).
func (h *Handler) DeleteJobs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if err := h.store.DeleteJobs(status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.recordHistory("jobs_purged", "", map[string]interface{}{"status": status})
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "filteredStatus": status})
}

// ClearHistory removes all history entries.
func (h *Handler) ClearHistory(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	if err := h.store.ClearHistory(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "cleared"})
}

// GetWeightUsage returns PVC usage statistics.
func (h *Handler) GetWeightUsage(c *gin.Context) {
	if h.weights == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "weight management is disabled"})
		return
	}

	stats, err := h.weights.GetStats()
	if err != nil {
		log.Printf("Failed to fetch storage stats: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch storage stats"})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// InstallWeights downloads HuggingFace model weights into the PVC.
func (h *Handler) InstallWeights(c *gin.Context) {
	var req installWeightsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := h.scheduleWeightInstall(c.Request.Context(), req)
	if err != nil {
		var reqErr *requestError
		if errors.As(err, &reqErr) {
			c.JSON(reqErr.code, gin.H{"error": reqErr.message})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	if result.Async {
		c.JSON(http.StatusAccepted, gin.H{
			"status":               "queued",
			"job":                  result.Job,
			"jobUrl":               fmt.Sprintf("/jobs/%s", result.Job.ID),
			"weightsInstallStatus": fmt.Sprintf("/weights/install/status/%s", result.Job.ID),
			"target":               result.Target,
			"storageUri":           result.StorageURI,
			"inferenceModelPath":   result.InferencePath,
		})
		return
	}

	info := result.Weight
	response := gin.H{
		"status":             "success",
		"model":              req.HFModelID,
		"weights":            info,
		"inferenceModelPath": result.InferencePath,
	}
	if result.StorageURI != "" {
		response["storageUri"] = result.StorageURI
		response["catalogInstructions"] = fmt.Sprintf("Set storageUri to %s and keep MODEL_ID (or equivalent env) pointed at %s", result.StorageURI, req.HFModelID)
	}

	h.recordHistory("weight_install_completed", req.HFModelID, map[string]interface{}{
		"target":      info.Name,
		"storageUri":  result.StorageURI,
		"modelPath":   result.InferencePath,
		"sizeBytes":   info.SizeBytes,
		"installedAt": info.InstalledAt,
	})

	c.JSON(http.StatusOK, response)
}

func (h *Handler) scheduleWeightInstall(ctx context.Context, req installWeightsRequest) (*installScheduleResult, error) {
	if h.weights == nil || h.vllm == nil {
		return nil, newRequestError(http.StatusNotImplemented, "weight installation is disabled", nil)
	}

	targetName, err := weights.CanonicalTarget(req.HFModelID, req.Target)
	if err != nil {
		return nil, newRequestError(http.StatusBadRequest, err.Error(), err)
	}
	req.Target = targetName

	hfModel, err := h.fetchAndValidateHFModel(req.HFModelID)
	if err != nil {
		return nil, newRequestError(http.StatusBadRequest, err.Error(), err)
	}

	files := req.Files
	if len(files) == 0 {
		files = vllm.CollectHuggingFaceFiles(hfModel)
	}
	if len(files) == 0 {
		return nil, newRequestError(http.StatusBadRequest, "no downloadable files found for model", nil)
	}

	storageURI := ""
	if h.opts.WeightsPVCName != "" {
		storageURI = fmt.Sprintf("pvc://%s/%s", h.opts.WeightsPVCName, targetName)
	}
	inferencePath := path.Join(h.opts.InferenceModelRoot, targetName)

	if h.jobs != nil {
		payload := jobs.InstallRequest{
			ModelID:   req.HFModelID,
			Revision:  req.Revision,
			Target:    req.Target,
			Files:     files,
			Overwrite: req.Overwrite,
		}
		job, err := h.jobs.CreateJob(payload)
		if err != nil {
			return nil, newRequestError(http.StatusInternalServerError, err.Error(), err)
		}

		runCtx := ctx
		if runCtx == nil {
			runCtx = context.Background()
		}
		if h.queue != nil {
			if err := h.queue.Enqueue(runCtx, job.ID, payload); err != nil {
				log.Printf("Failed to enqueue job to redis: %v, running inline", err)
				h.jobs.ExecuteJob(job, payload)
			} else {
				h.observeQueueDepth(runCtx)
			}
		} else {
			h.jobs.ExecuteJob(job, payload)
		}

		return &installScheduleResult{
			Async:         true,
			Job:           job,
			Target:        targetName,
			StorageURI:    storageURI,
			InferencePath: inferencePath,
		}, nil
	}

	timeout := h.opts.WeightsInstallTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(runCtx, timeout)
	defer cancel()

	info, err := h.weights.InstallFromHuggingFace(runCtx, weights.InstallOptions{
		ModelID:   req.HFModelID,
		Revision:  req.Revision,
		Target:    req.Target,
		Files:     files,
		Token:     h.opts.HuggingFaceToken,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		log.Printf("Failed to install weights for %s: %v", req.HFModelID, err)
		return nil, newRequestError(http.StatusInternalServerError, err.Error(), err)
	}

	storageURI = ""
	if h.opts.WeightsPVCName != "" {
		storageURI = fmt.Sprintf("pvc://%s/%s", h.opts.WeightsPVCName, info.Name)
	}
	modelPath := path.Join(h.opts.InferenceModelRoot, info.Name)

	return &installScheduleResult{
		Async:         false,
		Weight:        info,
		Target:        info.Name,
		StorageURI:    storageURI,
		InferencePath: modelPath,
	}, nil
}

// ListSecrets returns metadata for managed secrets.
func (h *Handler) ListSecrets(c *gin.Context) {
	if !h.ensureSecretManager(c) {
		return
	}
	items, err := h.secrets.List(c.Request.Context())
	if err != nil {
		log.Printf("Failed to list secrets: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list secrets"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"secrets": items})
}

// GetSecret returns the stored data for a secret.
func (h *Handler) GetSecret(c *gin.Context) {
	if !h.ensureSecretManager(c) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	record, err := h.secrets.Get(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "secret not found"})
			return
		}
		log.Printf("Failed to get secret %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch secret"})
		return
	}
	c.JSON(http.StatusOK, record)
}

// ApplySecret creates or updates a managed secret.
func (h *Handler) ApplySecret(c *gin.Context) {
	if !h.ensureSecretManager(c) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var req struct {
		Data map[string]string `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "data must include at least one key"})
		return
	}
	record, err := h.secrets.Upsert(c.Request.Context(), name, req.Data)
	if err != nil {
		log.Printf("Failed to apply secret %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save secret"})
		return
	}
	h.recordHistory("secret_applied", name, map[string]interface{}{"keys": len(req.Data)})
	c.JSON(http.StatusOK, record)
}

// DeleteSecret removes a managed secret.
func (h *Handler) DeleteSecret(c *gin.Context) {
	if !h.ensureSecretManager(c) {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if err := h.secrets.Delete(c.Request.Context(), name); err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "secret not found"})
			return
		}
		log.Printf("Failed to delete secret %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete secret"})
		return
	}
	h.recordHistory("secret_deleted", name, nil)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// ListPlaybooks returns stored playbook definitions.
func (h *Handler) ListPlaybooks(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	items, err := h.store.ListPlaybooks()
	if err != nil {
		log.Printf("Failed to list playbooks: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list playbooks"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"playbooks": items})
}

// GetPlaybook returns a single playbook record.
func (h *Handler) GetPlaybook(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	record, err := h.store.GetPlaybook(name)
	if err != nil {
		if errors.Is(err, store.ErrPlaybookNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "playbook not found"})
			return
		}
		log.Printf("Failed to load playbook %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load playbook"})
		return
	}
	c.JSON(http.StatusOK, record)
}

// ApplyPlaybook creates or updates a playbook definition.
func (h *Handler) ApplyPlaybook(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read payload"})
		return
	}
	payload, err := decodePlaybookPayload(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var spec playbookSpec
	if err := json.Unmarshal(payload.Spec, &spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playbook spec"})
		return
	}
	if err := validatePlaybookSpec(spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pb := &store.Playbook{
		Name:        name,
		Description: payload.Description,
		Tags:        payload.Tags,
		Spec:        payload.Spec,
	}
	record, err := h.store.UpsertPlaybook(pb)
	if err != nil {
		log.Printf("Failed to save playbook %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save playbook"})
		return
	}
	h.recordHistory("playbook_saved", name, map[string]interface{}{"tags": len(payload.Tags)})
	c.JSON(http.StatusOK, record)
}

// DeletePlaybook removes a stored playbook.
func (h *Handler) DeletePlaybook(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if err := h.store.DeletePlaybook(name); err != nil {
		if errors.Is(err, store.ErrPlaybookNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "playbook not found"})
			return
		}
		log.Printf("Failed to delete playbook %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete playbook"})
		return
	}
	h.recordHistory("playbook_deleted", name, nil)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// RunPlaybook executes the configured steps (install/activate) in order.
func (h *Handler) RunPlaybook(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	record, err := h.store.GetPlaybook(name)
	if err != nil {
		if errors.Is(err, store.ErrPlaybookNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "playbook not found"})
			return
		}
		log.Printf("Failed to load playbook %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load playbook"})
		return
	}

	var spec playbookSpec
	if err := json.Unmarshal(record.Spec, &spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playbook spec"})
		return
	}
	if err := validatePlaybookSpec(spec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	steps := gin.H{}
	status := http.StatusOK
	var installResult *installScheduleResult

	if spec.Install != nil {
		req := installWeightsRequest{
			HFModelID: spec.Install.HFModelID,
			Revision:  spec.Install.Revision,
			Target:    spec.Install.Target,
			Files:     spec.Install.Files,
			Overwrite: spec.Install.Overwrite,
		}
		installResult, err = h.scheduleWeightInstall(c.Request.Context(), req)
		if err != nil {
			var reqErr *requestError
			if errors.As(err, &reqErr) {
				c.JSON(reqErr.code, gin.H{"error": reqErr.message})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			}
			return
		}
		step := gin.H{
			"target":             installResult.Target,
			"storageUri":         installResult.StorageURI,
			"inferenceModelPath": installResult.InferencePath,
		}
		if installResult.Async {
			step["job"] = installResult.Job
			status = http.StatusAccepted
		} else {
			step["weights"] = installResult.Weight
		}
		steps["install"] = step
	}

	if spec.Activate != nil {
		modelID := spec.Activate.ModelID
		if modelID == "" && record != nil {
			modelID = record.Name
		}
		if modelID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "activate.modelId is required"})
			return
		}
		waitForInstall := spec.Activate.WaitForInstall
		if installResult == nil {
			waitForInstall = false
		} else if !installResult.Async {
			waitForInstall = false
		}
		strategy := strings.TrimSpace(spec.Activate.Strategy)
		if strategy == "" {
			strategy = "direct"
		}
		step := gin.H{
			"modelId":  modelID,
			"strategy": strategy,
		}
		if waitForInstall {
			status = http.StatusAccepted
			step["status"] = "pending_install"
			steps["activate"] = step
		} else {
			model, result, actErr := h.activateModelInternal(c.GetString("subject"), modelID)
			if actErr != nil {
				h.respondActivationError(c, actErr)
				return
			}
			step["status"] = "completed"
			step["model"] = model
			step["inferenceservice"] = result
			steps["activate"] = step
		}
	}

	if len(steps) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "playbook has no executable steps"})
		return
	}

	h.recordHistory("playbook_run", name, map[string]interface{}{
		"steps": len(steps),
	})
	h.publishEvent("playbook.run", gin.H{
		"name":  name,
		"steps": steps,
	})

	response := gin.H{
		"status":   "accepted",
		"playbook": record,
		"steps":    steps,
	}
	if status == http.StatusOK {
		response["status"] = "completed"
	}
	c.JSON(status, response)
}

// ListNotifications returns stored notification channels.
func (h *Handler) ListNotifications(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	channels, err := h.store.ListNotifications()
	if err != nil {
		log.Printf("Failed to list notifications: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list notifications"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"notifications": channels})
}

// ApplyNotification creates or updates a channel.
func (h *Handler) ApplyNotification(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var req notificationConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	record := &store.Notification{
		Name:     name,
		Type:     req.Type,
		Target:   req.Target,
		Metadata: req.Metadata,
	}
	if err := h.store.UpsertNotification(record); err != nil {
		log.Printf("Failed to upsert notification %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save notification"})
		return
	}
	h.recordHistory("notification_upserted", "", map[string]interface{}{"name": name, "type": req.Type})
	c.JSON(http.StatusOK, record)
}

// DeleteNotification removes a channel.
func (h *Handler) DeleteNotification(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if err := h.store.DeleteNotification(name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "notification not found"})
			return
		}
		log.Printf("Failed to delete notification %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete notification"})
		return
	}
	h.recordHistory("notification_deleted", "", map[string]interface{}{"name": name})
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *Handler) ensureSecretManager(c *gin.Context) bool {
	if h.secrets == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "secret management is disabled"})
		return false
	}
	return true
}

func postSlackMessage(webhook, message string) error {
	if webhook == "" {
		return fmt.Errorf("webhook empty")
	}
	payload := map[string]string{"text": message}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %s", resp.Status)
	}
	return nil
}

// ListTokens returns issued API tokens (metadata only).
func (h *Handler) ListTokens(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	tokens, err := h.store.ListAPITokens()
	if err != nil {
		log.Printf("Failed to list API tokens: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tokens"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tokens": tokens})
}

// IssueToken creates a new API token and returns the plaintext value once.
func (h *Handler) IssueToken(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	var req issueTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	validScopes := normalizeScopes(req.Scopes)
	plain, hash, err := store.GenerateToken(32)
	if err != nil {
		log.Printf("Failed to generate token: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}
	record := &store.APIToken{
		ID:        uuid.NewString(),
		Name:      req.Name,
		Scopes:    validScopes,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.CreateAPIToken(record); err != nil {
		log.Printf("Failed to store API token: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store token"})
		return
	}
	h.recordHistory("api_token_issued", "", map[string]interface{}{"id": record.ID, "name": record.Name})
	c.JSON(http.StatusCreated, gin.H{
		"token":     plain,
		"tokenId":   record.ID,
		"name":      record.Name,
		"scopes":    record.Scopes,
		"createdAt": record.CreatedAt,
	})
}

// DeleteToken revokes an API token by ID.
func (h *Handler) DeleteToken(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}
	if err := h.store.DeleteAPIToken(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
			return
		}
		log.Printf("Failed to delete token %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete token"})
		return
	}
	h.recordHistory("api_token_revoked", "", map[string]interface{}{"id": id})
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func normalizeScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func parseSince(value string) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return time.Now().Add(-d), nil
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, nil
	}
	return time.Time{}, fmt.Errorf("invalid since value")
}

// ListPolicies returns stored policy documents.
func (h *Handler) ListPolicies(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	policies, err := h.store.ListPolicies()
	if err != nil {
		log.Printf("Failed to list policies: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list policies"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"policies": policies})
}

// ApplyPolicy creates or updates a policy document.
func (h *Handler) ApplyPolicy(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	var req policyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	policy := &store.Policy{
		Name:      name,
		Document:  req.Document,
		UpdatedAt: time.Now().UTC(),
	}
	if err := h.store.UpsertPolicy(policy); err != nil {
		log.Printf("Failed to upsert policy %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save policy"})
		return
	}
	h.recordHistory("policy_applied", "", map[string]interface{}{"name": name})
	c.JSON(http.StatusOK, policy)
}

// DeletePolicy removes a policy by name.
func (h *Handler) DeletePolicy(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if err := h.store.DeletePolicy(name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "policy not found"})
			return
		}
		log.Printf("Failed to delete policy %s: %v", name, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete policy"})
		return
	}
	h.recordHistory("policy_deleted", "", map[string]interface{}{"name": name})
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// ListBackups returns recorded backups.
func (h *Handler) ListBackups(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	limit := parseLimit(c, "limit", 50, 200)
	backups, err := h.store.ListBackups(limit)
	if err != nil {
		log.Printf("Failed to list backups: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list backups"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"backups": backups})
}

// RecordBackup records metadata for a backup run.
func (h *Handler) RecordBackup(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	var req backupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rec := &store.Backup{
		ID:        uuid.NewString(),
		Type:      req.Type,
		Location:  req.Location,
		Notes:     req.Notes,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.store.RecordBackup(rec); err != nil {
		log.Printf("Failed to record backup: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record backup"})
		return
	}
	h.recordHistory("backup_recorded", "", map[string]interface{}{"type": req.Type, "location": req.Location})
	c.JSON(http.StatusCreated, rec)
}

// CleanupWeights removes the provided cached weight directories.
func (h *Handler) CleanupWeights(c *gin.Context) {
	if h.weights == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "weight management is disabled"})
		return
	}
	var req cleanupWeightsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Names) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "names is required"})
		return
	}
	results := make(map[string]string)
	for _, name := range req.Names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := h.weights.Delete(name); err != nil {
			results[name] = err.Error()
		} else {
			results[name] = "deleted"
			h.recordHistory("weight_deleted", name, nil)
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

type notificationRequest struct {
	Message string `json:"message"`
}
type notificationConfigRequest struct {
	Type     string            `json:"type" binding:"required"`
	Target   string            `json:"target" binding:"required"`
	Metadata map[string]string `json:"metadata"`
}

type issueTokenRequest struct {
	Name   string   `json:"name" binding:"required"`
	Scopes []string `json:"scopes"`
}

type policyRequest struct {
	Document string `json:"document" binding:"required"`
}

type backupRequest struct {
	Type     string `json:"type" binding:"required"`
	Location string `json:"location" binding:"required"`
	Notes    string `json:"notes"`
}

type cleanupWeightsRequest struct {
	Names []string `json:"names" binding:"required"`
}

// TestNotification sends a one-off notification via the configured channel.
func (h *Handler) TestNotification(c *gin.Context) {
	if h.opts.SlackWebhookURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "notification channel not configured"})
		return
	}
	var req notificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = fmt.Sprintf("Model Manager notification triggered at %s", time.Now().UTC().Format(time.RFC3339))
	}
	if err := postSlackMessage(h.opts.SlackWebhookURL, message); err != nil {
		log.Printf("Failed to send notification: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to deliver notification"})
		return
	}
	h.recordHistory("notification_test", "", map[string]interface{}{"message": message})
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

// ListVLLMArchitectures lists vLLM supported architectures.
func (h *Handler) ListVLLMArchitectures(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}

	architectures, err := h.vllm.ListSupportedArchitectures()
	if err != nil {
		log.Printf("Failed to list vLLM architectures: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list vLLM architectures"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"architectures": architectures})
}

// GetVLLMArchitecture returns details for one architecture.
func (h *Handler) GetVLLMArchitecture(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}
	name := c.Param("architecture")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "architecture is required"})
		return
	}
	detail, err := h.vllm.GetArchitectureDetail(name)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, detail)
}

// DiscoverModel generates a catalog entry for a HuggingFace model.
func (h *Handler) DiscoverModel(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}

	var req vllm.GenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	model, err := h.vllm.GenerateModelConfig(req)
	if err != nil {
		log.Printf("Failed to generate model config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, model)
}

// DescribeVLLMModel returns Hugging Face metadata plus compatibility info.
func (h *Handler) DescribeVLLMModel(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}

	var req modelInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	info, err := h.vllm.DescribeModel(req.HFModelID, req.AutoDetect)
	if err != nil {
		log.Printf("Failed to describe model %s: %v", req.HFModelID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := gin.H{"insight": info}

	if h.advisor != nil && info.SuggestedCatalog != nil {
		profiles := h.advisor.Profiles()
		recs := make([]recommendations.Recommendation, 0, len(profiles))
		compat := make([]recommendations.CompatibilityReport, 0, len(profiles))
		for _, profile := range profiles {
			recs = append(recs, h.advisor.RecommendForModel(info.SuggestedCatalog, profile.Name))
			compat = append(compat, h.advisor.Compatibility(info.SuggestedCatalog, profile.Name))
		}
		response["recommendations"] = recs
		response["compatibility"] = compat
	}

	c.JSON(http.StatusOK, response)
}

// GetHuggingFaceModel exposes metadata via REST-friendly GET.
func (h *Handler) GetHuggingFaceModel(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}
	id := strings.TrimPrefix(c.Param("id"), "/")
	autoDetect := c.Query("autoDetect") == "true"

	info, err := h.vllm.DescribeModel(id, autoDetect)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"insight": info})
}

// SearchHuggingFace proxies HF search for discoverability.
func (h *Handler) SearchHuggingFace(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}

	query := c.Query("q")
	limit := parseLimit(c, "limit", 10, 25)
	opts := vllm.SearchOptions{
		Query:          query,
		Limit:          limit,
		PipelineTag:    c.Query("pipelineTag"),
		Author:         c.Query("author"),
		License:        c.Query("license"),
		Sort:           c.Query("sort"),
		Direction:      c.Query("direction"),
		OnlyCompatible: parseBool(c, "compatibleOnly"),
		Tags:           parseTags(c),
	}

	if opts.OnlyCompatible || h.hfCache == nil {
		h.searchHuggingFaceLive(c, opts)
		return
	}

	if models, err := h.hfCache.List(c.Request.Context()); err == nil && len(models) > 0 {
		results := filterCachedHFModels(models, opts)
		c.JSON(http.StatusOK, gin.H{"results": results})
		return
	}

	h.searchHuggingFaceLive(c, opts)
}

func (h *Handler) searchHuggingFaceLive(c *gin.Context, opts vllm.SearchOptions) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}
	results, err := h.vllm.SearchModels(opts)
	if err != nil {
		log.Printf("Failed to search HuggingFace: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// GenerateCatalogEntry produces a draft catalog model with optional overrides.
func (h *Handler) GenerateCatalogEntry(c *gin.Context) {
	if h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "vLLM discovery is disabled"})
		return
	}

	var req generateCatalogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	model, err := h.vllm.GenerateModelConfig(vllm.GenerateRequest{
		HFModelID:   req.HFModelID,
		DisplayName: req.DisplayName,
		AutoDetect:  req.AutoDetect,
	})
	if err != nil {
		log.Printf("Failed to generate model config: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if req.StorageURI != "" {
		model.StorageURI = req.StorageURI
	}
	if req.Resources != nil {
		model.Resources = req.Resources
	}
	if req.NodeSelector != nil {
		model.NodeSelector = req.NodeSelector
	}
	if req.Tolerations != nil {
		model.Tolerations = req.Tolerations
	}
	if req.Env != nil {
		model.Env = req.Env
	}

	response := gin.H{"model": model}
	if h.checker != nil {
		result := h.checker.Validate(c.Request.Context(), nil, model)
		response["validation"] = result
		if !result.Valid {
			response["status"] = "warning"
		}
	}

	c.JSON(http.StatusOK, response)
}

// CreateCatalogPR saves a catalog entry, commits it, and optionally opens a PR.
func (h *Handler) CreateCatalogPR(c *gin.Context) {
	if h.writer == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "catalog contribution automation is disabled"})
		return
	}

	var req catalogPRRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Model.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model.id is required"})
		return
	}

	model := req.Model
	var validation interface{}
	if req.Validate && h.checker != nil {
		result := h.checker.Validate(c.Request.Context(), nil, &model)
		validation = result
		if !result.Valid {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":      "model validation failed",
				"validation": result,
			})
			return
		}
	}

	saveResult, err := h.writer.Save(&model)
	if err != nil {
		log.Printf("Failed to save catalog entry: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	branch := req.Branch
	if branch == "" {
		branch = fmt.Sprintf("model/%s", model.ID)
	}

	title := req.Title
	if title == "" {
		title = fmt.Sprintf("Add model %s", modelDisplayName(&model))
	}

	body := req.Body
	if body == "" {
		body = fmt.Sprintf("Automated catalog entry for `%s`.", modelDisplayName(&model))
	}

	if err := h.writer.CommitAndPush(c.Request.Context(), branch, req.Base, title, saveResult.RelativePath); err != nil {
		log.Printf("Failed to commit/push catalog change: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response := gin.H{
		"status": "success",
		"branch": branch,
		"file":   saveResult.RelativePath,
	}
	if validation != nil {
		response["validation"] = validation
	}

	if h.opts.GitHubToken == "" {
		response["message"] = "changes committed locally; set GITHUB_TOKEN to enable automatic PR creation"
		c.JSON(http.StatusOK, response)
		return
	}

	pr, err := h.writer.CreatePullRequest(c.Request.Context(), catalogwriter.PullRequestOptions{
		Branch: branch,
		Base:   req.Base,
		Title:  title,
		Body:   body,
		Draft:  req.Draft,
		Token:  h.opts.GitHubToken,
	})
	if err != nil {
		log.Printf("Failed to open pull request: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	response["pullRequest"] = pr

	c.JSON(http.StatusOK, response)
}

// GetModelManifest renders the KServe manifest for an existing catalog entry.
func (h *Handler) GetModelManifest(c *gin.Context) {
	if err := h.ensureCatalogFresh(false); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model catalog"})
		return
	}

	modelID := c.Param("id")
	model := h.catalog.Get(modelID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}

	manifest := h.kserve.RenderManifest(model)
	c.JSON(http.StatusOK, gin.H{"manifest": manifest, "model": model})
}

// PreviewCatalog validates an ad-hoc catalog entry and returns the manifest.
func (h *Handler) PreviewCatalog(c *gin.Context) {
	var model catalog.Model
	if err := c.ShouldBindJSON(&model); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result := gin.H{"model": model}
	if h.checker != nil {
		validation := h.checker.Validate(c.Request.Context(), nil, &model)
		result["validation"] = validation
		if !validation.Valid {
			result["status"] = "warning"
		}
	}

	result["manifest"] = h.kserve.RenderManifest(&model)

	c.JSON(http.StatusOK, result)
}

// ListJobs returns recent asynchronous jobs.
func (h *Handler) ListJobs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	limit := parseLimit(c, "limit", h.opts.HistoryLimit, 200)
	jobs, err := h.store.ListJobs(limit)
	if err != nil {
		log.Printf("Failed to list jobs: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	jobs = filterJobs(jobs, c.Query("status"), c.Query("type"), c.Query("modelId"))
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// GetJob returns a single job status.
func (h *Handler) GetJob(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	job, err := h.store.GetJob(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, job)
}

// CancelJob marks a pending/running job as cancelled.
func (h *Handler) CancelJob(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	job, err := h.store.GetJob(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if job.Status != store.JobPending && job.Status != store.JobRunning {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job is not cancellable"})
		return
	}
	now := time.Now().UTC()
	job.Status = store.JobCancelled
	job.Stage = "cancelled"
	job.Message = "Cancelled by operator"
	job.Error = "cancelled"
	job.CancelledAt = &now
	entry := store.JobLogEntry{
		Timestamp: now,
		Level:     "warn",
		Stage:     "cancelled",
		Message:   "Job cancelled via API",
	}
	job.Logs = append(job.Logs, entry)
	if err := h.store.UpdateJob(job); err != nil {
		log.Printf("Failed to cancel job %s: %v", job.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.publishJobEvent(c.Request.Context(), job)
	h.publishJobLog(c.Request.Context(), job.ID, entry)
	c.JSON(http.StatusOK, gin.H{"status": "cancelled", "job": job})
}

// RetryJob enqueues a failed/cancelled job again.
func (h *Handler) RetryJob(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	job, err := h.store.GetJob(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if job.Status != store.JobFailed && job.Status != store.JobCancelled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "job is not retryable"})
		return
	}
	if job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
		c.JSON(http.StatusBadRequest, gin.H{"error": "max attempts reached"})
		return
	}
	req, err := installRequestFromPayload(job.Payload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	job.Status = store.JobPending
	job.Stage = "queued"
	job.Progress = 0
	job.Message = "Retry requested"
	job.Error = ""
	job.CancelledAt = nil
	entry := store.JobLogEntry{
		Timestamp: time.Now().UTC(),
		Level:     "info",
		Stage:     "queued",
		Message:   fmt.Sprintf("Retry scheduled (%d/%d)", job.Attempt+1, job.MaxAttempts),
	}
	job.Logs = append(job.Logs, entry)
	if err := h.store.UpdateJob(job); err != nil {
		log.Printf("Failed to update job %s: %v", job.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.publishJobEvent(c.Request.Context(), job)
	h.publishJobLog(c.Request.Context(), job.ID, entry)
	queued := false
	if h.queue != nil {
		if err := h.queue.Enqueue(c.Request.Context(), job.ID, req); err == nil {
			queued = true
		} else {
			log.Printf("Failed to enqueue retry job %s: %v", job.ID, err)
		}
	}
	if !queued {
		if h.jobs == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "job queue unavailable"})
			return
		}
		h.jobs.ExecuteJob(job, req)
	}
	h.publishJobEvent(c.Request.Context(), job)
	c.JSON(http.StatusAccepted, gin.H{"status": "queued", "job": job})
}

// JobLogs returns the recorded job log entries.
func (h *Handler) JobLogs(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	job, err := h.store.GetJob(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": job.Logs})
}

// ListHistory returns historical deployment/install events.
func (h *Handler) ListHistory(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "persistent store not configured"})
		return
	}
	limit := parseLimit(c, "limit", h.opts.HistoryLimit, 200)
	entries, err := h.store.ListHistory(limit)
	if err != nil {
		log.Printf("Failed to list history: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	entries = filterHistory(entries, c.Query("event"), c.Query("modelId"))
	if sinceParam := strings.TrimSpace(c.Query("since")); sinceParam != "" {
		if since, err := parseSince(sinceParam); err == nil {
			filtered := entries[:0]
			for _, entry := range entries {
				if entry.CreatedAt.After(since) || entry.CreatedAt.Equal(since) {
					filtered = append(filtered, entry)
				}
			}
			entries = filtered
		}
	}
	c.JSON(http.StatusOK, gin.H{"events": entries})
}

// ListProfiles exposes GPU profiles for the frontend.
func (h *Handler) ListProfiles(c *gin.Context) {
	if h.advisor == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "recommendations disabled"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"profiles": h.advisor.Profiles()})
}

// ModelCompatibility reports whether a catalog entry fits on the requested GPU.
func (h *Handler) ModelCompatibility(c *gin.Context) {
	if h.advisor == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "compatibility service is disabled"})
		return
	}

	if err := h.ensureCatalogFresh(false); err != nil {
		log.Printf("Failed to ensure catalog freshness: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model catalog"})
		return
	}

	modelID := c.Param("id")
	model := h.catalog.Get(modelID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}

	gpuType := c.Query("gpuType")
	report := h.advisor.Compatibility(model, gpuType)
	c.JSON(http.StatusOK, report)
}

// GPURecommendations returns vLLM flag suggestions for a GPU type.
func (h *Handler) GPURecommendations(c *gin.Context) {
	if h.advisor == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "recommendations service is disabled"})
		return
	}

	gpuType := c.Param("gpuType")
	rec := h.advisor.Recommend(gpuType)
	c.JSON(http.StatusOK, rec)
}

func (h *Handler) ensureCatalogFresh(force bool) error {
	h.catalogMu.Lock()
	defer h.catalogMu.Unlock()

	refresh := force || h.lastCatalogRefresh.IsZero() || time.Since(h.lastCatalogRefresh) > h.opts.CatalogTTL || h.catalogStatus == "syncing"
	if !refresh {
		return nil
	}

	if err := h.catalog.Reload(); err != nil {
		if errors.Is(err, catalog.ErrModelsDirMissing) {
			log.Printf("Catalog directory not ready yet: %v", err)
			h.catalogStatus = "syncing"
			h.lastCatalogRefresh = time.Time{}
			if h.store != nil {
				if models, updatedAt, err := h.store.LoadCatalogSnapshot(); err == nil && len(models) > 0 {
					h.catalog.Restore(models)
					h.lastCatalogRefresh = updatedAt
					h.catalogCacheTime = updatedAt
					h.catalogStatus = "cache"
					log.Printf("Hydrated catalog from datastore snapshot updated at %s", updatedAt.Format(time.RFC3339))
				} else if err != nil {
					log.Printf("catalog snapshot unavailable: %v", err)
				}
			}
			return nil
		}
		return err
	}

	now := time.Now()
	h.lastCatalogRefresh = now
	h.catalogStatus = "live"
	h.catalogCacheTime = now

	if h.store != nil {
		if err := h.store.SaveCatalogSnapshot(h.catalog.All()); err != nil {
			log.Printf("Failed to persist catalog snapshot: %v", err)
		}
	}

	return nil
}

func (h *Handler) checkReadiness(ctx context.Context, url string, timeoutSeconds int) gin.H {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return gin.H{"status": "error", "message": err.Error()}
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return gin.H{"status": "error", "message": err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	status := "ok"
	if resp.StatusCode >= 400 {
		status = "fail"
	}

	return gin.H{
		"status":   status,
		"code":     resp.StatusCode,
		"duration": time.Since(start).String(),
		"url":      url,
		"preview":  string(body),
	}
}

func modelDisplayName(model *catalog.Model) string {
	if model == nil {
		return ""
	}
	if model.DisplayName != "" {
		return model.DisplayName
	}
	return model.ID
}

func (h *Handler) recordHistory(event, modelID string, meta map[string]interface{}) {
	if h.store == nil {
		return
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	entry := &store.HistoryEntry{
		Event:    event,
		ModelID:  modelID,
		Metadata: meta,
	}
	if err := h.store.AppendHistory(entry); err != nil {
		log.Printf("Failed to append history: %v", err)
	}
}

func (h *Handler) publishEvent(eventType string, payload interface{}) {
	if h.events == nil || eventType == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.events.Publish(ctx, events.Event{
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      payload,
	}); err != nil {
		log.Printf("Failed to publish event %s: %v", eventType, err)
	}
}

func parseLimit(c *gin.Context, key string, def, max int) int {
	if max <= 0 {
		max = 100
	}
	val := c.Query(key)
	if val == "" {
		return def
	}
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func parseBool(c *gin.Context, key string) bool {
	val := strings.TrimSpace(strings.ToLower(c.Query(key)))
	switch val {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseTags(c *gin.Context) []string {
	values := c.QueryArray("tag")
	if extra := c.Query("tags"); extra != "" {
		values = append(values, strings.Split(extra, ",")...)
	}
	seen := make(map[string]struct{})
	tags := make([]string, 0, len(values))
	for _, tag := range values {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		lower := strings.ToLower(tag)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func decodePlaybookPayload(body []byte) (*playbookApplyRequest, error) {
	data := bytes.TrimSpace(body)
	if len(data) == 0 {
		return nil, fmt.Errorf("payload is required")
	}
	if data[0] != '{' && data[0] != '[' {
		converted, err := yaml.YAMLToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		data = converted
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	var req playbookApplyRequest
	if raw, ok := wrapper["description"]; ok {
		_ = json.Unmarshal(raw, &req.Description)
	}
	if raw, ok := wrapper["tags"]; ok {
		_ = json.Unmarshal(raw, &req.Tags)
	}
	if raw, ok := wrapper["spec"]; ok && len(raw) > 0 {
		req.Spec = raw
	} else {
		// treat remaining fields as the spec blob
		filtered := make(map[string]json.RawMessage)
		for k, v := range wrapper {
			if k == "description" || k == "tags" {
				continue
			}
			filtered[k] = v
		}
		if len(filtered) == 0 {
			req.Spec = data
		} else {
			merged, err := json.Marshal(filtered)
			if err != nil {
				return nil, err
			}
			req.Spec = merged
		}
	}
	if len(req.Spec) == 0 {
		return nil, fmt.Errorf("spec field is required")
	}
	return &req, nil
}

func validatePlaybookSpec(spec playbookSpec) error {
	if spec.Install == nil && spec.Activate == nil {
		return fmt.Errorf("spec must include install and/or activate sections")
	}
	if spec.Install != nil {
		if spec.Install.HFModelID == "" {
			return fmt.Errorf("install.hfModelId is required")
		}
	}
	if spec.Activate != nil && spec.Activate.ModelID == "" {
		// allow empty if playbook name used later
	}
	return nil
}

func filterJobs(jobs []store.Job, status, jobType, modelID string) []store.Job {
	status = strings.TrimSpace(strings.ToLower(status))
	jobType = strings.TrimSpace(strings.ToLower(jobType))
	modelID = strings.TrimSpace(strings.ToLower(modelID))
	if status == "" && jobType == "" && modelID == "" {
		return jobs
	}
	result := make([]store.Job, 0, len(jobs))
	for _, job := range jobs {
		if status != "" && strings.ToLower(string(job.Status)) != status {
			continue
		}
		if jobType != "" && strings.ToLower(job.Type) != jobType {
			continue
		}
		if modelID != "" {
			payloadID, _ := job.Payload["hfModelId"].(string)
			if strings.ToLower(payloadID) != modelID {
				continue
			}
		}
		result = append(result, job)
	}
	return result
}

func (h *Handler) publishJobEvent(ctx context.Context, job *store.Job) {
	if h.events == nil || job == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timestamp := job.UpdatedAt
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	if err := h.events.Publish(ctx, events.Event{
		ID:        job.ID,
		Type:      fmt.Sprintf("job.%s", job.Status),
		Timestamp: timestamp,
		Data:      job,
	}); err != nil {
		log.Printf("Failed to publish job event: %v", err)
	}
}

func (h *Handler) publishJobLog(ctx context.Context, jobID string, entry store.JobLogEntry) {
	if h.events == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.events.Publish(ctx, events.Event{
		ID:        fmt.Sprintf("%s-log-%d", jobID, entry.Timestamp.UnixNano()),
		Type:      "job.log",
		Timestamp: entry.Timestamp,
		Data: gin.H{
			"jobId": jobID,
			"log":   entry,
		},
	}); err != nil {
		log.Printf("Failed to publish job log event: %v", err)
	}
}

func (h *Handler) appendJobLog(ctx context.Context, jobID string, entry store.JobLogEntry) {
	if h.store == nil {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if err := h.store.AppendJobLog(jobID, entry); err != nil {
		log.Printf("Failed to append log for job %s: %v", jobID, err)
		return
	}
	h.publishJobLog(ctx, jobID, entry)
}

func installRequestFromPayload(data map[string]interface{}) (jobs.InstallRequest, error) {
	if data == nil {
		return jobs.InstallRequest{}, fmt.Errorf("job payload missing")
	}
	modelID, _ := data["hfModelId"].(string)
	if modelID == "" {
		return jobs.InstallRequest{}, fmt.Errorf("payload missing hfModelId")
	}
	req := jobs.InstallRequest{
		ModelID: modelID,
	}
	if rev, ok := data["revision"].(string); ok {
		req.Revision = rev
	}
	if target, ok := data["target"].(string); ok {
		req.Target = target
	}
	if overwrite, ok := data["overwrite"].(bool); ok {
		req.Overwrite = overwrite
	}
	if rawFiles, ok := data["files"]; ok {
		switch v := rawFiles.(type) {
		case []interface{}:
			for _, entry := range v {
				if s, ok := entry.(string); ok {
					req.Files = append(req.Files, s)
				}
			}
		case []string:
			req.Files = append(req.Files, v...)
		}
	}
	return req, nil
}

func (h *Handler) collectAlerts(stats *weights.StorageStats) []gin.H {
	var alerts []gin.H
	if stats != nil && stats.TotalBytes > 0 && h.opts.PVCAlertThreshold > 0 {
		usage := float64(stats.UsedBytes) / float64(stats.TotalBytes)
		if usage >= h.opts.PVCAlertThreshold {
			alerts = append(alerts, gin.H{
				"level":   "warning",
				"kind":    "storage",
				"message": fmt.Sprintf("Weights PVC usage %.1f%% exceeds threshold", usage*100),
			})
		}
	}
	return alerts
}

func filterHistory(entries []store.HistoryEntry, event, modelID string) []store.HistoryEntry {
	event = strings.TrimSpace(strings.ToLower(event))
	modelID = strings.TrimSpace(strings.ToLower(modelID))
	if event == "" && modelID == "" {
		return entries
	}
	result := make([]store.HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if event != "" && strings.ToLower(entry.Event) != event {
			continue
		}
		if modelID != "" && strings.ToLower(entry.ModelID) != modelID {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func filterCachedHFModels(models []vllm.HuggingFaceModel, opts vllm.SearchOptions) []vllm.HuggingFaceModel {
	query := strings.ToLower(strings.TrimSpace(opts.Query))
	filtered := make([]vllm.HuggingFaceModel, 0, len(models))
	for _, model := range models {
		if query != "" {
			if !strings.Contains(strings.ToLower(model.ModelID), query) && !strings.Contains(strings.ToLower(model.ID), query) {
				continue
			}
		}
		if !hfOptionsMatch(&model, opts) {
			continue
		}
		filtered = append(filtered, model)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return compareHFModels(filtered[i], filtered[j], opts)
	})
	if opts.Limit > 0 && len(filtered) > opts.Limit {
		return filtered[:opts.Limit]
	}
	return filtered
}

func compareHFModels(a, b vllm.HuggingFaceModel, opts vllm.SearchOptions) bool {
	direction := strings.ToLower(opts.Direction)
	if direction == "" {
		direction = "desc"
	}
	lessInt := func(x, y int) bool {
		if direction == "asc" {
			return x < y
		}
		return x > y
	}

	switch strings.ToLower(opts.Sort) {
	case "likes":
		if a.Likes == b.Likes {
			return compareHFIdentifiers(a, b, direction)
		}
		return lessInt(a.Likes, b.Likes)
	case "downloads", "":
		if a.Downloads == b.Downloads {
			return compareHFIdentifiers(a, b, direction)
		}
		return lessInt(a.Downloads, b.Downloads)
	default:
		return compareHFIdentifiers(a, b, direction)
	}
}

func compareHFIdentifiers(a, b vllm.HuggingFaceModel, direction string) bool {
	left := strings.ToLower(hfIdentifier(a))
	right := strings.ToLower(hfIdentifier(b))
	if direction == "asc" {
		return left < right
	}
	return left > right
}

func hfIdentifier(model vllm.HuggingFaceModel) string {
	if strings.TrimSpace(model.ModelID) != "" {
		return model.ModelID
	}
	return model.ID
}

func hfOptionsMatch(model *vllm.HuggingFaceModel, opts vllm.SearchOptions) bool {
	if model == nil {
		return false
	}
	if opts.PipelineTag != "" && !strings.EqualFold(model.PipelineTag, opts.PipelineTag) {
		return false
	}
	if opts.Author != "" && !strings.EqualFold(model.Author, opts.Author) {
		return false
	}
	if opts.License != "" && !hfLicenseMatches(model, opts.License) {
		return false
	}
	if len(opts.Tags) > 0 && !hfHasAllTags(model.Tags, opts.Tags) {
		return false
	}
	return true
}

func hfHasAllTags(tags []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		set[strings.ToLower(tag)] = struct{}{}
	}
	for _, req := range required {
		if _, ok := set[strings.ToLower(req)]; !ok {
			return false
		}
	}
	return true
}

func hfLicenseMatches(model *vllm.HuggingFaceModel, license string) bool {
	if model == nil || license == "" {
		return true
	}
	target := strings.ToLower(license)
	if model.Config != nil {
		if value, ok := model.Config["license"].(string); ok && strings.EqualFold(value, license) {
			return true
		}
	}
	for _, tag := range model.Tags {
		if strings.HasPrefix(strings.ToLower(tag), "license:") {
			if strings.TrimPrefix(strings.ToLower(tag), "license:") == target {
				return true
			}
		}
	}
	return false
}

func isNilInterface(value interface{}) bool {
	val := reflect.ValueOf(value)
	switch val.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return val.IsNil()
	default:
		return false
	}
}

const docsHTML = `<!doctype html>
<html>
  <head>
    <meta charset="utf-8"/>
    <title>OL Model Manager API</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
    <style>body{margin:0;padding:0;}#swagger-ui{height:100vh;}</style>
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
      window.onload = () => {
        window.ui = SwaggerUIBundle({
          url: '/openapi',
          dom_id: '#swagger-ui',
          presets: [SwaggerUIBundle.presets.apis],
          layout: 'BaseLayout'
        });
      };
    </script>
  </body>
</html>`

var hfModelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (h *Handler) fetchAndValidateHFModel(id string) (*vllm.HuggingFaceModel, error) {
	if h.vllm == nil {
		return nil, fmt.Errorf("vLLM discovery client not configured")
	}
	if !hfModelIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid Hugging Face model id: %s", id)
	}

	model, err := h.vllm.GetHuggingFaceModel(id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch model from Hugging Face: %w", err)
	}

	return model, nil
}
