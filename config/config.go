// Package config provides application configuration management.
package config

import (
	"log"
	"os"
	"time"
)

// Config holds all application configuration.
type Config struct {
	// Server configuration
	ServerPort string

	// Model catalog configuration
	CatalogRoot            string
	CatalogModelsDir       string
	CatalogRefreshInterval time.Duration

	// KServe configuration
	Namespace            string
	InferenceServiceName string

	// Weights / storage configuration
	WeightsStoragePath    string
	WeightsInstallTimeout time.Duration
	WeightsPVCName        string

	// Inference runtime expectations
	InferenceModelRoot string

	// External tokens
	HuggingFaceToken string
	GitHubToken      string
}

// Load loads configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		ServerPort:             getEnv("SERVER_PORT", "8080"),
		CatalogRoot:            getEnv("MODEL_CATALOG_ROOT", "/workspace/catalog"),
		CatalogModelsDir:       getEnv("MODEL_CATALOG_MODELS_SUBDIR", "models"),
		CatalogRefreshInterval: getEnvDuration("CATALOG_REFRESH_INTERVAL", 30*time.Second),
		Namespace:              getEnv("ACTIVE_NAMESPACE", "ai"),
		InferenceServiceName:   getEnv("ACTIVE_INFERENCESERVICE_NAME", "active-llm"),
		WeightsStoragePath:     getEnv("WEIGHTS_STORAGE_PATH", "/mnt/model-storage"),
		WeightsInstallTimeout:  getEnvDuration("WEIGHTS_INSTALL_TIMEOUT", 30*time.Minute),
		WeightsPVCName:         getEnv("WEIGHTS_PVC_NAME", "venus-model-storage"),
		InferenceModelRoot:     getEnv("INFERENCE_MODEL_ROOT", "/mnt/models"),
		HuggingFaceToken:       os.Getenv("HUGGINGFACE_API_TOKEN"),
		GitHubToken:            os.Getenv("GITHUB_TOKEN"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
		log.Printf("Invalid duration for %s: %s, using default %s", key, value, defaultValue)
	}
	return defaultValue
}
