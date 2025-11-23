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
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// ListSupportedArchitectures returns all vLLM-supported model architectures.
func (d *Discovery) ListSupportedArchitectures() ([]ModelArchitecture, error) {
	if archs := d.cachedArchitectures(); archs != nil {
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
	d.supportedMu.Unlock()

	return architectures, nil
}

// GetHuggingFaceModel fetches model information from HuggingFace.
func (d *Discovery) GetHuggingFaceModel(modelID string) (*HuggingFaceModel, error) {
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

	return &model, nil
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
			"nvidia.com/gpu": "1",
		},
		Limits: map[string]string{
			"nvidia.com/gpu": "1",
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

	return insight, nil
}

// SearchModels queries Hugging Face for discoverable models.
func (d *Discovery) SearchModels(query string, limit int) ([]*ModelInsight, error) {
	if limit <= 0 || limit > 25 {
		limit = 10
	}

	params := url.Values{}
	if query != "" {
		params.Set("search", query)
	} else {
		params.Set("sort", "downloads")
	}
	params.Set("limit", strconv.Itoa(limit))

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

	results := make([]*ModelInsight, 0, len(models))
	for _, model := range models {
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
		results = append(results, insight)
		if len(results) >= limit {
			break
		}
	}

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

func (d *Discovery) getSupportedArchitectures() (map[string]ModelArchitecture, error) {
	d.supportedMu.RLock()
	if len(d.supportedArch) > 0 {
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

func decodeBase64(value string) (string, error) {
	clean := strings.ReplaceAll(value, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
