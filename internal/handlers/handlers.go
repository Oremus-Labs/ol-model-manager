// Package handlers provides HTTP request handlers for the model manager API.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/jobs"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
	"github.com/oremus-labs/ol-model-manager/internal/openapi"
	"github.com/oremus-labs/ol-model-manager/internal/recommendations"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/validator"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
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

// Handler encapsulates dependencies for HTTP handlers.
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
	opts    Options

	catalogMu          sync.Mutex
	lastCatalogRefresh time.Time
	catalogStatus      string
	catalogCacheTime   time.Time
}

// New creates a new Handler instance.
func New(cat *catalog.Catalog, ks *kserve.Client, wm weightStore, vdisc discoveryService, val catalogValidator, writer catalogWriter, advisor recommendationService, dataStore *store.Store, jobMgr jobManager, evt eventBus, opts Options) *Handler {
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
		opts:               opts,
		lastCatalogRefresh: time.Time{},
		catalogStatus:      "unknown",
	}
}

type activateRequest struct {
	ID string `json:"id" binding:"required"`
}

type installWeightsRequest struct {
	HFModelID string   `json:"hfModelId" binding:"required"`
	Revision  string   `json:"revision,omitempty"`
	Target    string   `json:"target,omitempty"`
	Files     []string `json:"files,omitempty"`
	Overwrite bool     `json:"overwrite"`
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

	stream, unsubscribe, err := h.events.Subscribe(ctx)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to subscribe"})
		return
	}
	defer unsubscribe()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	// Prime the stream with the latest jobs so clients immediately see something meaningful.
	if h.store != nil {
		if jobs, err := h.store.ListJobs(5); err == nil {
			for _, job := range jobs {
				c.SSEvent(fmt.Sprintf("job.%s", job.Status), job)
			}
		}
	}

	c.Stream(func(w io.Writer) bool {
		select {
		case evt, ok := <-stream:
			if !ok {
				return false
			}
			c.SSEvent(evt.Type, evt)
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

	log.Printf("Activating model: %s", req.ID)

	if err := h.ensureCatalogFresh(true); err != nil {
		log.Printf("Failed to reload catalog: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload model catalog"})
		return
	}

	model := h.catalog.Get(req.ID)
	if model == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		return
	}

	result, err := h.kserve.Activate(model)
	if err != nil {
		log.Printf("Failed to activate model %s: %v", req.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Printf("Successfully activated model: %s", req.ID)
	c.JSON(http.StatusOK, gin.H{
		"status":           "success",
		"message":          "Model " + req.ID + " activated",
		"model":            model,
		"inferenceservice": result,
	})

	h.recordHistory("model_activated", req.ID, map[string]interface{}{
		"action": result.Action,
	})
}

// DeactivateModel deactivates the active model.
func (h *Handler) DeactivateModel(c *gin.Context) {
	log.Println("Deactivating active model")

	result, err := h.kserve.Deactivate()
	if err != nil {
		log.Printf("Failed to deactivate model: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Println("Successfully deactivated model")
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Active model deactivated",
		"result":  result,
	})

	h.recordHistory("model_deactivated", "", map[string]interface{}{
		"action": result.Action,
	})
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
	if h.weights == nil || h.vllm == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "weight installation is disabled"})
		return
	}

	var req installWeightsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	targetName, err := weights.CanonicalTarget(req.HFModelID, req.Target)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Target = targetName

	hfModel, err := h.fetchAndValidateHFModel(req.HFModelID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	files := req.Files
	if len(files) == 0 {
		files = vllm.CollectHuggingFaceFiles(hfModel)
	}

	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no downloadable files found for model"})
		return
	}

	storageURI := ""
	if h.opts.WeightsPVCName != "" {
		storageURI = fmt.Sprintf("pvc://%s/%s", h.opts.WeightsPVCName, targetName)
	}
	inferencePath := path.Join(h.opts.InferenceModelRoot, targetName)

	if h.jobs != nil {
		job, err := h.jobs.EnqueueWeightInstall(jobs.InstallRequest{
			ModelID:   req.HFModelID,
			Revision:  req.Revision,
			Target:    req.Target,
			Files:     files,
			Overwrite: req.Overwrite,
		})
		if err != nil {
			log.Printf("Failed to enqueue weight install: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"status":               "queued",
			"job":                  job,
			"jobUrl":               fmt.Sprintf("/jobs/%s", job.ID),
			"weightsInstallStatus": fmt.Sprintf("/weights/install/status/%s", job.ID),
			"target":               targetName,
			"storageUri":           storageURI,
			"inferenceModelPath":   inferencePath,
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), h.opts.WeightsInstallTimeout)
	defer cancel()

	info, err := h.weights.InstallFromHuggingFace(ctx, weights.InstallOptions{
		ModelID:   req.HFModelID,
		Revision:  req.Revision,
		Target:    req.Target,
		Files:     files,
		Token:     h.opts.HuggingFaceToken,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		log.Printf("Failed to install weights for %s: %v", req.HFModelID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	storageURI = ""
	if h.opts.WeightsPVCName != "" {
		storageURI = fmt.Sprintf("pvc://%s/%s", h.opts.WeightsPVCName, info.Name)
	}
	modelPath := path.Join(h.opts.InferenceModelRoot, info.Name)

	response := gin.H{
		"status":             "success",
		"model":              req.HFModelID,
		"weights":            info,
		"inferenceModelPath": modelPath,
	}
	if storageURI != "" {
		response["storageUri"] = storageURI
		response["catalogInstructions"] = fmt.Sprintf("Set storageUri to %s and keep MODEL_ID (or equivalent env) pointed at %s", storageURI, req.HFModelID)
	}

	h.recordHistory("weight_install_completed", req.HFModelID, map[string]interface{}{
		"target":      info.Name,
		"storageUri":  storageURI,
		"modelPath":   modelPath,
		"sizeBytes":   info.SizeBytes,
		"installedAt": info.InstalledAt,
	})

	c.JSON(http.StatusOK, response)
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
