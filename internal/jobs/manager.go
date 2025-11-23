package jobs

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

// Manager coordinates asynchronous background work (e.g., weight installs).
type Manager struct {
	store   *store.Store
	weights weightStore
	hfToken string
}

type weightStore interface {
	InstallFromHuggingFace(context.Context, weights.InstallOptions) (*weights.WeightInfo, error)
}

// New creates a job manager.
func New(s *store.Store, w weightStore, hfToken string) *Manager {
	return &Manager{
		store:   s,
		weights: w,
		hfToken: hfToken,
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

	job.Status = store.JobRunning
	if err := m.store.UpdateJob(job); err != nil {
		log.Printf("jobs: failed to update job %s: %v", job.ID, err)
	}

	info, err := m.weights.InstallFromHuggingFace(ctx, weights.InstallOptions{
		ModelID:   req.ModelID,
		Revision:  req.Revision,
		Target:    req.Target,
		Files:     req.Files,
		Token:     m.hfToken,
		Overwrite: req.Overwrite,
	})

	if err != nil {
		job.Status = store.JobFailed
		job.Error = err.Error()
		_ = m.store.UpdateJob(job)
		m.appendHistory(job.ID, "weight_install_failed", req.ModelID, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	job.Status = store.JobDone
	job.Result = map[string]interface{}{
		"path":      info.Path,
		"name":      info.Name,
		"sizeBytes": info.SizeBytes,
	}
	job.Error = ""
	if err := m.store.UpdateJob(job); err != nil {
		log.Printf("jobs: failed to update completed job %s: %v", job.ID, err)
	}

	m.appendHistory(job.ID, "weight_install_completed", req.ModelID, job.Result)
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
