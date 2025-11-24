// Package vllm provides vLLM model discovery and configuration generation.
package vllm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
)

const (
	vllmModelsURL = "https://api.github.com/repos/vllm-project/vllm/contents/vllm/model_executor/models"
	hfAPIURL      = "https://huggingface.co/api/models"
)

// Discovery handles vLLM model discovery and auto-configuration.
type Discovery struct {
	client        *http.Client
	githubToken   string
	hfToken       string
	supportedMu   sync.RWMutex
	supportedArch map[string]ModelArchitecture
	supportedSync time.Time
	archCacheTTL  time.Duration

	hfCacheTTL   time.Duration
	hfMu         sync.RWMutex
	hfModels     map[string]hfModelCacheEntry
	insightMu    sync.RWMutex
	insightCache map[string]insightCacheEntry
	searchMu     sync.RWMutex
	searchCache  map[string]searchCacheEntry
}

// Option configures the discovery client.
type Option func(*Discovery)

// WithGitHubToken sets the GitHub token for API requests.
func WithGitHubToken(token string) Option {
	return func(d *Discovery) {
		d.githubToken = token
	}
}

// WithHuggingFaceToken sets the HuggingFace token for API requests.
func WithHuggingFaceToken(token string) Option {
	return func(d *Discovery) {
		d.hfToken = token
	}
}

// WithHuggingFaceCacheTTL sets the cache TTL for Hugging Face calls.
func WithHuggingFaceCacheTTL(ttl time.Duration) Option {
	return func(d *Discovery) {
		d.hfCacheTTL = ttl
	}
}

// WithVLLMCacheTTL sets the cache TTL for vLLM metadata.
func WithVLLMCacheTTL(ttl time.Duration) Option {
	return func(d *Discovery) {
		d.archCacheTTL = ttl
	}
}

// SearchOptions fine-tunes Hugging Face search behavior.
type SearchOptions struct {
	Query          string
	Limit          int
	PipelineTag    string
	Author         string
	License        string
	Tags           []string
	Sort           string
	Direction      string
	OnlyCompatible bool
}

// ModelArchitecture represents a vLLM-supported model architecture.
type ModelArchitecture struct {
	Name        string   `json:"name"`
	ClassName   string   `json:"className"`
	FilePath    string   `json:"filePath"`
	DownloadURL string   `json:"downloadUrl,omitempty"`
	SHA         string   `json:"sha,omitempty"`
	Size        int      `json:"size,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description,omitempty"`
}

// ArchitectureDetail includes file source content for UI previews.
type ArchitectureDetail struct {
	ModelArchitecture
	Source string `json:"source"`
}

// HuggingFaceModel represents a model from HuggingFace.
type HuggingFaceModel struct {
	ID          string                 `json:"id"`
	ModelID     string                 `json:"modelId"`
	Author      string                 `json:"author,omitempty"`
	SHA         string                 `json:"sha,omitempty"`
	Downloads   int                    `json:"downloads"`
	Likes       int                    `json:"likes"`
	Tags        []string               `json:"tags"`
	PipelineTag string                 `json:"pipeline_tag,omitempty"`
	Config      map[string]interface{} `json:"config,omitempty"`
	Siblings    []HFSibling            `json:"siblings,omitempty"`
}

// HFSibling represents a file in a HuggingFace model repo.
type HFSibling struct {
	RFileName string `json:"rfilename"`
}

// ModelInsight summarizes Hugging Face metadata + vLLM compatibility.
type ModelInsight struct {
	HFModel              *HuggingFaceModel `json:"huggingFace"`
	Compatible           bool              `json:"compatible"`
	MatchedArchitectures []string          `json:"matchedArchitectures,omitempty"`
	SuggestedCatalog     *catalog.Model    `json:"suggestedCatalog,omitempty"`
	RecommendedFiles     []string          `json:"recommendedFiles,omitempty"`
	Notes                []string          `json:"notes,omitempty"`
}

// GenerateRequest is a request to generate model configuration.
type GenerateRequest struct {
	HFModelID   string `json:"hfModelId" binding:"required"`
	DisplayName string `json:"displayName,omitempty"`
	AutoDetect  bool   `json:"autoDetect"`
}

// New creates a new vLLM discovery client.
func New(opts ...Option) *Discovery {
	d := &Discovery{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		supportedArch: make(map[string]ModelArchitecture),
		hfModels:      make(map[string]hfModelCacheEntry),
		insightCache:  make(map[string]insightCacheEntry),
		searchCache:   make(map[string]searchCacheEntry),
	}
	for _, opt := range opts {
		opt(d)
	}
	if d.hfCacheTTL <= 0 {
		d.hfCacheTTL = 5 * time.Minute
	}
	if d.archCacheTTL <= 0 {
		d.archCacheTTL = 10 * time.Minute
	}
	return d
}

// ListSupportedArchitectures returns all vLLM-supported model architectures.
func (d *Discovery) ListSupportedArchitectures() ([]ModelArchitecture, error) {
	if archs := d.cachedArchitectures(); archs != nil && !d.archCacheExpired() {
		return archs, nil
	}

	req, err := http.NewRequest("GET", vllmModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if d.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.githubToken)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch vLLM models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var files []struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Type        string `json:"type"`
		DownloadURL string `json:"download_url"`
		SHA         string `json:"sha"`
		Size        int    `json:"size"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	architectures := make([]ModelArchitecture, 0, len(files))
	cache := make(map[string]ModelArchitecture)

	for _, file := range files {
		if file.Type != "file" || !strings.HasSuffix(file.Name, ".py") {
			continue
		}

		if file.Name == "__init__.py" || strings.HasPrefix(file.Name, "_") {
			continue
		}

		name := strings.TrimSuffix(file.Name, ".py")
		arch := ModelArchitecture{
			Name:        name,
			ClassName:   toPascalCase(name),
			FilePath:    file.Path,
			DownloadURL: file.DownloadURL,
			SHA:         file.SHA,
			Size:        file.Size,
		}
		architectures = append(architectures, arch)
		cache[strings.ToLower(name)] = arch
	}

	d.supportedMu.Lock()
	d.supportedArch = cache
	d.supportedSync = time.Now()
	d.supportedMu.Unlock()

	return architectures, nil
}

// GetHuggingFaceModel fetches model information from HuggingFace.
func (d *Discovery) GetHuggingFaceModel(modelID string) (*HuggingFaceModel, error) {
	if cached := d.cachedHFModel(modelID); cached != nil {
		return cached, nil
	}

	url := fmt.Sprintf("%s/%s", hfAPIURL, modelID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if d.hfToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.hfToken)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch HuggingFace model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("model not found on HuggingFace: %s", modelID)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HuggingFace API returned status %d: %s", resp.StatusCode, string(body))
	}

	var model HuggingFaceModel
	if err := json.NewDecoder(resp.Body).Decode(&model); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	d.storeHFModel(modelID, &model)
	return cloneHuggingFaceModel(&model), nil
}

// GenerateModelConfig generates a model configuration from a HuggingFace model.
func (d *Discovery) GenerateModelConfig(req GenerateRequest) (*catalog.Model, error) {
	hfModel, err := d.GetHuggingFaceModel(req.HFModelID)
	if err != nil {
		return nil, err
	}
	return d.buildCatalogModel(hfModel, req), nil
}

// GetArchitectureDetail fetches and returns the source for an architecture file.
func (d *Discovery) GetArchitectureDetail(name string) (*ArchitectureDetail, error) {
	if name == "" {
		return nil, fmt.Errorf("architecture name is required")
	}
	arch, err := d.lookupArchitecture(name)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://api.github.com/repos/vllm-project/vllm/contents/%s", arch.FilePath)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if d.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.githubToken)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	source := payload.Content
	if strings.EqualFold(payload.Encoding, "base64") {
		if decoded, err := decodeBase64(payload.Content); err == nil {
			source = decoded
		}
	}

	return &ArchitectureDetail{
		ModelArchitecture: arch,
		Source:            source,
	}, nil
}

func (d *Discovery) buildCatalogModel(hfModel *HuggingFaceModel, req GenerateRequest) *catalog.Model {
	modelID := generateModelID(req.HFModelID)

	displayName := req.DisplayName
	if displayName == "" {
		displayName = generateDisplayName(req.HFModelID)
	}

	vllmConfig := &catalog.VLLMConfig{}
	if req.AutoDetect && hfModel.Config != nil {
		vllmConfig = d.detectVLLMSettings(hfModel)
	}

	model := &catalog.Model{
		ID:          modelID,
		DisplayName: displayName,
		HFModelID:   req.HFModelID,
		Runtime:     "vllm-runtime",
		VLLM:        vllmConfig,
	}

	model.Resources = &catalog.Resources{
		Requests: map[string]string{
			"amd.com/gpu": "1",
		},
		Limits: map[string]string{
			"amd.com/gpu": "1",
		},
	}

	return model
}

// detectVLLMSettings attempts to detect optimal vLLM settings from model config.
func (d *Discovery) detectVLLMSettings(hfModel *HuggingFaceModel) *catalog.VLLMConfig {
	config := &catalog.VLLMConfig{}

	// Check for trust_remote_code requirement
	if architectures, ok := hfModel.Config["architectures"].([]interface{}); ok {
		for _, arch := range architectures {
			archStr := fmt.Sprintf("%v", arch)
			if requiresTrustRemoteCode(archStr) {
				trustRemote := true
				config.TrustRemoteCode = &trustRemote
				break
			}
		}
	}

	// Detect dtype from model config
	if torchDtype, ok := hfModel.Config["torch_dtype"].(string); ok {
		config.Dtype = mapTorchDtype(torchDtype)
	}

	// Estimate max_model_len from config
	if maxPos, ok := hfModel.Config["max_position_embeddings"].(float64); ok {
		maxLen := int(maxPos)
		config.MaxModelLen = &maxLen
	}

	return config
}

// DescribeModel returns HuggingFace metadata plus vLLM compatibility info.
func (d *Discovery) DescribeModel(hfModelID string, autoDetect bool) (*ModelInsight, error) {
	cacheKey := describeCacheKey(hfModelID, autoDetect)
	if cached := d.cachedInsight(cacheKey); cached != nil {
		return cached, nil
	}

	hfModel, err := d.GetHuggingFaceModel(hfModelID)
	if err != nil {
		return nil, err
	}

	insight := &ModelInsight{
		HFModel:          hfModel,
		RecommendedFiles: CollectHuggingFaceFiles(hfModel),
	}

	supported, err := d.getSupportedArchitectures()
	if err != nil {
		insight.Notes = append(insight.Notes, fmt.Sprintf("failed to fetch vLLM supported architectures: %v", err))
	} else {
		matched := matchArchitectures(hfModel, supported)
		if len(matched) > 0 {
			insight.Compatible = true
			insight.MatchedArchitectures = matched
		} else {
			insight.Notes = append(insight.Notes, "no matching vLLM architecture detected")
		}
	}

	req := GenerateRequest{
		HFModelID:  hfModelID,
		AutoDetect: autoDetect,
	}
	insight.SuggestedCatalog = d.buildCatalogModel(hfModel, req)

	d.storeInsight(cacheKey, insight)
	return cloneInsight(insight), nil
}

// SearchModels queries Hugging Face for discoverable models.
func (d *Discovery) SearchModels(opts SearchOptions) ([]*ModelInsight, error) {
	opts = opts.normalize()
	if cached := d.cachedSearch(opts); cached != nil {
		return cached, nil
	}

	params := url.Values{}
	if opts.Query != "" {
		params.Set("search", opts.Query)
	} else {
		if opts.Sort == "" {
			params.Set("sort", "downloads")
		}
	}
	if opts.Sort != "" {
		params.Set("sort", opts.Sort)
	}
	if opts.Direction != "" {
		params.Set("direction", opts.Direction)
	}

	hfLimit := opts.Limit * 3
	if hfLimit < opts.Limit {
		hfLimit = opts.Limit
	}
	if hfLimit > 50 {
		hfLimit = 50
	}
	params.Set("limit", strconv.Itoa(hfLimit))

	reqURL := fmt.Sprintf("%s?%s", hfAPIURL, params.Encode())
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	if d.hfToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.hfToken)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HuggingFace search returned %d: %s", resp.StatusCode, string(body))
	}

	var models []HuggingFaceModel
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return nil, err
	}

	results := make([]*ModelInsight, 0, opts.Limit)
	for _, model := range models {
		if !opts.matches(&model) {
			continue
		}
		id := model.ModelID
		if id == "" {
			id = model.ID
		}
		if id == "" {
			continue
		}
		insight, err := d.DescribeModel(id, true)
		if err != nil {
			continue
		}
		if opts.OnlyCompatible && (insight == nil || !insight.Compatible) {
			continue
		}
		results = append(results, insight)
		if len(results) >= opts.Limit {
			break
		}
	}

	d.storeSearch(opts, results)
	return results, nil
}

func requiresTrustRemoteCode(architecture string) bool {
	// Architectures that typically require trust_remote_code
	requireTrust := []string{
		"Qwen", "ChatGLM", "InternLM", "Baichuan", "Yi",
	}

	for _, prefix := range requireTrust {
		if strings.Contains(architecture, prefix) {
			return true
		}
	}

	return false
}

func mapTorchDtype(torchDtype string) string {
	switch torchDtype {
	case "torch.float16", "float16":
		return "float16"
	case "torch.bfloat16", "bfloat16":
		return "bfloat16"
	case "torch.float32", "float32":
		return "float32"
	default:
		return "auto"
	}
}

func (d *Discovery) lookupArchitecture(name string) (ModelArchitecture, error) {
	if name == "" {
		return ModelArchitecture{}, fmt.Errorf("architecture name is required")
	}
	target := strings.ToLower(name)

	d.supportedMu.RLock()
	if arch, ok := d.supportedArch[target]; ok {
		d.supportedMu.RUnlock()
		return arch, nil
	}
	d.supportedMu.RUnlock()

	if _, err := d.ListSupportedArchitectures(); err != nil {
		return ModelArchitecture{}, err
	}

	d.supportedMu.RLock()
	defer d.supportedMu.RUnlock()
	if arch, ok := d.supportedArch[target]; ok {
		return arch, nil
	}
	return ModelArchitecture{}, fmt.Errorf("architecture not found: %s", name)
}

func generateModelID(hfModelID string) string {
	// Convert "Qwen/Qwen2.5-0.5B-Instruct" to "qwen2.5-0.5b-instruct"
	id := strings.ToLower(hfModelID)
	id = strings.ReplaceAll(id, "/", "-")
	id = strings.ReplaceAll(id, "_", "-")

	// Remove duplicate dashes
	re := regexp.MustCompile(`-+`)
	id = re.ReplaceAllString(id, "-")

	return strings.Trim(id, "-")
}

func generateDisplayName(hfModelID string) string {
	// Extract model name after /
	parts := strings.Split(hfModelID, "/")
	if len(parts) > 1 {
		return parts[1]
	}
	return hfModelID
}

func toPascalCase(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-'
	})

	for i, word := range words {
		words[i] = strings.Title(strings.ToLower(word))
	}

	return strings.Join(words, "")
}

func (d *Discovery) cachedArchitectures() []ModelArchitecture {
	d.supportedMu.RLock()
	defer d.supportedMu.RUnlock()
	if len(d.supportedArch) == 0 {
		return nil
	}
	archs := make([]ModelArchitecture, 0, len(d.supportedArch))
	for _, arch := range d.supportedArch {
		archs = append(archs, arch)
	}
	sort.Slice(archs, func(i, j int) bool {
		return archs[i].Name < archs[j].Name
	})
	return archs
}

func (d *Discovery) archCacheExpired() bool {
	if len(d.supportedArch) == 0 {
		return true
	}
	if d.archCacheTTL <= 0 {
		return false
	}
	return time.Since(d.supportedSync) > d.archCacheTTL
}

func (d *Discovery) getSupportedArchitectures() (map[string]ModelArchitecture, error) {
	d.supportedMu.RLock()
	if len(d.supportedArch) > 0 && !d.archCacheExpired() {
		defer d.supportedMu.RUnlock()
		out := make(map[string]ModelArchitecture, len(d.supportedArch))
		for k, v := range d.supportedArch {
			out[k] = v
		}
		return out, nil
	}
	d.supportedMu.RUnlock()

	if _, err := d.ListSupportedArchitectures(); err != nil {
		return nil, err
	}

	d.supportedMu.RLock()
	defer d.supportedMu.RUnlock()
	out := make(map[string]ModelArchitecture, len(d.supportedArch))
	for k, v := range d.supportedArch {
		out[k] = v
	}
	return out, nil
}

// CollectHuggingFaceFiles lists downloadable files for a model.
func CollectHuggingFaceFiles(model *HuggingFaceModel) []string {
	files := make([]string, 0, len(model.Siblings))
	seen := make(map[string]struct{})

	for _, sibling := range model.Siblings {
		name := sibling.RFileName
		if name == "" || name == "." {
			continue
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		files = append(files, name)
	}

	sort.Strings(files)
	return files
}

func matchArchitectures(model *HuggingFaceModel, supported map[string]ModelArchitecture) []string {
	architectures := extractArchitectures(model)
	if len(architectures) == 0 {
		return nil
	}

	found := make(map[string]struct{})
	for _, arch := range architectures {
		archLower := strings.ToLower(arch)
		for key, value := range supported {
			if strings.Contains(archLower, key) {
				found[value.Name] = struct{}{}
			}
		}
	}

	if len(found) == 0 {
		return nil
	}

	result := make([]string, 0, len(found))
	for arch := range found {
		result = append(result, arch)
	}
	sort.Strings(result)
	return result
}

func extractArchitectures(model *HuggingFaceModel) []string {
	if model == nil || model.Config == nil {
		return nil
	}
	raw, ok := model.Config["architectures"].([]interface{})
	if !ok {
		return nil
	}
	var result []string
	for _, item := range raw {
		if item == nil {
			continue
		}
		result = append(result, fmt.Sprintf("%v", item))
	}
	return result
}

type hfModelCacheEntry struct {
	model   *HuggingFaceModel
	expires time.Time
}

type insightCacheEntry struct {
	insight *ModelInsight
	expires time.Time
}

type searchCacheEntry struct {
	results []*ModelInsight
	expires time.Time
}

func (d *Discovery) cachedHFModel(id string) *HuggingFaceModel {
	if d.hfCacheTTL <= 0 {
		return nil
	}
	key := strings.ToLower(id)
	d.hfMu.RLock()
	entry, ok := d.hfModels[key]
	d.hfMu.RUnlock()
	if !ok || time.Now().After(entry.expires) || entry.model == nil {
		return nil
	}
	return cloneHuggingFaceModel(entry.model)
}

func (d *Discovery) storeHFModel(id string, model *HuggingFaceModel) {
	if d.hfCacheTTL <= 0 || model == nil {
		return
	}
	key := strings.ToLower(id)
	d.hfMu.Lock()
	d.hfModels[key] = hfModelCacheEntry{
		model:   cloneHuggingFaceModel(model),
		expires: time.Now().Add(d.hfCacheTTL),
	}
	d.hfMu.Unlock()
}

func describeCacheKey(id string, auto bool) string {
	return fmt.Sprintf("%s:%t", strings.ToLower(id), auto)
}

func (d *Discovery) cachedInsight(key string) *ModelInsight {
	if d.hfCacheTTL <= 0 {
		return nil
	}
	d.insightMu.RLock()
	entry, ok := d.insightCache[key]
	d.insightMu.RUnlock()
	if !ok || time.Now().After(entry.expires) || entry.insight == nil {
		return nil
	}
	return cloneInsight(entry.insight)
}

func (d *Discovery) storeInsight(key string, insight *ModelInsight) {
	if d.hfCacheTTL <= 0 || insight == nil {
		return
	}
	d.insightMu.Lock()
	d.insightCache[key] = insightCacheEntry{
		insight: cloneInsight(insight),
		expires: time.Now().Add(d.hfCacheTTL),
	}
	d.insightMu.Unlock()
}

func (d *Discovery) cachedSearch(opts SearchOptions) []*ModelInsight {
	if d.hfCacheTTL <= 0 {
		return nil
	}
	key := opts.cacheKey()
	d.searchMu.RLock()
	entry, ok := d.searchCache[key]
	d.searchMu.RUnlock()
	if !ok || time.Now().After(entry.expires) {
		return nil
	}
	return cloneInsightSlice(entry.results)
}

func (d *Discovery) storeSearch(opts SearchOptions, results []*ModelInsight) {
	if d.hfCacheTTL <= 0 {
		return
	}
	key := opts.cacheKey()
	d.searchMu.Lock()
	d.searchCache[key] = searchCacheEntry{
		results: cloneInsightSlice(results),
		expires: time.Now().Add(d.hfCacheTTL),
	}
	d.searchMu.Unlock()
}

func cloneHuggingFaceModel(model *HuggingFaceModel) *HuggingFaceModel {
	if model == nil {
		return nil
	}
	clone := *model
	if len(model.Tags) > 0 {
		clone.Tags = append([]string(nil), model.Tags...)
	}
	if len(model.Siblings) > 0 {
		clone.Siblings = append([]HFSibling(nil), model.Siblings...)
	}
	if model.Config != nil {
		clone.Config = make(map[string]interface{}, len(model.Config))
		for k, v := range model.Config {
			clone.Config[k] = v
		}
	}
	return &clone
}

func cloneInsight(insight *ModelInsight) *ModelInsight {
	if insight == nil {
		return nil
	}
	cloned := *insight
	cloned.HFModel = cloneHuggingFaceModel(insight.HFModel)
	if insight.SuggestedCatalog != nil {
		model := *insight.SuggestedCatalog
		if len(insight.SuggestedCatalog.Env) > 0 {
			model.Env = append([]catalog.EnvVar(nil), insight.SuggestedCatalog.Env...)
		}
		if len(insight.SuggestedCatalog.NodeSelector) > 0 {
			model.NodeSelector = make(map[string]string, len(insight.SuggestedCatalog.NodeSelector))
			for k, v := range insight.SuggestedCatalog.NodeSelector {
				model.NodeSelector[k] = v
			}
		}
		if len(insight.SuggestedCatalog.Tolerations) > 0 {
			model.Tolerations = append([]catalog.Toleration(nil), insight.SuggestedCatalog.Tolerations...)
		}
		cloned.SuggestedCatalog = &model
	}
	if len(insight.MatchedArchitectures) > 0 {
		cloned.MatchedArchitectures = append([]string(nil), insight.MatchedArchitectures...)
	}
	if len(insight.RecommendedFiles) > 0 {
		cloned.RecommendedFiles = append([]string(nil), insight.RecommendedFiles...)
	}
	if len(insight.Notes) > 0 {
		cloned.Notes = append([]string(nil), insight.Notes...)
	}
	return &cloned
}

func cloneInsightSlice(items []*ModelInsight) []*ModelInsight {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]*ModelInsight, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, cloneInsight(item))
	}
	return cloned
}

func (opts SearchOptions) normalize() SearchOptions {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.Limit > 25 {
		opts.Limit = 25
	}
	opts.Query = strings.TrimSpace(opts.Query)
	opts.PipelineTag = strings.TrimSpace(opts.PipelineTag)
	opts.Author = strings.TrimSpace(opts.Author)
	opts.License = strings.TrimSpace(opts.License)
	opts.Sort = strings.TrimSpace(opts.Sort)
	opts.Direction = strings.TrimSpace(opts.Direction)
	if opts.Tags != nil {
		tags := make([]string, 0, len(opts.Tags))
		for _, tag := range opts.Tags {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			tags = append(tags, strings.ToLower(tag))
		}
		opts.Tags = tags
	}
	return opts
}

func (opts SearchOptions) matches(model *HuggingFaceModel) bool {
	if opts.PipelineTag != "" && !strings.EqualFold(model.PipelineTag, opts.PipelineTag) {
		return false
	}
	if opts.Author != "" && !strings.EqualFold(model.Author, opts.Author) {
		return false
	}
	if opts.License != "" && !licenseMatches(model, opts.License) {
		return false
	}
	if len(opts.Tags) > 0 && !hasAllTags(model.Tags, opts.Tags) {
		return false
	}
	return true
}

func (opts SearchOptions) cacheKey() string {
	builder := strings.Builder{}
	builder.WriteString(strings.ToLower(opts.Query))
	builder.WriteString("|")
	builder.WriteString(strings.ToLower(opts.PipelineTag))
	builder.WriteString("|")
	builder.WriteString(strings.ToLower(opts.Author))
	builder.WriteString("|")
	builder.WriteString(strings.ToLower(opts.License))
	builder.WriteString("|")
	builder.WriteString(strings.Join(opts.Tags, ","))
	builder.WriteString("|")
	builder.WriteString(strings.ToLower(opts.Sort))
	builder.WriteString("|")
	builder.WriteString(strings.ToLower(opts.Direction))
	builder.WriteString("|")
	builder.WriteString(strconv.Itoa(opts.Limit))
	builder.WriteString("|")
	if opts.OnlyCompatible {
		builder.WriteString("1")
	} else {
		builder.WriteString("0")
	}
	return builder.String()
}

func hasAllTags(tags []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		set[strings.ToLower(tag)] = struct{}{}
	}
	for _, req := range required {
		if _, ok := set[req]; !ok {
			return false
		}
	}
	return true
}

func licenseMatches(model *HuggingFaceModel, license string) bool {
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

func decodeBase64(value string) (string, error) {
	clean := strings.ReplaceAll(value, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
