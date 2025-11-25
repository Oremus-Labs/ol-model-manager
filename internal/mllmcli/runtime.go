package mllmcli

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RuntimeStatus mirrors the /models/status payload.
type RuntimeStatus struct {
	InferenceService *InferenceServiceStatus `json:"inferenceService"`
	Deployments      []DeploymentStatus      `json:"deployments"`
	Pods             []PodStatus             `json:"pods"`
	GPUAllocations   map[string]string       `json:"gpuAllocations"`
	UpdatedAt        time.Time               `json:"updatedAt"`
}

type InferenceServiceStatus struct {
	Name       string      `json:"name"`
	URL        string      `json:"url"`
	Ready      string      `json:"ready"`
	Conditions []Condition `json:"conditions"`
}

type DeploymentStatus struct {
	Name              string      `json:"name"`
	ReadyReplicas     int32       `json:"readyReplicas"`
	AvailableReplicas int32       `json:"availableReplicas"`
	Replicas          int32       `json:"replicas"`
	UpdatedReplicas   int32       `json:"updatedReplicas"`
	Conditions        []Condition `json:"conditions"`
}

type PodStatus struct {
	Name        string            `json:"name"`
	Phase       string            `json:"phase"`
	Reason      string            `json:"reason"`
	Message     string            `json:"message"`
	Conditions  []Condition       `json:"conditions"`
	GPURequests map[string]string `json:"gpuRequests"`
}

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

func fetchRuntimeStatus(client *Client) (*RuntimeStatus, error) {
	var status RuntimeStatus
	if err := client.GetJSON("/models/status", &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func detectGPUContention(status *RuntimeStatus) (string, bool) {
	if status == nil {
		return "", false
	}
	for _, pod := range status.Pods {
		if strings.EqualFold(pod.Phase, "Pending") {
			for _, cond := range pod.Conditions {
				if cond.Reason == "Unschedulable" &&
					strings.Contains(strings.ToLower(cond.Message), "gpu") {
					return fmt.Sprintf("%s: %s", pod.Name, cond.Message), true
				}
			}
			if pod.Reason == "Unschedulable" && strings.Contains(strings.ToLower(pod.Message), "gpu") {
				return fmt.Sprintf("%s: %s", pod.Name, pod.Message), true
			}
		}
	}
	return "", false
}

func waitForGPUClear(ctx context.Context, client *Client) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		status, err := fetchRuntimeStatus(client)
		if err == nil {
			if _, busy := detectGPUContention(status); !busy {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("context cancelled")
		case <-ticker.C:
		}
	}
}

func fetchActiveModelID(client *Client) (string, error) {
	var resp struct {
		Status           string `json:"status"`
		InferenceService *struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"inferenceservice"`
	}
	if err := client.GetJSON("/active", &resp); err != nil {
		return "", err
	}
	if resp.Status != "active" || resp.InferenceService == nil {
		return "", nil
	}
	if resp.InferenceService.Metadata.Annotations == nil {
		return "", nil
	}
	return resp.InferenceService.Metadata.Annotations["model-manager/model-id"], nil
}
