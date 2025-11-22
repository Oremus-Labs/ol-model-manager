"""KServe Client - Manages InferenceService resources."""
import logging
from typing import Optional, Dict, Any
from kubernetes import client, config
from kubernetes.client.rest import ApiException

logger = logging.getLogger(__name__)


class KServeClient:
    """Client for managing KServe InferenceServices."""

    def __init__(self, namespace: str, inferenceservice_name: str):
        """Initialize the KServe client."""
        self.namespace = namespace
        self.inferenceservice_name = inferenceservice_name

        # Load in-cluster config
        try:
            config.load_incluster_config()
            logger.info("Loaded in-cluster Kubernetes config")
        except Exception as e:
            logger.warning(f"Failed to load in-cluster config: {e}")
            logger.info("Attempting to load local kubeconfig")
            config.load_kube_config()

        self.custom_api = client.CustomObjectsApi()
        self.group = "serving.kserve.io"
        self.version = "v1beta1"
        self.plural = "inferenceservices"

    def activate_model(self, model_config: dict) -> dict:
        """Create or update an InferenceService for the given model."""
        logger.info(f"Activating model: {model_config['id']}")

        # Build InferenceService spec
        inference_service = self._build_inferenceservice(model_config)

        try:
            # Try to get existing InferenceService
            existing = self.custom_api.get_namespaced_custom_object(
                group=self.group,
                version=self.version,
                namespace=self.namespace,
                plural=self.plural,
                name=self.inferenceservice_name
            )

            # Update existing
            logger.info(f"Updating existing InferenceService: {self.inferenceservice_name}")
            result = self.custom_api.patch_namespaced_custom_object(
                group=self.group,
                version=self.version,
                namespace=self.namespace,
                plural=self.plural,
                name=self.inferenceservice_name,
                body=inference_service
            )
            return {"action": "updated", "name": self.inferenceservice_name}

        except ApiException as e:
            if e.status == 404:
                # Create new
                logger.info(f"Creating new InferenceService: {self.inferenceservice_name}")
                result = self.custom_api.create_namespaced_custom_object(
                    group=self.group,
                    version=self.version,
                    namespace=self.namespace,
                    plural=self.plural,
                    body=inference_service
                )
                return {"action": "created", "name": self.inferenceservice_name}
            else:
                raise

    def deactivate_model(self) -> dict:
        """Delete the active InferenceService."""
        logger.info(f"Deactivating InferenceService: {self.inferenceservice_name}")

        try:
            self.custom_api.delete_namespaced_custom_object(
                group=self.group,
                version=self.version,
                namespace=self.namespace,
                plural=self.plural,
                name=self.inferenceservice_name
            )
            return {"action": "deleted", "name": self.inferenceservice_name}

        except ApiException as e:
            if e.status == 404:
                logger.info(f"InferenceService already does not exist: {self.inferenceservice_name}")
                return {"action": "already_deleted", "name": self.inferenceservice_name}
            else:
                raise

    def get_active_inferenceservice(self) -> Optional[dict]:
        """Get the current active InferenceService."""
        try:
            result = self.custom_api.get_namespaced_custom_object(
                group=self.group,
                version=self.version,
                namespace=self.namespace,
                plural=self.plural,
                name=self.inferenceservice_name
            )
            return result
        except ApiException as e:
            if e.status == 404:
                return None
            else:
                raise

    def _build_inferenceservice(self, model_config: dict) -> dict:
        """Build an InferenceService manifest from model config."""
        # Base InferenceService structure
        isvc = {
            "apiVersion": f"{self.group}/{self.version}",
            "kind": "InferenceService",
            "metadata": {
                "name": self.inferenceservice_name,
                "namespace": self.namespace,
                "annotations": {
                    "serving.kserve.io/secretName": "hf-token",
                    "model-manager/model-id": model_config["id"]
                }
            },
            "spec": {
                "predictor": {
                    "minReplicas": 1,
                    "model": {
                        "modelFormat": {
                            "name": "custom"
                        },
                        "runtime": model_config.get("runtime", "qwen-vllm-runtime"),
                        "storageUri": f"hf://{model_config['hfModelId']}",
                        "storage": {
                            "persistentVolumeClaim": {
                                "claimName": "venus-model-storage",
                                "subPath": model_config["id"]
                            }
                        }
                    }
                }
            }
        }

        # Add node selector if provided
        if "nodeSelector" in model_config:
            isvc["spec"]["predictor"]["nodeSelector"] = model_config["nodeSelector"]

        # Add tolerations if provided
        if "tolerations" in model_config:
            isvc["spec"]["predictor"]["tolerations"] = model_config["tolerations"]

        # Add resources if provided
        if "resources" in model_config:
            isvc["spec"]["predictor"]["model"]["resources"] = model_config["resources"]

        return isvc
