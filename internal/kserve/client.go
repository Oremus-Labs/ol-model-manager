// Package kserve provides a client for managing KServe InferenceServices.
package kserve

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/kube"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
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

// DryRunResult captures the outcome of a dry-run activation.
type DryRunResult struct {
	Action   string                 `json:"action"`
	Manifest map[string]interface{} `json:"manifest"`
}

// NewClient creates a new KServe client.
func NewClient(namespace, isvcName, inferenceModelRoot string) (*Client, error) {
	config, err := kube.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	return NewClientWithConfig(config, namespace, isvcName, inferenceModelRoot)
}

// NewClientWithConfig creates a KServe client using the provided REST config.
func NewClientWithConfig(config *rest.Config, namespace, isvcName, inferenceModelRoot string) (*Client, error) {
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

// DryRun renders the InferenceService and performs a server-side dry-run.
func (c *Client) DryRun(model *catalog.Model) (*DryRunResult, error) {
	isvc := buildInferenceService(c.namespace, c.isvcName, model, c.inferenceModelRoot)
	manifest := deepCopyMap(isvc.Object)

	ctx := context.Background()
	action := "create"

	_, err := c.client.Resource(c.gvr).Namespace(c.namespace).Create(ctx, isvc.DeepCopy(), metav1.CreateOptions{
		DryRun: []string{metav1.DryRunAll},
	})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			action = "update"
			existing, getErr := c.client.Resource(c.gvr).Namespace(c.namespace).Get(ctx, c.isvcName, metav1.GetOptions{})
			if getErr != nil {
				return nil, fmt.Errorf("failed to fetch existing InferenceService: %w", getErr)
			}
			isvc.SetResourceVersion(existing.GetResourceVersion())
			_, err = c.client.Resource(c.gvr).Namespace(c.namespace).Update(ctx, isvc.DeepCopy(), metav1.UpdateOptions{
				DryRun: []string{metav1.DryRunAll},
			})
		}
		if err != nil {
			return nil, fmt.Errorf("kserve dry-run failed: %w", err)
		}
	}

	return &DryRunResult{
		Action:   action,
		Manifest: manifest,
	}, nil
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
		if converted := jsonCompatible(envVars); converted != nil {
			modelSpec["env"] = converted
		}
	}

	if model.Storage != nil {
		if converted := jsonCompatible(model.Storage); converted != nil {
			modelSpec["storage"] = converted
		}
	}

	// Add vLLM args if configured
	if vllmArgs := buildVLLMArgs(model.VLLM); len(vllmArgs) > 0 {
		modelSpec["args"] = vllmArgs
	}

	modelSpec = ensureJSONObject(modelSpec)

	predictor := map[string]interface{}{
		"minReplicas": int64(1),
		"model":       modelSpec,
	}

	// Add optional configurations
	if model.NodeSelector != nil {
		predictor["nodeSelector"] = model.NodeSelector
	}

	if model.Tolerations != nil {
		if converted := jsonCompatible(model.Tolerations); converted != nil {
			predictor["tolerations"] = converted
		}
	}

	if model.Resources != nil {
		if converted := jsonCompatible(model.Resources); converted != nil {
			modelSpec["resources"] = converted
			predictor["resources"] = converted
		}
	}

	if model.VolumeMounts != nil {
		if converted := jsonCompatible(model.VolumeMounts); converted != nil {
			modelSpec["volumeMounts"] = converted
		}
	}

	if model.Volumes != nil {
		if converted := jsonCompatible(model.Volumes); converted != nil {
			predictor["volumes"] = converted
		}
	}

	predictor = ensureJSONObject(predictor)

	annotations := map[string]interface{}{
		"serving.kserve.io/secretName": "hf-token",
		"model-manager/model-id":       model.ID,
	}
	if pvcStorage {
		annotations["storage.kserve.io/readonly"] = "false"
	}

	spec := map[string]interface{}{
		"predictor": predictor,
	}

	isvc := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": kserveGroup + "/" + kserveVersion,
			"kind":       "InferenceService",
			"metadata": map[string]interface{}{
				"name":        name,
				"namespace":   namespace,
				"annotations": annotations,
			},
			"spec": spec,
		},
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
	} else {
		copied := make([]catalog.EnvVar, len(env))
		copy(copied, env)
		env = copied
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
		env[i].ValueFrom = nil
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
		return inferenceModelRoot
	}

	subPath := strings.Trim(parts[1], "/")
	return filepath.Join(inferenceModelRoot, subPath)
}

func deepCopyMap(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		switch val := v.(type) {
		case map[string]interface{}:
			dst[k] = deepCopyMap(val)
		case []interface{}:
			dst[k] = deepCopySlice(val)
		default:
			dst[k] = val
		}
	}
	return dst
}

func deepCopySlice(src []interface{}) []interface{} {
	out := make([]interface{}, len(src))
	for i, v := range src {
		switch val := v.(type) {
		case map[string]interface{}:
			out[i] = deepCopyMap(val)
		case []interface{}:
			out[i] = deepCopySlice(val)
		default:
			out[i] = val
		}
	}
	return out
}

func jsonCompatible(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		log.Printf("Failed to marshal object: %v", err)
		return nil
	}
	var out interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		log.Printf("Failed to unmarshal object: %v", err)
		return nil
	}
	return out
}

func ensureJSONObject(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	if converted, ok := jsonCompatible(src).(map[string]interface{}); ok && converted != nil {
		return converted
	}
	return src
}
