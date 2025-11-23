package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// ModelCatalog manages model configurations
type ModelCatalog struct {
	CatalogRoot  string
	ModelsSubdir string
	Models       map[string]*ModelConfig
}

// ModelConfig represents a model configuration
type ModelConfig struct {
	ID             string                 `json:"id"`
	DisplayName    string                 `json:"displayName,omitempty"`
	HFModelID      string                 `json:"hfModelId,omitempty"`
	StorageURI     string                 `json:"storageUri,omitempty"`
	Runtime        string                 `json:"runtime,omitempty"`
	Env            []EnvVar               `json:"env,omitempty"`
	Storage        *Storage               `json:"storage,omitempty"`
	VLLM           *VLLMConfig            `json:"vllm,omitempty"`
	NodeSelector   map[string]string      `json:"nodeSelector,omitempty"`
	Tolerations    []Toleration           `json:"tolerations,omitempty"`
	Resources      *Resources             `json:"resources,omitempty"`
	VolumeMounts   []VolumeMount          `json:"volumeMounts,omitempty"`
	Volumes        []Volume               `json:"volumes,omitempty"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Storage struct {
	Key          string `json:"key,omitempty"`
	Path         string `json:"path,omitempty"`
	SchemaPath   string `json:"schemaPath,omitempty"`
	StorageKey   string `json:"storageKey,omitempty"`
}

type VLLMConfig struct {
	TensorParallelSize   *int     `json:"tensorParallelSize,omitempty"`
	Dtype                string   `json:"dtype,omitempty"`
	GPUMemoryUtilization *float64 `json:"gpuMemoryUtilization,omitempty"`
	MaxModelLen          *int     `json:"maxModelLen,omitempty"`
	TrustRemoteCode      *bool    `json:"trustRemoteCode,omitempty"`
}

type Toleration struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

type Resources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type Volume struct {
	Name string      `json:"name"`
	PVC  *PVCSource  `json:"persistentVolumeClaim,omitempty"`
}

type PVCSource struct {
	ClaimName string `json:"claimName"`
}

// ModelListItem is a simplified model for listing
type ModelListItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	HFModelID   string `json:"hfModelId,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
}

// NewModelCatalog creates a new ModelCatalog
func NewModelCatalog(catalogRoot, modelsSubdir string) *ModelCatalog {
	return &ModelCatalog{
		CatalogRoot:  catalogRoot,
		ModelsSubdir: modelsSubdir,
		Models:       make(map[string]*ModelConfig),
	}
}

// LoadCatalog loads all model configurations
func (mc *ModelCatalog) LoadCatalog() error {
	modelsPath := filepath.Join(mc.CatalogRoot, mc.ModelsSubdir)

	if _, err := os.Stat(modelsPath); os.IsNotExist(err) {
		log.Printf("Models directory does not exist: %s", modelsPath)
		return nil
	}

	log.Printf("Loading models from: %s", modelsPath)

	// Load all JSON files
	files, err := filepath.Glob(filepath.Join(modelsPath, "*.json"))
	if err != nil {
		return err
	}

	for _, file := range files {
		if err := mc.loadModelFile(file); err != nil {
			log.Printf("Failed to load model config %s: %v", file, err)
		}
	}

	return nil
}

func (mc *ModelCatalog) loadModelFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	var model ModelConfig
	if err := json.Unmarshal(data, &model); err != nil {
		return err
	}

	if model.ID == "" {
		log.Printf("Model config missing 'id' field: %s", filePath)
		return nil
	}

	mc.Models[model.ID] = &model
	log.Printf("Loaded model: %s", model.ID)

	return nil
}

// ListModels returns a list of all models
func (mc *ModelCatalog) ListModels() []ModelListItem {
	models := make([]ModelListItem, 0, len(mc.Models))

	for _, model := range mc.Models {
		displayName := model.DisplayName
		if displayName == "" {
			displayName = model.ID
		}

		models = append(models, ModelListItem{
			ID:          model.ID,
			DisplayName: displayName,
			HFModelID:   model.HFModelID,
			Runtime:     model.Runtime,
		})
	}

	return models
}

// GetModel returns a specific model configuration
func (mc *ModelCatalog) GetModel(modelID string) *ModelConfig {
	return mc.Models[modelID]
}

// ReloadCatalog reloads the catalog from disk
func (mc *ModelCatalog) ReloadCatalog() error {
	mc.Models = make(map[string]*ModelConfig)
	return mc.LoadCatalog()
}
