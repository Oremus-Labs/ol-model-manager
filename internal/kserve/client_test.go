package kserve

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
)

func TestBuildVLLMArgsIncludesExtraAndServedName(t *testing.T) {
	tp := 2
	gpuUtil := 0.5
	maxLen := 2048
	trust := true

	model := &catalog.Model{
		HFModelID:       "Repo/Model",
		ServedModelName: "Repo/Model",
		VLLM: &catalog.VLLMConfig{
			TensorParallelSize:   &tp,
			Dtype:                "bfloat16",
			GPUMemoryUtilization: &gpuUtil,
			MaxModelLen:          &maxLen,
			TrustRemoteCode:      &trust,
			ExtraArgs: []string{
				"--speculative-decoding",
				"eagle",
				"",
				" --served-model-name bad",
				"--custom-flag=1",
			},
		},
	}

	got := buildVLLMArgs(model)
	want := []string{
		"--tensor-parallel-size", "2",
		"--dtype", "bfloat16",
		"--gpu-memory-utilization", fmt.Sprintf("%f", gpuUtil),
		"--max-model-len", "2048",
		"--trust-remote-code",
		"--served-model-name", "Repo/Model",
		"--speculative-decoding",
		"eagle",
		"--custom-flag=1",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected args.\nwant: %#v\n got: %#v", want, got)
	}
}

func TestBuildVLLMArgsFallsBackToHFID(t *testing.T) {
	model := &catalog.Model{
		HFModelID: "Fallback/Model",
		VLLM:      &catalog.VLLMConfig{},
	}

	got := buildVLLMArgs(model)
	want := []string{
		"--served-model-name", "Fallback/Model",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected fallback served name.\nwant: %#v\n got: %#v", want, got)
	}
}
