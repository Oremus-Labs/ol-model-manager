package validator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestValidatorPassesWhenResourcesExist(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "venus", Namespace: "ai"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hf-token", Namespace: "ai"}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "venus"},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{
					corev1.ResourceName("amd.com/gpu"): resource.MustParse("2"),
				},
			},
		},
	)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my-model"), 0o755); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}

	v, err := New(Options{
		Namespace:          "ai",
		KubernetesClient:   client,
		WeightsPVCName:     "venus",
		InferenceModelRoot: root,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	model := &catalog.Model{
		ID:         "test",
		StorageURI: "pvc://venus/my-model",
		Env: []catalog.EnvVar{
			{
				Name: "HUGGING_FACE_HUB_TOKEN",
				ValueFrom: &catalog.EnvVarSource{
					SecretKeyRef: &catalog.SecretKeySelector{Name: "hf-token", Key: "token"},
				},
			},
		},
		Resources: &catalog.Resources{Limits: map[string]string{"amd.com/gpu": "1"}},
	}

	res := v.Validate(context.Background(), nil, model)
	if !res.Valid {
		t.Fatalf("expected validation to pass, got errors: %+v", res)
	}
}

func TestValidatorFailsWhenSecretMissing(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "venus", Namespace: "ai"}},
	)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my-model"), 0o755); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}

	v, err := New(Options{
		Namespace:          "ai",
		KubernetesClient:   client,
		WeightsPVCName:     "venus",
		InferenceModelRoot: root,
	})
	if err != nil {
		t.Fatalf("failed to create validator: %v", err)
	}

	model := &catalog.Model{
		ID:         "test",
		StorageURI: "pvc://venus/my-model",
		Env: []catalog.EnvVar{
			{
				Name: "HUGGING_FACE_HUB_TOKEN",
				ValueFrom: &catalog.EnvVarSource{
					SecretKeyRef: &catalog.SecretKeySelector{Name: "missing", Key: "token"},
				},
			},
		},
	}

	res := v.Validate(context.Background(), nil, model)
	if res.Valid {
		t.Fatalf("expected validation to fail due to missing secret")
	}
}
