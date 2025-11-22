"""Model Manager Service - Main entry point."""
import os
import logging
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from typing import Optional

from .model_catalog import ModelCatalog
from .kserve_client import KServeClient

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)

# Initialize FastAPI app
app = FastAPI(
    title="Model Manager",
    description="Dynamic LLM model management for KServe",
    version="0.1.4"
)

# Configuration from environment
MODEL_CATALOG_ROOT = os.getenv("MODEL_CATALOG_ROOT", "/workspace/catalog")
MODEL_CATALOG_MODELS_SUBDIR = os.getenv("MODEL_CATALOG_MODELS_SUBDIR", "models")
ACTIVE_NAMESPACE = os.getenv("ACTIVE_NAMESPACE", "ai")
ACTIVE_INFERENCESERVICE_NAME = os.getenv("ACTIVE_INFERENCESERVICE_NAME", "active-llm")

# Initialize components
catalog = ModelCatalog(MODEL_CATALOG_ROOT, MODEL_CATALOG_MODELS_SUBDIR)
kserve_client = KServeClient(ACTIVE_NAMESPACE, ACTIVE_INFERENCESERVICE_NAME)


class ActivateRequest(BaseModel):
    """Request to activate a model."""
    id: str


class DeactivateRequest(BaseModel):
    """Request to deactivate the active model."""
    pass


@app.on_event("startup")
async def startup_event():
    """Load catalog on startup."""
    logger.info("Starting Model Manager")
    logger.info(f"Catalog root: {MODEL_CATALOG_ROOT}")
    logger.info(f"Models subdir: {MODEL_CATALOG_MODELS_SUBDIR}")
    logger.info(f"Active namespace: {ACTIVE_NAMESPACE}")
    logger.info(f"Active InferenceService: {ACTIVE_INFERENCESERVICE_NAME}")

    try:
        catalog.load_catalog()
        logger.info(f"Loaded {len(catalog.models)} models from catalog")
    except Exception as e:
        logger.error(f"Failed to load catalog: {e}")
        raise


@app.get("/healthz")
async def health():
    """Health check endpoint."""
    return {"status": "ok"}


@app.get("/models")
async def list_models():
    """List all available models."""
    try:
        catalog.reload_catalog()
    except Exception as e:
        logger.error(f"Failed to reload catalog: {e}")
        raise HTTPException(status_code=500, detail="Failed to reload model catalog")

    return catalog.list_models()


@app.get("/models/{model_id}")
async def get_model(model_id: str):
    """Get details for a specific model."""
    try:
        catalog.reload_catalog()
    except Exception as e:
        logger.error(f"Failed to reload catalog: {e}")
        raise HTTPException(status_code=500, detail="Failed to reload model catalog")

    model = catalog.get_model(model_id)
    if not model:
        raise HTTPException(status_code=404, detail=f"Model {model_id} not found")
    return model


@app.post("/models/activate")
async def activate_model(request: ActivateRequest):
    """Activate a model by creating/updating the InferenceService."""
    logger.info(f"Activating model: {request.id}")

    # Get model config
    try:
        catalog.reload_catalog()
    except Exception as e:
        logger.error(f"Failed to reload catalog: {e}")
        raise HTTPException(status_code=500, detail="Failed to reload model catalog")

    model = catalog.get_model(request.id)
    if not model:
        raise HTTPException(status_code=404, detail=f"Model {request.id} not found")

    # Create/update InferenceService
    try:
        result = kserve_client.activate_model(model)
        logger.info(f"Successfully activated model: {request.id}")
        return {
            "status": "success",
            "message": f"Model {request.id} activated",
            "model": model,
            "inferenceservice": result
        }
    except Exception as e:
        logger.error(f"Failed to activate model {request.id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/models/deactivate")
async def deactivate_model(request: DeactivateRequest = DeactivateRequest()):
    """Deactivate the active model."""
    logger.info("Deactivating active model")

    try:
        result = kserve_client.deactivate_model()
        logger.info("Successfully deactivated model")
        return {
            "status": "success",
            "message": "Active model deactivated",
            "result": result
        }
    except Exception as e:
        logger.error(f"Failed to deactivate model: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/active")
async def get_active_model():
    """Get information about the currently active model."""
    try:
        inference_service = kserve_client.get_active_inferenceservice()
        if not inference_service:
            return {
                "status": "none",
                "message": "No active model"
            }

        return {
            "status": "active",
            "inferenceservice": inference_service
        }
    except Exception as e:
        logger.error(f"Failed to get active model: {e}")
        raise HTTPException(status_code=500, detail=str(e))
