# Model Manager

HTTP API service for dynamically managing KServe InferenceServices based on model catalog configurations.

## Features

- List available models from git-synced catalog with automatic refresh caching
- Activate and deactivate KServe InferenceServices for catalog entries
- Inspect and manage cached HuggingFace weights on the Venus PVC
- Install new model weights directly from HuggingFace (with optional auth token)
- Generate draft catalog entries from HuggingFace metadata via vLLM discovery helpers
- Query PVC usage statistics and supported vLLM architectures
- Validate catalog entries against the shared schema, PVC/secret availability, and GPU capacity
- Dry-run KServe activations (and optional readiness probes) before flipping production traffic
- Estimate GPU compatibility + runtime recommendations per catalog entry
- Inspect Hugging Face metadata (downloads, tags, compatibility) before installing a model

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
- `HUGGINGFACE_API_TOKEN` - Optional token for private HuggingFace models
- `GITHUB_TOKEN` - Optional token for calling the GitHub API when scraping vLLM metadata
- `CATALOG_REPO` - GitHub repo slug (`owner/repo`) for PR automation (enables `/catalog/pr`)
- `CATALOG_BASE_BRANCH` - Default base branch for catalog PRs (default: `main`)
- `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` - Identity to use when creating commits in the catalog repo
- `MODEL_MANAGER_API_TOKEN` - Optional bearer token required for mutating endpoints (activation, installs, PRs)

## API Endpoints

- `GET /healthz` - Health check
- `GET /metrics` - Prometheus metrics (request counts, durations, PVC usage)
- `GET /models` - List available models (cached)
- `GET /models/{id}` - Get details for a specific model
- `GET /models/{id}/compatibility` - Estimate if the catalog entry fits on a GPU type (or all known GPUs)
- `POST /models/activate` - Activate a model (body: `{"id": "model-id"}`)
- `POST /models/deactivate` - Deactivate the active model
- `POST /models/test` - Dry-run the InferenceService manifest (and optional readiness URL ping)
- `GET /active` - Get information about the currently active model
- `POST /refresh` - Manually force catalog reload
- `POST /catalog/generate` - Generate a catalog JSON stub (wrapper around discovery helpers)
- `POST /catalog/validate` - Validate a catalog entry against schema + cluster resources
- `POST /catalog/pr` - Save a catalog entry, commit it, and open a GitHub pull request
- `POST /vllm/model-info` - Describe a Hugging Face model (metadata, compatibility, suggested catalog entry)
- `GET /recommendations/{gpuType}` - Suggested vLLM flags/notes for the GPU profile
- `GET /weights` - List all installed weight directories
- `GET /weights/usage` - PVC usage statistics
- `GET /weights/{name}/info` - Inspect a specific weight directory
- `DELETE /weights/{name}` - Delete cached weights
- `POST /weights/install` - Install weights from HuggingFace (body includes `hfModelId`, optional `revision`, `files`, etc.)
  - Response includes the `storageUri` (`pvc://...`) and `inferenceModelPath` you can paste directly into the catalog entry (`MODEL_ID` env) so the runtime loads the cached copy.
- `GET /vllm/supported-models` - List vLLM-supported architectures scraped from GitHub
- `POST /vllm/discover` - Generate a catalog config for a HuggingFace model

## Building

```bash
docker build -t ghcr.io/oremus-labs/ol-model-manager:0.4.2-go .
docker push ghcr.io/oremus-labs/ol-model-manager:0.4.2-go
```

## Running Locally

```bash
pip install -r requirements.txt
export MODEL_CATALOG_ROOT=/path/to/ol-model-catalog
uvicorn app.main:app --host 0.0.0.0 --port 8080
```
