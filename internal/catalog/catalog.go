// Package catalog manages model configurations from JSON files.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// ErrModelsDirMissing indicates the catalog models directory hasn't been synced yet.
var ErrModelsDirMissing = errors.New("catalog models directory missing")

// Catalog manages model configurations.
type Catalog struct {
	catalogRoot string
	modelsDir   string
	models      map[string]*Model
	mu          sync.RWMutex
}

// New creates a new Catalog instance.
func New(catalogRoot, modelsDir string) *Catalog {
	return &Catalog{
		catalogRoot: catalogRoot,
		modelsDir:   modelsDir,
		models:      make(map[string]*Model),
	}
}

// Load loads all model configurations from disk.
func (c *Catalog) Load() error {
	modelsPath := filepath.Join(c.catalogRoot, c.modelsDir)

	if _, err := os.Stat(modelsPath); os.IsNotExist(err) {
		log.Printf("Models directory does not exist: %s", modelsPath)
		return ErrModelsDirMissing
	}

	log.Printf("Loading models from: %s", modelsPath)

	files, err := filepath.Glob(filepath.Join(modelsPath, "*.json"))
	if err != nil {
		return fmt.Errorf("failed to glob model files: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, file := range files {
		if err := c.loadModelFile(file); err != nil {
			log.Printf("Failed to load model config %s: %v", file, err)
		}
	}

	return nil
}

func (c *Catalog) loadModelFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var model Model
	if err := json.Unmarshal(data, &model); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	if model.ID == "" {
		return fmt.Errorf("model config missing 'id' field")
	}

	c.models[model.ID] = &model
	log.Printf("Loaded model: %s", model.ID)

	return nil
}

// List returns a simplified list of all models.
func (c *Catalog) List() []ModelSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	models := make([]ModelSummary, 0, len(c.models))
	for _, model := range c.models {
		displayName := model.DisplayName
		if displayName == "" {
			displayName = model.ID
		}

		models = append(models, ModelSummary{
			ID:          model.ID,
			DisplayName: displayName,
			HFModelID:   model.HFModelID,
			Runtime:     model.Runtime,
		})
	}

	return models
}

// Get returns a specific model configuration by ID.
func (c *Catalog) Get(modelID string) *Model {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.models[modelID]
}

// Reload clears the current catalog and reloads from disk.
func (c *Catalog) Reload() error {
	c.mu.Lock()
	c.models = make(map[string]*Model)
	c.mu.Unlock()

	return c.Load()
}

// Count returns the number of loaded models.
func (c *Catalog) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.models)
}

// All returns a deep copy of every catalog model currently loaded.
func (c *Catalog) All() []*Model {
	c.mu.RLock()
	defer c.mu.RUnlock()

	models := make([]*Model, 0, len(c.models))
	for _, model := range c.models {
		if model == nil {
			continue
		}
		models = append(models, model)
	}
	return cloneModels(models)
}

// Restore replaces the in-memory catalog with the supplied models.
func (c *Catalog) Restore(models []*Model) {
	cloned := cloneModels(models)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.models = make(map[string]*Model, len(cloned))
	for _, model := range cloned {
		if model == nil || model.ID == "" {
			continue
		}
		c.models[model.ID] = model
	}
}

func cloneModels(models []*Model) []*Model {
	if len(models) == 0 {
		return nil
	}
	data, err := json.Marshal(models)
	if err != nil {
		log.Printf("failed to serialize catalog snapshot: %v", err)
		return nil
	}
	var copy []*Model
	if err := json.Unmarshal(data, &copy); err != nil {
		log.Printf("failed to deserialize catalog snapshot: %v", err)
		return nil
	}
	return copy
}
