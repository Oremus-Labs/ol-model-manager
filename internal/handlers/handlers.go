// Package handlers provides HTTP request handlers for the model manager API.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/kserve"
	"github.com/oremus-labs/ol-model-manager/internal/recommendations"
	"github.com/oremus-labs/ol-model-manager/internal/validator"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

// Options configures handler runtime behavior.
type Options struct {
	CatalogTTL            time.Duration
	WeightsInstallTimeout time.Duration
	HuggingFaceToken      string
	GitHubToken           string
	WeightsPVCName        string
	InferenceModelRoot    string
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
	GenerateModelConfig(vllm.GenerateRequest) (*catalog.Model, error)
	GetHuggingFaceModel(string) (*vllm.HuggingFaceModel, error)
	DescribeModel(string, bool) (*vllm.ModelInsight, error)
}

type catalogValidator interface {
	Validate(context.Context, []byte, *catalog.Model) validator.Result
}

type catalogWriter interface {
	Save(*catalog.Model) (*catalogwriter.SaveResult, error)
	CommitAndPush(context.Context, string, string, string, ...string) error
	CreatePullRequest(context.Context, catalogwriter.PullRequestOptions) (*catalogwriter.PullRequest, error)
}

type recommendationService interface {
	Compatibility(*catalog.Model, string) recommendations.CompatibilityReport
	Recommend(string) recommendations.Recommendation
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
	opts    Options

	catalogMu          sync.Mutex
	lastCatalogRefresh time.Time
}

// New creates a new Handler instance.
func New(cat *catalog.Catalog, ks *kserve.Client, wm weightStore, vdisc discoveryService, val catalogValidator, writer catalogWriter, advisor recommendationService, opts Options) *Handler {
	if opts.CatalogTTL <= 0 {
		opts.CatalogTTL = time.Minute
	}
	if opts.WeightsInstallTimeout <= 0 {
		opts.WeightsInstallTimeout = 30 * time.Minute
	}
	if opts.InferenceModelRoot == "" {
		opts.InferenceModelRoot = "/mnt/models"
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
		opts:               opts,
		lastCatalogRefresh: time.Now(),
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

type modelInfoRequest struct {
	HFModelID string `json:"hfModelId" binding:"required"`
	AutoDetect bool  `json:"autoDetect"`
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

// Health returns the health status of the service.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ListModels returns all available models.
func (h *Handler) ListModels(c *gin.Context) {
	if err := h.ensureCatalogFresh(false); err != nil {
		log.Printf("Failed to ensure catalog freshness: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load model catalog"})
		return
	}

	c.JSON(http.StatusOK, h.catalog.List())
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
		"models":  h.catalog.List(),
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

	name := c.Param("name")
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

	name := c.Param("name")
	if err := h.weights.Delete(name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Deleted weights for " + name,
	})
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

	storageURI := ""
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
		response["catalogInstructions"] = fmt.Sprintf("Set storageUri to %s and MODEL_ID (or equivalent env) to %s", storageURI, modelPath)
	}

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

	c.JSON(http.StatusOK, info)
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

	if force || h.lastCatalogRefresh.IsZero() || time.Since(h.lastCatalogRefresh) > h.opts.CatalogTTL {
		if err := h.catalog.Reload(); err != nil {
			return err
		}
		h.lastCatalogRefresh = time.Now()
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

func isNilInterface(value interface{}) bool {
	val := reflect.ValueOf(value)
	switch val.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
		return val.IsNil()
	default:
		return false
	}
}

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
