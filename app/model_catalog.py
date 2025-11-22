"""Model Catalog - Manages loading and accessing model configurations."""
import os
import json
import logging
from pathlib import Path
from typing import Dict, List, Optional

logger = logging.getLogger(__name__)


class ModelCatalog:
    """Manages the model catalog."""

    def __init__(self, catalog_root: str, models_subdir: str):
        """Initialize the catalog."""
        self.catalog_root = Path(catalog_root)
        self.models_subdir = models_subdir
        self.models: Dict[str, dict] = {}

    def load_catalog(self):
        """Load all model configurations from the catalog."""
        models_path = self.catalog_root / self.models_subdir

        if not models_path.exists():
            logger.warning(f"Models directory does not exist: {models_path}")
            return

        logger.info(f"Loading models from: {models_path}")

        # Load all JSON files
        for json_file in models_path.glob("*.json"):
            try:
                with open(json_file, 'r') as f:
                    model_config = json.load(f)

                model_id = model_config.get("id")
                if not model_id:
                    logger.warning(f"Model config missing 'id' field: {json_file}")
                    continue

                self.models[model_id] = model_config
                logger.info(f"Loaded model: {model_id}")

            except Exception as e:
                logger.error(f"Failed to load model config {json_file}: {e}")

    def list_models(self) -> List[dict]:
        """List all available models."""
        return [
            {
                "id": model["id"],
                "displayName": model.get("displayName", model["id"]),
                "hfModelId": model.get("hfModelId"),
                "runtime": model.get("runtime")
            }
            for model in self.models.values()
        ]

    def get_model(self, model_id: str) -> Optional[dict]:
        """Get a specific model configuration."""
        return self.models.get(model_id)

    def reload_catalog(self):
        """Reload the catalog from disk."""
        self.models.clear()
        self.load_catalog()
