# Model Manager

HTTP API service for dynamically managing KServe InferenceServices based on model catalog configurations.

## Features

- List available models from git-synced catalog
- Activate models by creating/updating KServe InferenceServices
- Deactivate models by deleting InferenceServices
- Get status of currently active model

## Environment Variables

- `MODEL_CATALOG_ROOT` - Root path to the catalog (default: `/workspace/catalog`)
- `MODEL_CATALOG_MODELS_SUBDIR` - Subdirectory containing model configs (default: `models`)
- `ACTIVE_NAMESPACE` - Kubernetes namespace for InferenceServices (default: `ai`)
- `ACTIVE_INFERENCESERVICE_NAME` - Name of the InferenceService to manage (default: `active-llm`)

## API Endpoints

- `GET /healthz` - Health check
- `GET /models` - List all available models
- `GET /models/{id}` - Get details for a specific model
- `POST /models/activate` - Activate a model (body: `{"id": "model-id"}`)
- `POST /models/deactivate` - Deactivate the active model
- `GET /active` - Get information about the currently active model

## Building

```bash
docker build -t ghcr.io/oremus-labs/model-manager:v0.1.0 .
docker push ghcr.io/oremus-labs/model-manager:v0.1.0
```

## Running Locally

```bash
pip install -r requirements.txt
export MODEL_CATALOG_ROOT=/path/to/ol-model-catalog
uvicorn app.main:app --host 0.0.0.0 --port 8080
```
