// Package kserve provides a client for managing KServe InferenceServices.
package kserve

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	kserveGroup   = "serving.kserve.io"
	kserveVersion = "v1beta1"
	isvcResource  = "inferenceservices"
)

// Client manages KServe InferenceServices.
type Client struct {
	client             dynamic.Interface
	namespace          string
	isvcName           string
	inferenceModelRoot string
	gvr                schema.GroupVersionResource
}

// Result represents an operation result.
type Result struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}

// NewClient creates a new KServe client.
func NewClient(namespace, isvcName, inferenceModelRoot string) (*Client, error) {
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Client{
		client:             dynClient,
		namespace:          namespace,
		isvcName:           isvcName,
		inferenceModelRoot: inferenceModelRoot,
		gvr: schema.GroupVersionResource{
			Group:    kserveGroup,
			Version:  kserveVersion,
			Resource: isvcResource,
		},
	}, nil
}

func getKubeConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		log.Println("Using in-cluster Kubernetes configuration")
		return config, nil
	}

	log.Printf("In-cluster config not available: %v", err)
	log.Println("Attempting to load local kubeconfig")

	// Fall back to local kubeconfig
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	return config, nil
}

// Activate creates or updates an InferenceService for the given model.
func (c *Client) Activate(model *catalog.Model) (*Result, error) {
	log.Printf("Activating model: %s", model.ID)

	isvc := buildInferenceService(c.namespace, c.isvcName, model, c.inferenceModelRoot)

	ctx := context.Background()

	// Check if InferenceService exists
	_, err := c.client.Resource(c.gvr).Namespace(c.namespace).Get(ctx, c.isvcName, metav1.GetOptions{})
	if err == nil {
		// Update existing
		log.Printf("Updating existing InferenceService: %s", c.isvcName)
		_, err = c.client.Resource(c.gvr).Namespace(c.namespace).Update(ctx, isvc, metav1.UpdateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to update InferenceService: %w", err)
		}
		return &Result{Action: "updated", Name: c.isvcName}, nil
	}

	// Create new
	log.Printf("Creating new InferenceService: %s", c.isvcName)
	_, err = c.client.Resource(c.gvr).Namespace(c.namespace).Create(ctx, isvc, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create InferenceService: %w", err)
	}

	return &Result{Action: "created", Name: c.isvcName}, nil
}

// Deactivate deletes the active InferenceService.
func (c *Client) Deactivate() (*Result, error) {
	log.Printf("Deactivating InferenceService: %s", c.isvcName)

	ctx := context.Background()

	err := c.client.Resource(c.gvr).Namespace(c.namespace).Delete(ctx, c.isvcName, metav1.DeleteOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			log.Printf("InferenceService already deleted: %s", c.isvcName)
			return &Result{Action: "already_deleted", Name: c.isvcName}, nil
		}
		return nil, fmt.Errorf("failed to delete InferenceService: %w", err)
	}

	return &Result{Action: "deleted", Name: c.isvcName}, nil
}

// GetActive retrieves the current active InferenceService.
func (c *Client) GetActive() (map[string]interface{}, error) {
	ctx := context.Background()

	result, err := c.client.Resource(c.gvr).Namespace(c.namespace).Get(ctx, c.isvcName, metav1.GetOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get InferenceService: %w", err)
	}

	return result.UnstructuredContent(), nil
}

func buildInferenceService(namespace, name string, model *catalog.Model, inferenceModelRoot string) *unstructured.Unstructured {
	// Determine storage URI
	storageURI := model.StorageURI
	if storageURI == "" && model.HFModelID != "" {
		storageURI = fmt.Sprintf("hf://%s", model.HFModelID)
	}

	pvcStorage := strings.HasPrefix(storageURI, "pvc://")

	// Build model spec
	modelSpec := map[string]interface{}{
		"modelFormat": map[string]interface{}{
			"name": "custom",
		},
		"runtime": defaultString(model.Runtime, "vllm-runtime"),
	}

	if storageURI != "" {
		modelSpec["storageUri"] = storageURI
	}

	envVars := prepareEnvVars(model.Env, model.StorageURI, inferenceModelRoot)
	if envVars != nil {
		modelSpec["env"] = envVars
	}

	if model.Storage != nil {
		modelSpec["storage"] = model.Storage
	}

	// Add vLLM args if configured
	if vllmArgs := buildVLLMArgs(model.VLLM); len(vllmArgs) > 0 {
		modelSpec["args"] = vllmArgs
	}

	// Build InferenceService manifest
	isvc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": kserveGroup + "/" + kserveVersion,
			"kind":       "InferenceService",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
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

	// Add optional configurations
	if model.NodeSelector != nil {
		predictor["nodeSelector"] = model.NodeSelector
	}

	if model.Tolerations != nil {
		predictor["tolerations"] = model.Tolerations
	}

	if model.Resources != nil {
		modelSpec["resources"] = model.Resources
		predictor["resources"] = model.Resources
	}

	if pvcStorage {
		annotations := isvc.Object["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
		annotations["storage.kserve.io/readonly"] = "false"
	}

	if model.VolumeMounts != nil {
		modelSpec["volumeMounts"] = model.VolumeMounts
	}

	if model.Volumes != nil {
		predictor["volumes"] = model.Volumes
	}

	return isvc
}

func buildVLLMArgs(vllm *catalog.VLLMConfig) []string {
	if vllm == nil {
		return nil
	}

	var args []string

	if vllm.TensorParallelSize != nil {
		args = append(args, "--tensor-parallel-size", fmt.Sprintf("%d", *vllm.TensorParallelSize))
	}

	if vllm.Dtype != "" {
		args = append(args, "--dtype", vllm.Dtype)
	}

	if vllm.GPUMemoryUtilization != nil {
		args = append(args, "--gpu-memory-utilization", fmt.Sprintf("%f", *vllm.GPUMemoryUtilization))
	}

	if vllm.MaxModelLen != nil {
		args = append(args, "--max-model-len", fmt.Sprintf("%d", *vllm.MaxModelLen))
	}

	if vllm.TrustRemoteCode != nil && *vllm.TrustRemoteCode {
		args = append(args, "--trust-remote-code")
	}

	return args
}

func defaultString(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

func prepareEnvVars(env []catalog.EnvVar, storageURI, inferenceModelRoot string) []catalog.EnvVar {
	if env == nil {
		env = []catalog.EnvVar{}
	}

	localPath := deriveLocalModelPath(storageURI, inferenceModelRoot)
	if localPath == "" {
		if len(env) == 0 {
			return nil
		}
		return env
	}

	found := false
	for i, e := range env {
		if e.Name != "MODEL_ID" {
			continue
		}
		found = true
		if strings.HasPrefix(e.Value, "/") {
			break
		}
		env[i].Value = localPath
		break
	}

	if !found {
		env = append(env, catalog.EnvVar{
			Name:  "MODEL_ID",
			Value: localPath,
		})
	}

	return env
}

func deriveLocalModelPath(storageURI, inferenceModelRoot string) string {
	if inferenceModelRoot == "" {
		return ""
	}
	const pvcPrefix = "pvc://"
	if !strings.HasPrefix(storageURI, pvcPrefix) {
		return ""
	}

	trimmed := strings.TrimPrefix(storageURI, pvcPrefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}

	return path.Join(inferenceModelRoot, parts[1])
}
