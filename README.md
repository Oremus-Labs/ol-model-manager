# Model Manager

HTTP API service for dynamically managing KServe InferenceServices based on model catalog configurations.

## Features

- List available models from git-synced catalog with automatic refresh caching
- Activate and deactivate KServe InferenceServices for catalog entries
- Inspect and manage cached HuggingFace weights on the Venus PVC
- Install new model weights directly from HuggingFace (with optional auth token)
- Generate draft catalog entries from HuggingFace metadata via vLLM discovery helpers
- Query PVC usage statistics and supported vLLM architectures

## Environment Variables

- `MODEL_CATALOG_ROOT` - Root path to the catalog (default: `/workspace/catalog`)
- `MODEL_CATALOG_MODELS_SUBDIR` - Subdirectory containing model configs (default: `models`)
- `CATALOG_REFRESH_INTERVAL` - TTL before models are reloaded from disk (default: `30s`)
- `ACTIVE_NAMESPACE` - Kubernetes namespace for InferenceServices (default: `ai`)
- `ACTIVE_INFERENCESERVICE_NAME` - Name of the InferenceService to manage (default: `active-llm`)
- `WEIGHTS_STORAGE_PATH` - Root directory for cached weights on the PVC (default: `/mnt/model-storage`)
- `WEIGHTS_INSTALL_TIMEOUT` - Timeout for weight installation operations (default: `30m`)
- `WEIGHTS_PVC_NAME` - Name of the PVC backing the cache (default: `venus-model-storage`)
- `INFERENCE_MODEL_ROOT` - Path where KServe mounts the PVC inside runtime containers (default: `/mnt/models`)
- `HUGGINGFACE_API_TOKEN` - Optional token for private HuggingFace models
- `GITHUB_TOKEN` - Optional token for calling the GitHub API when scraping vLLM metadata

## API Endpoints

- `GET /healthz` - Health check
- `GET /models` - List available models (cached)
- `GET /models/{id}` - Get details for a specific model
- `POST /models/activate` - Activate a model (body: `{"id": "model-id"}`)
- `POST /models/deactivate` - Deactivate the active model
- `GET /active` - Get information about the currently active model
- `POST /refresh` - Manually force catalog reload
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
docker build -t ghcr.io/oremus-labs/ol-model-manager:0.3.0-go .
docker push ghcr.io/oremus-labs/ol-model-manager:0.3.0-go
```

## Running Locally

```bash
pip install -r requirements.txt
export MODEL_CATALOG_ROOT=/path/to/ol-model-catalog
uvicorn app.main:app --host 0.0.0.0 --port 8080
```
