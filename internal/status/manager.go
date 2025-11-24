package status

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/events"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// RuntimeStatus captures the live state of the active InferenceService runtime.
type RuntimeStatus struct {
	InferenceService *InferenceServiceStatus `json:"inferenceService,omitempty"`
	Deployments      []DeploymentStatus      `json:"deployments,omitempty"`
	Pods             []PodStatus             `json:"pods,omitempty"`
	UpdatedAt        time.Time               `json:"updatedAt"`
}

// InferenceServiceStatus summarizes kserve status.
type InferenceServiceStatus struct {
	Name       string      `json:"name"`
	URL        string      `json:"url,omitempty"`
	Ready      string      `json:"ready"`
	Conditions []Condition `json:"conditions,omitempty"`
}

// Condition mirrors k8s condition details.
type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime,omitempty"`
}

// DeploymentStatus describes a deployment.
type DeploymentStatus struct {
	Name              string `json:"name"`
	ReadyReplicas     int32  `json:"readyReplicas"`
	AvailableReplicas int32  `json:"availableReplicas"`
	Replicas          int32  `json:"replicas"`
	UpdatedReplicas   int32  `json:"updatedReplicas"`
}

// PodStatus captures pod details.
type PodStatus struct {
	Name            string `json:"name"`
	Phase           string `json:"phase"`
	ReadyContainers int32  `json:"readyContainers"`
	TotalContainers int32  `json:"totalContainers"`
	Restarts        int32  `json:"restarts"`
	HostIP          string `json:"hostIP,omitempty"`
	PodIP           string `json:"podIP,omitempty"`
	NodeName        string `json:"nodeName,omitempty"`
}

// Provider exposes runtime status snapshots.
type Provider interface {
	CurrentStatus() RuntimeStatus
}

// Manager wires informers and maintains cached status.
type Manager struct {
	namespace string
	isvcName  string

	dynClient  dynamic.Interface
	kubeClient kubernetes.Interface
	gvr        schema.GroupVersionResource

	eventBus eventsPublisher

	mu          sync.RWMutex
	isvcStatus  *InferenceServiceStatus
	deployments map[string]DeploymentStatus
	pods        map[string]PodStatus
	lastUpdate  time.Time
}

type eventsPublisher interface {
	Publish(context.Context, events.Event) error
}

// NewManager constructs a manager for the active runtime.
func NewManager(cfg *rest.Config, namespace, isvcName string, bus eventsPublisher) (*Manager, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	gvr := schema.GroupVersionResource{
		Group:    "serving.kserve.io",
		Version:  "v1beta1",
		Resource: "inferenceservices",
	}
	return &Manager{
		namespace:   namespace,
		isvcName:    isvcName,
		dynClient:   dyn,
		kubeClient:  kubeClient,
		gvr:         gvr,
		eventBus:    bus,
		deployments: make(map[string]DeploymentStatus),
		pods:        make(map[string]PodStatus),
	}, nil
}

// Run starts informers until context cancellation.
func (m *Manager) Run(ctx context.Context) error {
	dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(m.dynClient, 0, m.namespace, nil)
	isvcInformer := dynFactory.ForResource(m.gvr).Informer()
	isvcInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    m.onISVC,
		UpdateFunc: func(oldObj, newObj interface{}) { m.onISVC(newObj) },
		DeleteFunc: m.onISVCDelete,
	})

	sharedFactory := informers.NewSharedInformerFactoryWithOptions(m.kubeClient, 0, informers.WithNamespace(m.namespace))
	depInformer := sharedFactory.Apps().V1().Deployments().Informer()
	depInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    m.onDeployment,
		UpdateFunc: func(oldObj, newObj interface{}) { m.onDeployment(newObj) },
		DeleteFunc: m.onDeploymentDelete,
	})
	podInformer := sharedFactory.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    m.onPod,
		UpdateFunc: func(oldObj, newObj interface{}) { m.onPod(newObj) },
		DeleteFunc: m.onPodDelete,
	})

	dynFactory.Start(ctx.Done())
	sharedFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), isvcInformer.HasSynced, depInformer.HasSynced, podInformer.HasSynced) {
		return fmt.Errorf("status manager cache sync failed")
	}

	<-ctx.Done()
	log.Println("status manager stopped")
	return ctx.Err()
}

// CurrentStatus returns a snapshot of the runtime state.
func (m *Manager) CurrentStatus() RuntimeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := RuntimeStatus{
		UpdatedAt: m.lastUpdate,
	}
	if m.isvcStatus != nil {
		copyISVC := *m.isvcStatus
		status.InferenceService = &copyISVC
	}
	if len(m.deployments) > 0 {
		deps := make([]DeploymentStatus, 0, len(m.deployments))
		for _, d := range m.deployments {
			deps = append(deps, d)
		}
		status.Deployments = deps
	}
	if len(m.pods) > 0 {
		pods := make([]PodStatus, 0, len(m.pods))
		for _, p := range m.pods {
			pods = append(pods, p)
		}
		status.Pods = pods
	}
	return status
}

func (m *Manager) onISVC(obj interface{}) {
	unstr, ok := toUnstructured(obj)
	if !ok {
		return
	}
	if unstr.GetName() != m.isvcName {
		return
	}
	status := parseInferenceService(unstr)
	m.mu.Lock()
	if status == nil {
		m.isvcStatus = nil
	} else {
		m.isvcStatus = status
	}
	m.lastUpdate = time.Now().UTC()
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	m.publish(snapshot)
}

func (m *Manager) onISVCDelete(obj interface{}) {
	unstr, ok := toUnstructured(obj)
	if !ok {
		return
	}
	if unstr.GetName() != m.isvcName {
		return
	}
	m.mu.Lock()
	m.isvcStatus = nil
	m.lastUpdate = time.Now().UTC()
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	m.publish(snapshot)
}

func (m *Manager) onDeployment(obj interface{}) {
	dep, ok := obj.(*appsv1.Deployment)
	if !ok {
		return
	}
	if dep.Labels["serving.kserve.io/inferenceservice"] != m.isvcName {
		return
	}
	m.mu.Lock()
	m.deployments[dep.Name] = DeploymentStatus{
		Name:              dep.Name,
		ReadyReplicas:     dep.Status.ReadyReplicas,
		AvailableReplicas: dep.Status.AvailableReplicas,
		Replicas:          dep.Status.Replicas,
		UpdatedReplicas:   dep.Status.UpdatedReplicas,
	}
	m.lastUpdate = time.Now().UTC()
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	m.publish(snapshot)
}

func (m *Manager) onDeploymentDelete(obj interface{}) {
	dep, ok := obj.(*appsv1.Deployment)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if ok {
			dep, _ = tombstone.Obj.(*appsv1.Deployment)
		}
	}
	if dep == nil || dep.Labels["serving.kserve.io/inferenceservice"] != m.isvcName {
		return
	}
	m.mu.Lock()
	delete(m.deployments, dep.Name)
	m.lastUpdate = time.Now().UTC()
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	m.publish(snapshot)
}

func (m *Manager) onPod(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if pod.Labels["serving.kserve.io/inferenceservice"] != m.isvcName {
		return
	}
	ready := int32(0)
	total := int32(len(pod.Status.ContainerStatuses))
	restarts := int32(0)
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
	}
	m.mu.Lock()
	m.pods[pod.Name] = PodStatus{
		Name:            pod.Name,
		Phase:           string(pod.Status.Phase),
		ReadyContainers: ready,
		TotalContainers: total,
		Restarts:        restarts,
		HostIP:          pod.Status.HostIP,
		PodIP:           pod.Status.PodIP,
		NodeName:        pod.Spec.NodeName,
	}
	m.lastUpdate = time.Now().UTC()
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	m.publish(snapshot)
}

func (m *Manager) onPodDelete(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if ok {
			pod, _ = tombstone.Obj.(*corev1.Pod)
		}
	}
	if pod == nil || pod.Labels["serving.kserve.io/inferenceservice"] != m.isvcName {
		return
	}
	m.mu.Lock()
	delete(m.pods, pod.Name)
	m.lastUpdate = time.Now().UTC()
	snapshot := m.snapshotLocked()
	m.mu.Unlock()
	m.publish(snapshot)
}

func (m *Manager) snapshotLocked() RuntimeStatus {
	status := RuntimeStatus{UpdatedAt: m.lastUpdate}
	if m.isvcStatus != nil {
		copyISVC := *m.isvcStatus
		status.InferenceService = &copyISVC
	}
	if len(m.deployments) > 0 {
		deps := make([]DeploymentStatus, 0, len(m.deployments))
		for _, d := range m.deployments {
			deps = append(deps, d)
		}
		status.Deployments = deps
	}
	if len(m.pods) > 0 {
		pods := make([]PodStatus, 0, len(m.pods))
		for _, p := range m.pods {
			pods = append(pods, p)
		}
		status.Pods = pods
	}
	return status
}

func (m *Manager) publish(status RuntimeStatus) {
	if m.eventBus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.eventBus.Publish(ctx, events.Event{
		Type:      "model.status.updated",
		Timestamp: status.UpdatedAt,
		Data:      status,
	}); err != nil {
		log.Printf("status manager: failed to publish event: %v", err)
	}
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, bool) {
	switch t := obj.(type) {
	case *unstructured.Unstructured:
		return t, true
	case cache.DeletedFinalStateUnknown:
		if unstr, ok := t.Obj.(*unstructured.Unstructured); ok {
			return unstr, true
		}
	}
	return nil, false
}

func parseInferenceService(obj *unstructured.Unstructured) *InferenceServiceStatus {
	statusMap, found, err := unstructured.NestedMap(obj.Object, "status")
	if err != nil || !found {
		return &InferenceServiceStatus{Name: obj.GetName(), Ready: "Unknown"}
	}
	url, _, _ := unstructured.NestedString(statusMap, "url")
	conds, _, _ := unstructured.NestedSlice(statusMap, "conditions")
	conditionList := make([]Condition, 0, len(conds))
	ready := "Unknown"
	for _, raw := range conds {
		condMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		cond := Condition{
			Type:    toString(condMap["type"]),
			Status:  toString(condMap["status"]),
			Reason:  toString(condMap["reason"]),
			Message: toString(condMap["message"]),
		}
		if ts := toString(condMap["lastTransitionTime"]); ts != "" {
			if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
				cond.LastTransitionTime = parsed
			}
		}
		if cond.Type == "Ready" {
			ready = cond.Status
		}
		conditionList = append(conditionList, cond)
	}
	return &InferenceServiceStatus{
		Name:       obj.GetName(),
		URL:        url,
		Ready:      ready,
		Conditions: conditionList,
	}
}

func toString(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}
