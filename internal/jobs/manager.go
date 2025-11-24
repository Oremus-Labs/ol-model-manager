package jobs

import (
	"context"
	"fmt"
	"log"
	"math"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

// Manager coordinates asynchronous background work (e.g., weight installs).
type Manager struct {
	store     *store.Store
	weights   weightStore
	hfToken   string
	pvcName   string
	modelRoot string
	events    eventPublisher
}

type weightStore interface {
	InstallFromHuggingFace(context.Context, weights.InstallOptions) (*weights.WeightInfo, error)
}

type eventPublisher interface {
	Publish(context.Context, events.Event) error
}

// Options configures the job manager.
type Options struct {
	Store              *store.Store
	Weights            weightStore
	HuggingFaceToken   string
	WeightsPVCName     string
	InferenceModelRoot string
	EventPublisher     eventPublisher
}

// New creates a job manager.
func New(opts Options) *Manager {
	return &Manager{
		store:     opts.Store,
		weights:   opts.Weights,
		hfToken:   opts.HuggingFaceToken,
		pvcName:   opts.WeightsPVCName,
		modelRoot: opts.InferenceModelRoot,
		events:    opts.EventPublisher,
	}
}

// InstallRequest describes a weight installation job.
type InstallRequest struct {
	ModelID   string
	Revision  string
	Target    string
	Files     []string
	Overwrite bool
}

// EnqueueWeightInstall schedules a weight install job.
func (m *Manager) EnqueueWeightInstall(req InstallRequest) (*store.Job, error) {
	if m.store == nil || m.weights == nil {
		return nil, fmt.Errorf("job manager not configured")
	}
	job := &store.Job{
		ID:   uuid.NewString(),
		Type: "weight_install",
		Payload: map[string]interface{}{
			"hfModelId": req.ModelID,
			"revision":  req.Revision,
			"target":    req.Target,
		},
		Status: store.JobPending,
	}
	if err := m.store.CreateJob(job); err != nil {
		return nil, err
	}

	go m.runInstall(job, req)
	return job, nil
}

func (m *Manager) runInstall(job *store.Job, req InstallRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	m.updateJob(job, store.JobRunning, 5, "queued", "Waiting for worker")
	m.updateJob(job, store.JobRunning, 15, "preparing", "Preparing cache directory")

	info, err := m.weights.InstallFromHuggingFace(ctx, weights.InstallOptions{
		ModelID:   req.ModelID,
		Revision:  req.Revision,
		Target:    req.Target,
		Files:     req.Files,
		Token:     m.hfToken,
		Overwrite: req.Overwrite,
		Progress: func(file string, completed, total int) {
			progress := 20
			if total > 0 {
				progress = 20 + int(math.Round(float64(completed)/float64(total)*70))
			}
			msg := "Downloading weights"
			if file != "" {
				msg = fmt.Sprintf("Downloading %s (%d/%d)", file, completed, total)
			}
			m.updateJob(job, store.JobRunning, progress, "downloading", msg)
		},
	})

	if err != nil {
		job.Error = err.Error()
		m.updateJob(job, store.JobFailed, job.Progress, "failed", err.Error())
		m.appendHistory(job.ID, "weight_install_failed", req.ModelID, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	job.Error = ""
	result := map[string]interface{}{
		"path":      info.Path,
		"name":      info.Name,
		"sizeBytes": info.SizeBytes,
	}
	if storageURI := m.storageURI(info.Name); storageURI != "" {
		result["storageUri"] = storageURI
	}
	if inferencePath := m.inferencePath(info.Name); inferencePath != "" {
		result["inferenceModelPath"] = inferencePath
	}
	job.Result = result
	m.updateJob(job, store.JobDone, 100, "completed", "Weights ready")

	m.appendHistory(job.ID, "weight_install_completed", req.ModelID, job.Result)
}

func (m *Manager) updateJob(job *store.Job, status store.JobStatus, progress int, stage, message string) {
	if status != "" {
		job.Status = status
	}
	if progress >= 0 {
		if progress > 100 {
			progress = 100
		}
		job.Progress = progress
	}
	if stage != "" {
		job.Stage = stage
	}
	if message != "" {
		job.Message = message
	}
	if err := m.store.UpdateJob(job); err != nil {
		log.Printf("jobs: failed to update job %s: %v", job.ID, err)
		return
	}
	m.emitJobEvent(job)
}

func (m *Manager) appendHistory(id, event, modelID string, meta map[string]interface{}) {
	if m.store == nil {
		return
	}
	_ = m.store.AppendHistory(&store.HistoryEntry{
		ID:       fmt.Sprintf("%s-%s", event, id),
		Event:    event,
		ModelID:  modelID,
		Metadata: meta,
	})
}

func (m *Manager) storageURI(name string) string {
	if m.pvcName == "" || name == "" {
		return ""
	}
	return fmt.Sprintf("pvc://%s/%s", m.pvcName, name)
}

func (m *Manager) inferencePath(name string) string {
	if m.modelRoot == "" || name == "" {
		return ""
	}
	return path.Join(m.modelRoot, name)
}

func (m *Manager) emitJobEvent(job *store.Job) {
	if m.events == nil || job == nil {
		return
	}
	payload := *job
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.events.Publish(ctx, events.Event{
		Type: fmt.Sprintf("job.%s", job.Status),
		Data: payload,
	}); err != nil {
		log.Printf("jobs: failed to publish event for job %s: %v", job.ID, err)
	}
}
