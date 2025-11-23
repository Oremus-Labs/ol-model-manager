// Package config provides application configuration management.
package config

import "os"

// Config holds all application configuration.
type Config struct {
	// Server configuration
	ServerPort string

	// Model catalog configuration
	CatalogRoot       string
	CatalogModelsDir  string

	// KServe configuration
	Namespace              string
	InferenceServiceName   string
}

// Load loads configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		ServerPort:             getEnv("SERVER_PORT", "8080"),
		CatalogRoot:            getEnv("MODEL_CATALOG_ROOT", "/workspace/catalog"),
		CatalogModelsDir:       getEnv("MODEL_CATALOG_MODELS_SUBDIR", "models"),
		Namespace:              getEnv("ACTIVE_NAMESPACE", "ai"),
		InferenceServiceName:   getEnv("ACTIVE_INFERENCESERVICE_NAME", "active-llm"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
