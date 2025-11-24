# Model Manager

HTTP API service for dynamically managing KServe InferenceServices based on model catalog configurations.

> **Roadmap:** The long-term platform plan (Phases 0–7), schema decisions, and persistence plan
> now live in [`docs/`](docs). Start with [`docs/roadmap.md`](docs/roadmap.md) for sequencing
> and [`docs/persistence.md`](docs/persistence.md) for the database design that Phase 1
> will implement.

## Features

- List available models from git-synced catalog with automatic refresh caching
- Activate and deactivate KServe InferenceServices for catalog entries
- Inspect and manage cached HuggingFace weights on the Venus PVC
- Install new model weights directly from HuggingFace (with optional auth token) with async job tracking
- Generate draft catalog entries from HuggingFace metadata via vLLM discovery helpers and manifest previews
- Search the Hugging Face Hub for vLLM-compatible models and inspect metadata before installing
- Publish a live OpenAPI spec + Swagger UI so UI/automation teams can self-discover endpoints
- Query PVC usage statistics and supported vLLM architectures
- Persist deployment history + job status via an embedded SQLite datastore (backed by a PVC)
- Validate catalog entries against the shared schema, PVC/secret availability, and GPU capacity
- Dry-run KServe activations (and optional readiness probes) before flipping production traffic
- Estimate GPU compatibility + runtime recommendations per catalog entry, with GPU profile metadata exposed to the UI

## Environment Variables

- `MODEL_CATALOG_ROOT` - Root path to the catalog (default: `/workspace/catalog`)
- `MODEL_CATALOG_MODELS_SUBDIR` - Subdirectory containing model configs (default: `models`)
- `CATALOG_REFRESH_INTERVAL` - TTL before models are reloaded from disk (default: `30s`)
- `ACTIVE_NAMESPACE` - Kubernetes namespace for InferenceServices (default: `ai`)
- `ACTIVE_INFERENCESERVICE_NAME` - Name of the InferenceService to manage (default: `active-llm`)
- `WEIGHTS_STORAGE_PATH` - Root directory for cached weights on the PVC (default: `/mnt/models`)
- `WEIGHTS_INSTALL_TIMEOUT` - Timeout for weight installation operations (default: `30m`)
- `WEIGHTS_PVC_NAME` - Name of the PVC backing the cache (default: `venus-model-storage`)
- `INFERENCE_MODEL_ROOT` - Path where KServe mounts the PVC inside runtime containers (default: `/mnt/models`)
- `GPU_PROFILE_PATH` - Optional JSON file describing cluster GPU profiles (default: `/app/config/gpu-profiles.json`)
- `STATE_PATH` - Directory where the BoltDB/SQLite state file (jobs/history) is stored (default: `/app/state`)
- `DATASTORE_DRIVER` - Persistence backend (`bolt` today, `sqlite` once Phase 1 ships) (default: `bolt`)
- `DATASTORE_DSN` - Optional DSN/path override for the persistence layer (defaults to `<STATE_PATH>/model-manager.db`)
- `DATABASE_PVC_NAME` - PVC providing storage for the persistence volume (default: `model-manager-db`)
- `HUGGINGFACE_API_TOKEN` - Optional token for private HuggingFace models
- `HUGGINGFACE_CACHE_TTL` - Cache TTL for Hugging Face lookups (default: `5m`)
- `GITHUB_TOKEN` - Optional token for calling the GitHub API when scraping vLLM metadata
- `VLLM_CACHE_TTL` - Cache TTL for upstream vLLM scraping (default: `10m`)
- `RECOMMENDATION_CACHE_TTL` - Cache TTL for recommendation responses (default: `15m`)
- `CATALOG_REPO` - GitHub repo slug (`owner/repo`) for PR automation (enables `/catalog/pr`)
- `CATALOG_BASE_BRANCH` - Default base branch for catalog PRs (default: `main`)
- `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` - Identity to use when creating commits in the catalog repo
- `MODEL_MANAGER_API_TOKEN` - Optional bearer token required for mutating endpoints (activation, installs, PRs)
- `GPU_INVENTORY_SOURCE` - Source for GPU metadata (`k8s-nodes`, `daemonset`, etc.) used by the recommendation engine (default: `k8s-nodes`)
- `PVC_ALERT_THRESHOLD` - Utilization threshold (0–1) where alerts/notifications fire (default: `0.85`)
- `SLACK_WEBHOOK_URL` - Optional webhook used for notifications

## API Endpoints

- `GET /healthz` - Health check
- `GET /system/info` - Service metadata (version, catalog counts, PVC paths, GPU profiles, recent jobs/history)
- `GET /openapi` / `GET /docs` - Machine-readable OpenAPI document and Swagger UI
- `GET /metrics` - Prometheus metrics (request counts, durations, PVC usage)
- `GET /models` - List available models (cached)
- `GET /models/{id}` - Get details for a specific model
- `GET /models/{id}/manifest` - Render the KServe manifest for an existing catalog entry
- `GET /models/{id}/compatibility` - Estimate if the catalog entry fits on a GPU type (or all known GPUs)
- `POST /models/activate` - Activate a model (body: `{"id": "model-id"}`)
- `POST /models/deactivate` - Deactivate the active model
- `POST /models/test` - Dry-run the InferenceService manifest (and optional readiness URL ping)
- `GET /active` - Get information about the currently active model
- `POST /refresh` - Manually force catalog reload
- `POST /catalog/generate` - Generate a catalog JSON stub (wrapper around discovery helpers)
- `POST /catalog/preview` - Validate an ad-hoc catalog model and render its manifest
- `POST /catalog/validate` - Validate a catalog entry against schema + cluster resources
- `POST /catalog/pr` - Save a catalog entry, commit it, and open a GitHub pull request
- `POST /vllm/model-info` - Describe a Hugging Face model (metadata, compatibility, suggested catalog entry)
- `GET /huggingface/search` - Proxy Hugging Face search for vLLM-friendly results
- `GET /huggingface/models/{id}` - Fetch Hugging Face metadata + compatibility info (GET variant of `/vllm/model-info`)
- `GET /recommendations/{gpuType}` - Suggested vLLM flags/notes for the GPU profile
- `GET /recommendations/profiles` - List known GPU profiles (useful for UI dropdowns)
- `GET /weights` - List all installed weight directories
- `GET /weights/usage` - PVC usage statistics
- `GET /weights/{name}/info` - Inspect a specific weight directory
- `DELETE /weights/{name}` - Delete cached weights
- `POST /weights/install` - Install weights from HuggingFace (body includes `hfModelId`, optional `revision`, `files`, etc.)
  - Response includes the `storageUri` (`pvc://...`) and `inferenceModelPath` you can paste directly into the catalog entry (`MODEL_ID` env) so the runtime loads the cached copy. When async mode is enabled the endpoint returns `202 Accepted` plus a `job` object you can poll below.
- `GET /weights/install/status/{id}` - Convenience alias for checking install job status
- `GET /jobs` / `GET /jobs/{id}` - Inspect asynchronous work (weight installs, etc.)
- `GET /history` - Fetch recent install/activation/deletion events for UI timelines
- `GET /vllm/supported-models` - List vLLM-supported architectures scraped from GitHub
- `GET /vllm/model/{architecture}` - Fetch source/template metadata for a single vLLM runtime class
- `POST /vllm/discover` - Generate a catalog config for a HuggingFace model

## Building

```bash
docker build -t ghcr.io/oremus-labs/ol-model-manager:0.4.11-go .
docker push ghcr.io/oremus-labs/ol-model-manager:0.4.11-go
```

## Running Locally

```bash
pip install -r requirements.txt
export MODEL_CATALOG_ROOT=/path/to/ol-model-catalog
uvicorn app.main:app --host 0.0.0.0 --port 8080
```
