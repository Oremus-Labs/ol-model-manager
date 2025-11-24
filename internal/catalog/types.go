package catalog

// Model represents a complete model configuration.
type Model struct {
	ID           string            `json:"id"`
	DisplayName  string            `json:"displayName,omitempty"`
	HFModelID    string            `json:"hfModelId,omitempty"`
	ServedModelName string         `json:"servedModelName,omitempty"`
	StorageURI   string            `json:"storageUri,omitempty"`
	Runtime      string            `json:"runtime,omitempty"`
	Env          []EnvVar          `json:"env,omitempty"`
	Storage      *Storage          `json:"storage,omitempty"`
	VLLM         *VLLMConfig       `json:"vllm,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	Tolerations  []Toleration      `json:"tolerations,omitempty"`
	Resources    *Resources        `json:"resources,omitempty"`
	VolumeMounts []VolumeMount     `json:"volumeMounts,omitempty"`
	Volumes      []Volume          `json:"volumes,omitempty"`
}

// ModelSummary is a simplified model representation for listing.
type ModelSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	HFModelID   string `json:"hfModelId,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
}

// EnvVar represents an environment variable.
type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

// EnvVarSource represents a source for the value of an EnvVar.
type EnvVarSource struct {
	SecretKeyRef    *SecretKeySelector    `json:"secretKeyRef,omitempty"`
	ConfigMapKeyRef *ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
}

// SecretKeySelector selects a key of a secret.
type SecretKeySelector struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Optional *bool  `json:"optional,omitempty"`
}

// ConfigMapKeySelector selects a key of a ConfigMap.
type ConfigMapKeySelector struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Optional *bool  `json:"optional,omitempty"`
}

// Storage configuration for model storage.
type Storage struct {
	Key        string `json:"key,omitempty"`
	Path       string `json:"path,omitempty"`
	SchemaPath string `json:"schemaPath,omitempty"`
	StorageKey string `json:"storageKey,omitempty"`
}

// VLLMConfig holds vLLM-specific configuration.
type VLLMConfig struct {
	TensorParallelSize   *int     `json:"tensorParallelSize,omitempty"`
	Dtype                string   `json:"dtype,omitempty"`
	GPUMemoryUtilization *float64 `json:"gpuMemoryUtilization,omitempty"`
	MaxModelLen          *int     `json:"maxModelLen,omitempty"`
	TrustRemoteCode      *bool    `json:"trustRemoteCode,omitempty"`
}

// Toleration represents a Kubernetes toleration.
type Toleration struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

// Resources defines resource requests and limits.
type Resources struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// VolumeMount represents a Kubernetes volume mount.
type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// Volume represents a Kubernetes volume.
type Volume struct {
	Name      string                 `json:"name"`
	PVC       *PVCSource             `json:"persistentVolumeClaim,omitempty"`
	Secret    *SecretVolumeSource    `json:"secret,omitempty"`
	ConfigMap *ConfigMapVolumeSource `json:"configMap,omitempty"`
}

// PVCSource represents a PersistentVolumeClaim source.
type PVCSource struct {
	ClaimName string `json:"claimName"`
}

// SecretVolumeSource references a secret to populate a volume.
type SecretVolumeSource struct {
	SecretName string `json:"secretName"`
	Optional   *bool  `json:"optional,omitempty"`
}

// ConfigMapVolumeSource references a ConfigMap to populate a volume.
type ConfigMapVolumeSource struct {
	Name     string `json:"name"`
	Optional *bool  `json:"optional,omitempty"`
}
