package hfcache

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/redis/go-redis/v9"
)

// Cache persists Hugging Face metadata in Redis + the datastore.
type Cache struct {
	store    *store.Store
	redis    redis.UniversalClient
	logger   *log.Logger
	ttl      time.Duration
	keySpace string
}

// Options configure the cache.
type Options struct {
	Store    *store.Store
	Redis    redis.UniversalClient
	Logger   *log.Logger
	TTL      time.Duration
	KeySpace string
}

// New creates a new cache manager.
func New(opts Options) *Cache {
	keySpace := opts.KeySpace
	if keySpace == "" {
		keySpace = "hf:models"
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.TTL <= 0 {
		opts.TTL = 30 * time.Minute
	}
	return &Cache{
		store:    opts.Store,
		redis:    opts.Redis,
		logger:   opts.Logger,
		ttl:      opts.TTL,
		keySpace: keySpace,
	}
}

func (c *Cache) listKey() string {
	return fmt.Sprintf("%s:all", c.keySpace)
}

func (c *Cache) modelKey(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", c.keySpace, id)
}

// Save persists the provided models.
func (c *Cache) Save(ctx context.Context, models []vllm.HuggingFaceModel) error {
	if len(models) == 0 {
		return nil
	}
	if c.store != nil {
		if err := c.store.ReplaceHFModels(models); err != nil {
			return fmt.Errorf("persist hf_models: %w", err)
		}
	}
	if c.redis != nil {
		payload, err := json.Marshal(models)
		if err != nil {
			return err
		}
		if err := c.redis.Set(ctx, c.listKey(), payload, c.ttl).Err(); err != nil {
			c.logger.Printf("hf cache: failed to prime redis list: %v", err)
		}
		for _, model := range models {
			key := c.modelKey(canonicalModelID(model))
			if key == "" {
				continue
			}
			item, err := json.Marshal(model)
			if err != nil {
				continue
			}
			if err := c.redis.Set(ctx, key, item, c.ttl).Err(); err != nil {
				c.logger.Printf("hf cache: failed to store %s: %v", key, err)
			}
		}
	}
	return nil
}

// List returns cached models, preferring Redis.
func (c *Cache) List(ctx context.Context) ([]vllm.HuggingFaceModel, error) {
	if c.redis != nil {
		data, err := c.redis.Get(ctx, c.listKey()).Bytes()
		if err == nil && len(data) > 0 {
			var models []vllm.HuggingFaceModel
			if err := json.Unmarshal(data, &models); err == nil {
				return models, nil
			}
		}
	}
	if c.store != nil {
		return c.store.ListHFModels()
	}
	return nil, fmt.Errorf("huggingface cache unavailable")
}

// Get retrieves a single model from cache.
func (c *Cache) Get(ctx context.Context, id string) (*vllm.HuggingFaceModel, error) {
	if id == "" {
		return nil, fmt.Errorf("model id required")
	}
	key := c.modelKey(id)
	if c.redis != nil && key != "" {
		data, err := c.redis.Get(ctx, key).Bytes()
		if err == nil && len(data) > 0 {
			var model vllm.HuggingFaceModel
			if err := json.Unmarshal(data, &model); err == nil {
				return &model, nil
			}
		}
	}
	if c.store != nil {
		return c.store.GetHFModel(id)
	}
	return nil, nil
}

func canonicalModelID(model vllm.HuggingFaceModel) string {
	if model.ModelID != "" {
		return model.ModelID
	}
	return model.ID
}
