package openapi

import (
	_ "embed"

	"sigs.k8s.io/yaml"
)

//go:embed spec.yaml
var specYAML []byte

// JSON returns the OpenAPI document serialized as JSON.
func JSON() ([]byte, error) {
	return yaml.YAMLToJSON(specYAML)
}

// YAML returns the raw OpenAPI YAML document.
func YAML() []byte {
	return specYAML
}
