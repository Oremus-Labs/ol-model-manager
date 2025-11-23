package validator

import (
	"context"
	"fmt"
	"strings"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (v *Validator) checkGPU(ctx context.Context, model *catalog.Model) CheckResult {
	resourceName, required := gpuRequirement(model)
	if required == 0 || resourceName == "" {
		return CheckResult{Name: "gpu-capacity", Status: StatusPass, Message: "no GPU requirement detected"}
	}

	if v.kube == nil {
		return CheckResult{Name: "gpu-capacity", Status: StatusWarn, Message: "kubernetes client not configured"}
	}

	nodes, err := v.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return CheckResult{Name: "gpu-capacity", Status: StatusWarn, Message: fmt.Sprintf("failed to list nodes: %v", err)}
	}

	for _, node := range nodes.Items {
		if !matchesNodeSelector(&node, model.NodeSelector) {
			continue
		}

		allocatable, ok := node.Status.Allocatable[corev1.ResourceName(resourceName)]
		if !ok {
			continue
		}

		if allocatable.Value() < required {
			continue
		}

		metadata := describeNodeGPU(&node, resourceName, v.gpuProfiles)
		metadata["capacity"] = allocatable.String()
		metadata["resource"] = resourceName

		msg := fmt.Sprintf("node %s satisfies %s requirement (>= %d)", node.Name, resourceName, required)
		return CheckResult{Name: "gpu-capacity", Status: StatusPass, Message: msg, Metadata: metadata}
	}

	return CheckResult{Name: "gpu-capacity", Status: StatusFail, Message: fmt.Sprintf("no nodes satisfy %s >= %d", resourceName, required)}
}

func gpuRequirement(model *catalog.Model) (string, int64) {
	if model == nil || model.Resources == nil {
		return "", 0
	}

	if name, value := findGPUResource(model.Resources.Limits); name != "" {
		return name, value
	}
	if name, value := findGPUResource(model.Resources.Requests); name != "" {
		return name, value
	}
	return "", 0
}

func findGPUResource(resources map[string]string) (string, int64) {
	for name, value := range resources {
		if !strings.Contains(strings.ToLower(name), "gpu") {
			continue
		}
		qty, err := resource.ParseQuantity(value)
		if err != nil {
			continue
		}
		if qty.Value() <= 0 {
			continue
		}
		return name, qty.Value()
	}
	return "", 0
}

func matchesNodeSelector(node *corev1.Node, selector map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	for key, value := range selector {
		if node.Labels[key] != value {
			return false
		}
	}
	return true
}

func describeNodeGPU(node *corev1.Node, resourceName string, profiles map[string]GPUProfile) map[string]string {
	meta := map[string]string{"node": node.Name}
	switch {
	case strings.Contains(resourceName, "nvidia"):
		if product, ok := node.Labels["nvidia.com/gpu.product"]; ok {
			meta["gpuProduct"] = product
			if profile, found := profiles[strings.ToLower(product)]; found && profile.MemoryGB > 0 {
				meta["gpuMemory"] = fmt.Sprintf("%dGi", profile.MemoryGB)
			}
		}
	case strings.Contains(resourceName, "amd"):
		if product, ok := node.Labels["amd.com/gpu.product"]; ok {
			meta["gpuProduct"] = product
			if profile, found := profiles[strings.ToLower(product)]; found && profile.MemoryGB > 0 {
				meta["gpuMemory"] = fmt.Sprintf("%dGi", profile.MemoryGB)
			}
		}
	}
	return meta
}
