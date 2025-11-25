package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

const managedLabel = "model-manager.oremuslabs.app/managed-secret"

// ErrNotFound indicates the requested secret does not exist.
var ErrNotFound = errors.New("secret not found")

// Manager wraps interactions with Kubernetes Secrets for the Model Manager namespace.
type Manager struct {
	client    kubernetes.Interface
	namespace string
}

// Meta describes a managed secret without exposing its values.
type Meta struct {
	Name      string    `json:"name"`
	Keys      []string  `json:"keys"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// Record represents a secret including its key/value data.
type Record struct {
	Name      string            `json:"name"`
	Data      map[string]string `json:"data"`
	CreatedAt time.Time         `json:"createdAt,omitempty"`
	UpdatedAt time.Time         `json:"updatedAt,omitempty"`
}

// NewManager constructs a Manager.
func NewManager(client kubernetes.Interface, namespace string) *Manager {
	return &Manager{
		client:    client,
		namespace: namespace,
	}
}

func (m *Manager) secretsClient() corev1client.SecretInterface {
	return m.client.CoreV1().Secrets(m.namespace)
}

// List returns the metadata for all managed secrets.
func (m *Manager) List(ctx context.Context) ([]Meta, error) {
	selector := labels.SelectorFromSet(labels.Set{managedLabel: "true"})
	secrets, err := m.secretsClient().List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(secrets.Items))
	for _, sec := range secrets.Items {
		meta := Meta{
			Name: sec.Name,
		}
		if !sec.CreationTimestamp.IsZero() {
			meta.CreatedAt = sec.CreationTimestamp.Time
		}
		if sec.ManagedFields != nil {
			meta.UpdatedAt = latestManagedTime(sec.ManagedFields, sec.CreationTimestamp.Time)
		}
		for k := range sec.Data {
			meta.Keys = append(meta.Keys, k)
		}
		sort.Strings(meta.Keys)
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Get fetches the named secret (regardless of label) and returns its values.
func (m *Manager) Get(ctx context.Context, name string) (*Record, error) {
	sec, err := m.secretsClient().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, translateError(err)
	}
	return convertSecret(sec), nil
}

// Upsert creates or updates the named secret with the provided data map.
func (m *Manager) Upsert(ctx context.Context, name string, data map[string]string) (*Record, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("secret data is empty")
	}
	stringData := make(map[string]string, len(data))
	for k, v := range data {
		stringData[k] = v
	}
	client := m.secretsClient()
	existing, err := client.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			created, err := client.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: m.namespace,
					Labels: map[string]string{
						managedLabel: "true",
					},
				},
				StringData: stringData,
				Type:       corev1.SecretTypeOpaque,
			}, metav1.CreateOptions{})
			if err != nil {
				return nil, err
			}
			return convertSecret(created), nil
		}
		return nil, err
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[managedLabel] = "true"
	existing.StringData = stringData
	existing.Type = corev1.SecretTypeOpaque
	updated, err := client.Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	return convertSecret(updated), nil
}

// Delete removes the named secret.
func (m *Manager) Delete(ctx context.Context, name string) error {
	return translateError(m.secretsClient().Delete(ctx, name, metav1.DeleteOptions{}))
}

func convertSecret(sec *corev1.Secret) *Record {
	record := &Record{
		Name: sec.Name,
		Data: make(map[string]string, len(sec.Data)),
	}
	if !sec.CreationTimestamp.IsZero() {
		record.CreatedAt = sec.CreationTimestamp.Time
	}
	if sec.ManagedFields != nil {
		record.UpdatedAt = latestManagedTime(sec.ManagedFields, sec.CreationTimestamp.Time)
	}
	for k, v := range sec.Data {
		record.Data[k] = string(v)
	}
	return record
}

func latestManagedTime(fields []metav1.ManagedFieldsEntry, fallback time.Time) time.Time {
	var latest time.Time
	for _, entry := range fields {
		if entry.Time != nil && entry.Time.After(latest) {
			latest = entry.Time.Time
		}
	}
	if latest.IsZero() {
		return fallback
	}
	return latest
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}
