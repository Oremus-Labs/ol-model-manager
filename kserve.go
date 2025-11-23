package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KServeClient manages KServe InferenceServices
type KServeClient struct {
	client                 dynamic.Interface
	namespace              string
	inferenceServiceName   string
	gvr                    schema.GroupVersionResource
}

// ActivateResult represents the result of activating a model
type ActivateResult struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}

// DeactivateResult represents the result of deactivating a model
type DeactivateResult struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}

// NewKServeClient creates a new KServe client
func NewKServeClient(namespace, inferenceServiceName string) (*KServeClient, error) {
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &KServeClient{
		client:               client,
		namespace:            namespace,
		inferenceServiceName: inferenceServiceName,
		gvr: schema.GroupVersionResource{
			Group:    "serving.kserve.io",
			Version:  "v1beta1",
			Resource: "inferenceservices",
		},
	}, nil
}

func getKubeConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		log.Println("Loaded in-cluster Kubernetes config")
		return config, nil
	}

	log.Printf("Failed to load in-cluster config: %v", err)
	log.Println("Attempting to load local kubeconfig")

	// Try local kubeconfig
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	return config, nil
}

// ActivateModel creates or updates an InferenceService
func (kc *KServeClient) ActivateModel(model *ModelConfig) (*ActivateResult, error) {
	log.Printf("Activating model: %s", model.ID)

	// Build InferenceService manifest
	isvc := kc.buildInferenceService(model)

	ctx := context.Background()

	// Try to get existing InferenceService
	_, err := kc.client.Resource(kc.gvr).Namespace(kc.namespace).Get(ctx, kc.inferenceServiceName, metav1.GetOptions{})

	if err == nil {
		// InferenceService exists, update it
		log.Printf("Updating existing InferenceService: %s", kc.inferenceServiceName)
		_, err = kc.client.Resource(kc.gvr).Namespace(kc.namespace).Update(ctx, isvc, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to update InferenceService: %w", err)
		}
		return &ActivateResult{Action: "updated", Name: kc.inferenceServiceName}, nil
	}

	// InferenceService doesn't exist, create it
	log.Printf("Creating InferenceService: %s", kc.inferenceServiceName)
	_, err = kc.client.Resource(kc.gvr).Namespace(kc.namespace).Create(ctx, isvc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create InferenceService: %w", err)
	}

	return &ActivateResult{Action: "created", Name: kc.inferenceServiceName}, nil
}

// DeactivateModel deletes the active InferenceService
func (kc *KServeClient) DeactivateModel() (*DeactivateResult, error) {
	log.Printf("Deactivating InferenceService: %s", kc.inferenceServiceName)

	ctx := context.Background()

	err := kc.client.Resource(kc.gvr).Namespace(kc.namespace).Delete(ctx, kc.inferenceServiceName, metav1.DeleteOptions{})
	if err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "not found") {
			log.Printf("InferenceService already does not exist: %s", kc.inferenceServiceName)
			return &DeactivateResult{Action: "already_deleted", Name: kc.inferenceServiceName}, nil
		}
		return nil, fmt.Errorf("failed to delete InferenceService: %w", err)
	}

	return &DeactivateResult{Action: "deleted", Name: kc.inferenceServiceName}, nil
}

// GetActiveInferenceService gets the current active InferenceService
func (kc *KServeClient) GetActiveInferenceService() (map[string]interface{}, error) {
	ctx := context.Background()

	result, err := kc.client.Resource(kc.gvr).Namespace(kc.namespace).Get(ctx, kc.inferenceServiceName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get InferenceService: %w", err)
	}

	return result.UnstructuredContent(), nil
}

func (kc *KServeClient) buildInferenceService(model *ModelConfig) *unstructured.Unstructured {
	// Build storage URI
	storageURI := model.StorageURI
	if storageURI == "" && model.HFModelID != "" {
		storageURI = fmt.Sprintf("hf://%s", model.HFModelID)
	}

	pvcStorage := false
	if strings.HasPrefix(storageURI, "pvc://") {
		pvcStorage = true
	}

	// Build model spec
	modelSpec := map[string]interface{}{
		"modelFormat": map[string]interface{}{
			"name": "custom",
		},
	}

	if model.Runtime != "" {
		modelSpec["runtime"] = model.Runtime
	} else {
		modelSpec["runtime"] = "vllm-runtime"
	}

	if storageURI != "" {
		modelSpec["storageUri"] = storageURI
	}

	if model.Env != nil {
		modelSpec["env"] = model.Env
	}

	if model.Storage != nil {
		modelSpec["storage"] = model.Storage
	}

	// Build vLLM args
	vllmArgs := buildVLLMArgs(model.VLLM)
	if len(vllmArgs) > 0 {
		modelSpec["args"] = vllmArgs
	}

	// Build InferenceService
	isvc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "serving.kserve.io/v1beta1",
			"kind":       "InferenceService",
			"metadata": map[string]interface{}{
				"name":      kc.inferenceServiceName,
				"namespace": kc.namespace,
				"annotations": map[string]interface{}{
					"serving.kserve.io/secretName": "hf-token",
					"model-manager/model-id":       model.ID,
				},
			},
			"spec": map[string]interface{}{
				"predictor": map[string]interface{}{
					"minReplicas": 1,
					"model":       modelSpec,
				},
			},
		},
	}

	predictor := isvc.Object["spec"].(map[string]interface{})["predictor"].(map[string]interface{})

	// Add node selector
	if model.NodeSelector != nil {
		predictor["nodeSelector"] = model.NodeSelector
	}

	// Add tolerations
	if model.Tolerations != nil {
		predictor["tolerations"] = model.Tolerations
	}

	// Add resources
	if model.Resources != nil {
		modelSpec["resources"] = model.Resources
		predictor["resources"] = model.Resources
	}

	// Add PVC storage annotation
	if pvcStorage {
		annotations := isvc.Object["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
		annotations["storage.kserve.io/readonly"] = "false"
	}

	// Add volume mounts
	if model.VolumeMounts != nil {
		modelSpec["volumeMounts"] = model.VolumeMounts
	}

	// Add volumes
	if model.Volumes != nil {
		predictor["volumes"] = model.Volumes
	}

	return isvc
}

func buildVLLMArgs(vllmConfig *VLLMConfig) []string {
	if vllmConfig == nil {
		return nil
	}

	args := []string{}

	if vllmConfig.TensorParallelSize != nil {
		args = append(args, "--tensor-parallel-size", fmt.Sprintf("%d", *vllmConfig.TensorParallelSize))
	}

	if vllmConfig.Dtype != "" {
		args = append(args, "--dtype", vllmConfig.Dtype)
	}

	if vllmConfig.GPUMemoryUtilization != nil {
		args = append(args, "--gpu-memory-utilization", fmt.Sprintf("%f", *vllmConfig.GPUMemoryUtilization))
	}

	if vllmConfig.MaxModelLen != nil {
		args = append(args, "--max-model-len", fmt.Sprintf("%d", *vllmConfig.MaxModelLen))
	}

	if vllmConfig.TrustRemoteCode != nil && *vllmConfig.TrustRemoteCode {
		args = append(args, "--trust-remote-code")
	}

	return args
}
