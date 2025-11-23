package validator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/xeipuuv/gojsonschema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

type Options struct {
	SchemaPath         string
	Namespace          string
	KubernetesClient   kubernetes.Interface
	WeightsPVCName     string
	InferenceModelRoot string
	GPUProfilePath     string
}

type Validator struct {
	schemaLoader       gojsonschema.JSONLoader
	kube               kubernetes.Interface
	namespace          string
	weightsPVC         string
	inferenceModelRoot string
	gpuProfiles        map[string]GPUProfile
}

type Result struct {
	Valid       bool          `json:"valid"`
	Errors      []string      `json:"errors,omitempty"`
	Checks      []CheckResult `json:"checks,omitempty"`
	GeneratedAt time.Time     `json:"generatedAt"`
}

type CheckResult struct {
	Name     string            `json:"name"`
	Status   Status            `json:"status"`
	Message  string            `json:"message"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type GPUProfile struct {
	Name     string            `json:"name"`
	MemoryGB int               `json:"memoryGB"`
	Labels   map[string]string `json:"labels,omitempty"`
}

func New(opts Options) (*Validator, error) {
	v := &Validator{
		kube:               opts.KubernetesClient,
		namespace:          opts.Namespace,
		weightsPVC:         opts.WeightsPVCName,
		inferenceModelRoot: opts.InferenceModelRoot,
		gpuProfiles:        map[string]GPUProfile{},
	}

	if opts.SchemaPath != "" {
		data, err := os.ReadFile(opts.SchemaPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read schema: %w", err)
		}
		v.schemaLoader = gojsonschema.NewBytesLoader(data)
	}

	if opts.GPUProfilePath != "" {
		if err := v.loadGPUProfiles(opts.GPUProfilePath); err != nil {
			return nil, err
		}
	}

	return v, nil
}

func (v *Validator) loadGPUProfiles(path string) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("failed to read GPU profile file: %w", err)
	}
	var profiles []GPUProfile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return fmt.Errorf("failed to parse GPU profile file: %w", err)
	}
	for _, profile := range profiles {
		if profile.Name == "" {
			continue
		}
		key := strings.ToLower(profile.Name)
		v.gpuProfiles[key] = profile
	}
	return nil
}

func (v *Validator) Validate(ctx context.Context, payload []byte, model *catalog.Model) Result {
	result := Result{Valid: true, GeneratedAt: time.Now()}

	if model == nil {
		result.Valid = false
		result.Errors = append(result.Errors, "model payload missing")
		return result
	}

	raw := payload
	if len(raw) == 0 {
		b, err := json.Marshal(model)
		if err == nil {
			raw = b
		}
	}

	if v.schemaLoader != nil && len(raw) > 0 {
		schemaResult, err := gojsonschema.Validate(v.schemaLoader, gojsonschema.NewBytesLoader(raw))
		if err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("schema validation error: %v", err))
		} else if !schemaResult.Valid() {
			result.Valid = false
			for _, e := range schemaResult.Errors() {
				result.Errors = append(result.Errors, e.String())
			}
		}
	}

	result.Checks = append(result.Checks, v.checkStorage(ctx, model))
	result.Checks = append(result.Checks, v.checkLocalWeights(model))
	result.Checks = append(result.Checks, v.checkSecretRefs(ctx, model)...)
	result.Checks = append(result.Checks, v.checkConfigMapRefs(ctx, model)...)
	result.Checks = append(result.Checks, v.checkGPU(ctx, model))

	for _, check := range result.Checks {
		if check.Status == StatusFail {
			result.Valid = false
			break
		}
	}

	return result
}

func (v *Validator) checkStorage(ctx context.Context, model *catalog.Model) CheckResult {
	if model.StorageURI == "" {
		return CheckResult{Name: "storage", Status: StatusWarn, Message: "model has no storageUri configured"}
	}

	pvcName, subPath, ok := parsePVC(model.StorageURI)
	if !ok {
		return CheckResult{Name: "storage", Status: StatusPass, Message: "storageUri does not reference a PVC"}
	}

	if v.kube == nil {
		return CheckResult{Name: "storage", Status: StatusWarn, Message: "kubernetes client not configured"}
	}

	pvc, err := v.kube.CoreV1().PersistentVolumeClaims(v.namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return CheckResult{Name: "storage", Status: StatusFail, Message: fmt.Sprintf("PVC %s not found in namespace %s", pvcName, v.namespace)}
		}
		return CheckResult{Name: "storage", Status: StatusWarn, Message: fmt.Sprintf("failed to read PVC %s: %v", pvcName, err)}
	}

	msg := fmt.Sprintf("PVC %s found (phase %s)", pvc.Name, pvc.Status.Phase)
	if subPath != "" {
		msg = fmt.Sprintf("%s subpath %s", msg, subPath)
	}

	metadata := map[string]string{"pvc": pvc.Name, "phase": string(pvc.Status.Phase)}
	if v.weightsPVC != "" && pvc.Name != v.weightsPVC {
		return CheckResult{Name: "storage", Status: StatusWarn, Message: fmt.Sprintf("PVC %s differs from configured cache %s", pvc.Name, v.weightsPVC), Metadata: metadata}
	}

	return CheckResult{Name: "storage", Status: StatusPass, Message: msg, Metadata: metadata}
}

func (v *Validator) checkLocalWeights(model *catalog.Model) CheckResult {
	if v.inferenceModelRoot == "" {
		return CheckResult{Name: "local-cache", Status: StatusWarn, Message: "inference model root not configured"}
	}

	pvcName, subPath, ok := parsePVC(model.StorageURI)
	if !ok || subPath == "" {
		return CheckResult{Name: "local-cache", Status: StatusWarn, Message: "storageUri does not provide a PVC subpath"}
	}

	localPath := filepath.Join(v.inferenceModelRoot, subPath)
	info, err := os.Stat(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			msg := fmt.Sprintf("weights not present at %s (PVC %s)", localPath, pvcName)
			return CheckResult{Name: "local-cache", Status: StatusWarn, Message: msg}
		}
		return CheckResult{Name: "local-cache", Status: StatusWarn, Message: fmt.Sprintf("failed to stat %s: %v", localPath, err)}
	}

	if !info.IsDir() {
		return CheckResult{Name: "local-cache", Status: StatusFail, Message: fmt.Sprintf("%s exists but is not a directory", localPath)}
	}

	return CheckResult{Name: "local-cache", Status: StatusPass, Message: fmt.Sprintf("cached weights located at %s", localPath), Metadata: map[string]string{"path": localPath}}
}

func (v *Validator) checkSecretRefs(ctx context.Context, model *catalog.Model) []CheckResult {
	refs := collectSecretRefs(model)
	if len(refs) == 0 {
		return nil
	}

	if v.kube == nil {
		return []CheckResult{{Name: "secrets", Status: StatusWarn, Message: "kubernetes client not configured"}}
	}

	var results []CheckResult
	for name, optional := range refs {
		_, err := v.kube.CoreV1().Secrets(v.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				status := StatusFail
				if optional {
					status = StatusWarn
				}
				msg := fmt.Sprintf("secret %s not found", name)
				if optional {
					msg += " (optional)"
				}
				results = append(results, CheckResult{Name: "secret:" + name, Status: status, Message: msg})
				continue
			}
			results = append(results, CheckResult{Name: "secret:" + name, Status: StatusWarn, Message: fmt.Sprintf("failed to read secret %s: %v", name, err)})
			continue
		}
		results = append(results, CheckResult{Name: "secret:" + name, Status: StatusPass, Message: "secret present"})
	}

	return results
}

func (v *Validator) checkConfigMapRefs(ctx context.Context, model *catalog.Model) []CheckResult {
	refs := collectConfigMapRefs(model)
	if len(refs) == 0 {
		return nil
	}
	if v.kube == nil {
		return []CheckResult{{Name: "configmaps", Status: StatusWarn, Message: "kubernetes client not configured"}}
	}

	var results []CheckResult
	for name, optional := range refs {
		_, err := v.kube.CoreV1().ConfigMaps(v.namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				status := StatusFail
				if optional {
					status = StatusWarn
				}
				msg := fmt.Sprintf("configmap %s not found", name)
				if optional {
					msg += " (optional)"
				}
				results = append(results, CheckResult{Name: "configmap:" + name, Status: status, Message: msg})
				continue
			}
			results = append(results, CheckResult{Name: "configmap:" + name, Status: StatusWarn, Message: fmt.Sprintf("failed to read configmap %s: %v", name, err)})
			continue
		}
		results = append(results, CheckResult{Name: "configmap:" + name, Status: StatusPass, Message: "configmap present"})
	}

	return results
}

func collectSecretRefs(model *catalog.Model) map[string]bool {
	refs := make(map[string]bool)
	if model == nil {
		return refs
	}
	for _, env := range model.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			name := env.ValueFrom.SecretKeyRef.Name
			if name == "" {
				continue
			}
			optional := false
			if env.ValueFrom.SecretKeyRef.Optional != nil {
				optional = *env.ValueFrom.SecretKeyRef.Optional
			}
			if existing, ok := refs[name]; ok {
				if !optional {
					refs[name] = false
				} else {
					refs[name] = existing
				}
			} else {
				refs[name] = optional
			}
		}
	}
	for _, vol := range model.Volumes {
		if vol.Secret == nil {
			continue
		}
		name := vol.Secret.SecretName
		if name == "" {
			continue
		}
		optional := false
		if vol.Secret.Optional != nil {
			optional = *vol.Secret.Optional
		}
		if existing, ok := refs[name]; ok {
			if !optional {
				refs[name] = false
			} else {
				refs[name] = existing
			}
		} else {
			refs[name] = optional
		}
	}
	return refs
}

func collectConfigMapRefs(model *catalog.Model) map[string]bool {
	refs := make(map[string]bool)
	if model == nil {
		return refs
	}
	for _, env := range model.Env {
		if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
			name := env.ValueFrom.ConfigMapKeyRef.Name
			if name == "" {
				continue
			}
			optional := false
			if env.ValueFrom.ConfigMapKeyRef.Optional != nil {
				optional = *env.ValueFrom.ConfigMapKeyRef.Optional
			}
			if existing, ok := refs[name]; ok {
				if !optional {
					refs[name] = false
				} else {
					refs[name] = existing
				}
			} else {
				refs[name] = optional
			}
		}
	}
	for _, vol := range model.Volumes {
		if vol.ConfigMap == nil {
			continue
		}
		name := vol.ConfigMap.Name
		if name == "" {
			continue
		}
		optional := false
		if vol.ConfigMap.Optional != nil {
			optional = *vol.ConfigMap.Optional
		}
		if existing, ok := refs[name]; ok {
			if !optional {
				refs[name] = false
			} else {
				refs[name] = existing
			}
		} else {
			refs[name] = optional
		}
	}
	return refs
}

func parsePVC(uri string) (string, string, bool) {
	const prefix = "pvc://"
	if !strings.HasPrefix(uri, prefix) {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(uri, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "", "", false
	}
	pvc := parts[0]
	subPath := ""
	if len(parts) == 2 {
		subPath = strings.Trim(parts[1], "/")
	}
	return pvc, subPath, true
}
