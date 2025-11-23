"""KServe Client - Manages InferenceService resources."""
import logging
import time
from typing import Optional, Dict, Any, List
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
            self.custom_api.get_namespaced_custom_object(
                group=self.group,
                version=self.version,
                namespace=self.namespace,
                plural=self.plural,
                name=self.inferenceservice_name
            )
            logger.info("Existing InferenceService detected; deleting to free resources")
            self.deactivate_model()
            self._wait_for_deletion()
        except ApiException as e:
            if e.status != 404:
                raise

        logger.info(f"Creating InferenceService: {self.inferenceservice_name}")
        self.custom_api.create_namespaced_custom_object(
            group=self.group,
            version=self.version,
            namespace=self.namespace,
            plural=self.plural,
            body=inference_service
        )
        return {"action": "created", "name": self.inferenceservice_name}

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
        # Base InferenceService structure. Allow storageUri to be explicitly disabled (None) so
        # the runtime can pull models directly when desired.
        storage_uri = model_config.get("storageUri")
        if not storage_uri and model_config.get("hfModelId"):
            storage_uri = f"hf://{model_config['hfModelId']}"

        model_spec: Dict[str, Any] = {
            "modelFormat": {
                "name": "custom"
            },
            "runtime": model_config.get("runtime", "vllm-runtime")
        }

        if storage_uri:
            model_spec["storageUri"] = storage_uri

        if "env" in model_config:
            model_spec["env"] = model_config["env"]

        if "storage" in model_config:
            model_spec["storage"] = model_config["storage"]

        vllm_args = self._build_vllm_args(model_config.get("vllm"))
        if vllm_args:
            model_spec["args"] = vllm_args

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
                    "model": model_spec
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
            isvc["spec"]["predictor"]["resources"] = model_config["resources"]

        # Attach extra volume mounts/volumes for persistent storage when explicitly requested.
        if "volumeMounts" in model_config:
            isvc["spec"]["predictor"]["model"]["volumeMounts"] = model_config["volumeMounts"]

        if "volumes" in model_config:
            isvc["spec"]["predictor"]["volumes"] = model_config["volumes"]

        return isvc

    def _build_vllm_args(self, vllm_config: Optional[Dict[str, Any]]) -> List[str]:
        """Translate vLLM configuration into CLI args."""
        if not vllm_config:
            return []

        flag_map = {
            "tensorParallelSize": "--tensor-parallel-size",
            "dtype": "--dtype",
            "gpuMemoryUtilization": "--gpu-memory-utilization",
            "maxModelLen": "--max-model-len",
            "trustRemoteCode": "--trust-remote-code"
        }

        args: List[str] = []
        for key, value in vllm_config.items():
            flag = flag_map.get(key)
            if not flag or value is None:
                continue

            if isinstance(value, bool):
                if value:
                    args.append(flag)
            else:
                args.extend([flag, str(value)])

        return args

    def _wait_for_deletion(self, timeout: int = 300, interval: int = 2):
        """Wait until the InferenceService no longer exists."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                self.custom_api.get_namespaced_custom_object(
                    group=self.group,
                    version=self.version,
                    namespace=self.namespace,
                    plural=self.plural,
                    name=self.inferenceservice_name
                )
                time.sleep(interval)
            except ApiException as e:
                if e.status == 404:
                    return
                raise
        raise TimeoutError(f"InferenceService {self.inferenceservice_name} still exists after {timeout}s")
