// Package config provides application configuration management.
package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	CatalogSchemaPath      string
	CatalogRepo            string
	CatalogBaseBranch      string

	// KServe configuration
	Namespace            string
	ValidationNamespace  string
	InferenceServiceName string

	// Weights / storage configuration
	WeightsStoragePath    string
	WeightsInstallTimeout time.Duration
	WeightsPVCName        string

	// Inference runtime expectations
	InferenceModelRoot string
	GPUProfilesPath    string
	StatePath          string

	// Persistence + cache configuration
	DataStoreDriver         string
	DataStoreDSN            string
	DatabasePVCName         string
	HuggingFaceCacheTTL     time.Duration
	HuggingFaceSyncInterval time.Duration
	VLLMCacheTTL            time.Duration
	RecommendationCacheTTL  time.Duration
	GPUInventorySource      string
	PVCAlertThreshold       float64

	// Redis / events configuration
	RedisAddr        string
	RedisUsername    string
	RedisPassword    string
	RedisDB          int
	RedisTLSEnabled  bool
	RedisTLSInsecure bool
	EventsChannel    string
	RedisJobStream   string
	RedisJobGroup    string

	// External tokens
	HuggingFaceToken string
	GitHubToken      string
	GitAuthorName    string
	GitAuthorEmail   string
	APIToken         string
	SlackWebhookURL  string
}

// Load loads configuration from environment variables with defaults.
func Load() *Config {
	namespace := getEnv("ACTIVE_NAMESPACE", "ai")
	statePath := getEnv("STATE_PATH", "/app/state")
	dataStoreDriver := getEnv("DATASTORE_DRIVER", "bolt")
	dataStoreDSN := getEnv("DATASTORE_DSN", "")
	if dataStoreDSN == "" {
		defaultFile := "state.db"
		if dataStoreDriver == "sqlite" {
			defaultFile = "model-manager.db"
		}
		dataStoreDSN = filepath.Join(statePath, defaultFile)
	}
	if dataStoreDriver == "postgres" && dataStoreDSN == "" {
		dataStoreDSN = os.Getenv("POSTGRES_DSN")
	}
	return &Config{
		ServerPort:              getEnv("SERVER_PORT", "8080"),
		CatalogRoot:             getEnv("MODEL_CATALOG_ROOT", "/workspace/catalog"),
		CatalogModelsDir:        getEnv("MODEL_CATALOG_MODELS_SUBDIR", "models"),
		CatalogSchemaPath:       getEnv("MODEL_CATALOG_SCHEMA_PATH", ""),
		CatalogRefreshInterval:  getEnvDuration("CATALOG_REFRESH_INTERVAL", 30*time.Second),
		CatalogRepo:             getEnv("CATALOG_REPO", ""),
		CatalogBaseBranch:       getEnv("CATALOG_BASE_BRANCH", "main"),
		Namespace:               namespace,
		ValidationNamespace:     getEnv("VALIDATION_NAMESPACE", namespace),
		InferenceServiceName:    getEnv("ACTIVE_INFERENCESERVICE_NAME", "active-llm"),
		WeightsStoragePath:      getEnv("WEIGHTS_STORAGE_PATH", "/mnt/models"),
		WeightsInstallTimeout:   getEnvDuration("WEIGHTS_INSTALL_TIMEOUT", 30*time.Minute),
		WeightsPVCName:          getEnv("WEIGHTS_PVC_NAME", "venus-model-storage"),
		InferenceModelRoot:      getEnv("INFERENCE_MODEL_ROOT", "/mnt/models"),
		GPUProfilesPath:         getEnv("GPU_PROFILE_PATH", "/app/config/gpu-profiles.json"),
		StatePath:               statePath,
		DataStoreDriver:         dataStoreDriver,
		DataStoreDSN:            dataStoreDSN,
		DatabasePVCName:         getEnv("DATABASE_PVC_NAME", "model-manager-db"),
		HuggingFaceCacheTTL:     getEnvDuration("HUGGINGFACE_CACHE_TTL", 5*time.Minute),
		HuggingFaceSyncInterval: getEnvDuration("HUGGINGFACE_SYNC_INTERVAL", 30*time.Minute),
		VLLMCacheTTL:            getEnvDuration("VLLM_CACHE_TTL", 10*time.Minute),
		RecommendationCacheTTL:  getEnvDuration("RECOMMENDATION_CACHE_TTL", 15*time.Minute),
		GPUInventorySource:      getEnv("GPU_INVENTORY_SOURCE", "k8s-nodes"),
		PVCAlertThreshold:       getEnvFloat("PVC_ALERT_THRESHOLD", 0.85),
		RedisAddr:               getEnv("REDIS_ADDR", ""),
		RedisUsername:           getEnv("REDIS_USERNAME", ""),
		RedisPassword:           os.Getenv("REDIS_PASSWORD"),
		RedisDB:                 getEnvInt("REDIS_DB", 0),
		RedisTLSEnabled:         getEnvBool("REDIS_TLS_ENABLED", false),
		RedisTLSInsecure:        getEnvBool("REDIS_TLS_INSECURE_SKIP_VERIFY", false),
		EventsChannel:           getEnv("EVENTS_CHANNEL", "model-manager-events"),
		RedisJobStream:          getEnv("REDIS_JOB_STREAM", "model-manager:jobs"),
		RedisJobGroup:           getEnv("REDIS_JOB_GROUP", "weights-workers"),
		HuggingFaceToken:        os.Getenv("HUGGINGFACE_API_TOKEN"),
		GitHubToken:             os.Getenv("GITHUB_TOKEN"),
		GitAuthorName:           getEnv("GIT_AUTHOR_NAME", ""),
		GitAuthorEmail:          getEnv("GIT_AUTHOR_EMAIL", ""),
		APIToken:                os.Getenv("MODEL_MANAGER_API_TOKEN"),
		SlackWebhookURL:         os.Getenv("SLACK_WEBHOOK_URL"),
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

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
		log.Printf("Invalid float for %s: %s, using default %f", key, value, defaultValue)
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
		log.Printf("Invalid int for %s: %s, using default %d", key, value, defaultValue)
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		switch strings.ToLower(value) {
		case "1", "true", "yes", "y":
			return true
		case "0", "false", "no", "n":
			return false
		default:
			log.Printf("Invalid bool for %s: %s, using default %t", key, value, defaultValue)
		}
	}
	return defaultValue
}
