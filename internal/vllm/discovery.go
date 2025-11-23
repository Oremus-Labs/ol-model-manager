// Package vllm provides vLLM model discovery and configuration generation.
package vllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
)

const (
	vllmModelsURL  = "https://api.github.com/repos/vllm-project/vllm/contents/vllm/model_executor/models"
	vllmRawBaseURL = "https://raw.githubusercontent.com/vllm-project/vllm/main/vllm/model_executor/models"
	hfAPIURL       = "https://huggingface.co/api/models"
)

// Discovery handles vLLM model discovery and auto-configuration.
type Discovery struct {
	client      *http.Client
	githubToken string
	hfToken     string
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
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description,omitempty"`
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
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// ListSupportedArchitectures returns all vLLM-supported model architectures.
func (d *Discovery) ListSupportedArchitectures() ([]ModelArchitecture, error) {
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
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var architectures []ModelArchitecture

	for _, file := range files {
		if file.Type != "file" || !strings.HasSuffix(file.Name, ".py") {
			continue
		}

		// Skip __init__.py and utility files
		if file.Name == "__init__.py" || strings.HasPrefix(file.Name, "_") {
			continue
		}

		name := strings.TrimSuffix(file.Name, ".py")
		architectures = append(architectures, ModelArchitecture{
			Name:      name,
			ClassName: toPascalCase(name),
			FilePath:  file.Path,
		})
	}

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
	// Fetch HuggingFace model info
	hfModel, err := d.GetHuggingFaceModel(req.HFModelID)
	if err != nil {
		return nil, err
	}

	// Generate a clean ID
	modelID := generateModelID(req.HFModelID)

	// Use provided display name or generate from model ID
	displayName := req.DisplayName
	if displayName == "" {
		displayName = generateDisplayName(req.HFModelID)
	}

	// Detect architecture and optimal settings
	vllmConfig := &catalog.VLLMConfig{}

	// Auto-detect settings from model config if requested
	if req.AutoDetect && hfModel.Config != nil {
		vllmConfig = d.detectVLLMSettings(hfModel)
	}

	// Build model configuration
	model := &catalog.Model{
		ID:          modelID,
		DisplayName: displayName,
		HFModelID:   req.HFModelID,
		Runtime:     "vllm-runtime",
		VLLM:        vllmConfig,
	}

	// Add default resource requirements for production
	model.Resources = &catalog.Resources{
		Requests: map[string]string{
			"nvidia.com/gpu": "1",
		},
		Limits: map[string]string{
			"nvidia.com/gpu": "1",
		},
	}

	return model, nil
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
