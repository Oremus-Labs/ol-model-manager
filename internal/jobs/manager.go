package jobs

import (
	"context"
	"fmt"
	"log"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/oremus-labs/ol-model-manager/internal/events"
	"github.com/oremus-labs/ol-model-manager/internal/logutil"
	"github.com/oremus-labs/ol-model-manager/internal/metrics"
	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

// Manager coordinates asynchronous background work (e.g., weight installs).
type Manager struct {
	store       *store.Store
	weights     weightStore
	hfToken     string
	pvcName     string
	modelRoot   string
	events      eventPublisher
	maxAttempts int
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
	MaxJobAttempts     int
}

// New creates a job manager.
func New(opts Options) *Manager {
	if opts.MaxJobAttempts <= 0 {
		opts.MaxJobAttempts = 3
	}
	return &Manager{
		store:       opts.Store,
		weights:     opts.Weights,
		hfToken:     opts.HuggingFaceToken,
		pvcName:     opts.WeightsPVCName,
		modelRoot:   opts.InferenceModelRoot,
		events:      opts.EventPublisher,
		maxAttempts: opts.MaxJobAttempts,
	}
}

// InstallRequest describes a weight installation job.
type InstallRequest struct {
	ModelID   string   `json:"modelId"`
	Revision  string   `json:"revision,omitempty"`
	Target    string   `json:"target"`
	Files     []string `json:"files,omitempty"`
	Overwrite bool     `json:"overwrite"`
}

// EnqueueWeightInstall schedules a weight install job asynchronously.
func (m *Manager) EnqueueWeightInstall(req InstallRequest) (*store.Job, error) {
	job, err := m.CreateJob(req)
	if err != nil {
		return nil, err
	}
	m.ExecuteJob(job, req)
	return job, nil
}

// CreateJob persists a new pending job without executing it.
func (m *Manager) CreateJob(req InstallRequest) (*store.Job, error) {
	if m.store == nil || m.weights == nil {
		return nil, fmt.Errorf("job manager not configured")
	}
	payload := map[string]interface{}{
		"hfModelId": req.ModelID,
		"revision":  req.Revision,
		"target":    req.Target,
		"overwrite": req.Overwrite,
	}
	if len(req.Files) > 0 {
		payload["files"] = req.Files
	}
	job := &store.Job{
		ID:          uuid.NewString(),
		Type:        "weight_install",
		Payload:     payload,
		Status:      store.JobPending,
		MaxAttempts: m.maxAttempts,
	}
	if err := m.store.CreateJob(job); err != nil {
		return nil, err
	}
	return job, nil
}

// ExecuteJob kicks off the job asynchronously.
func (m *Manager) ExecuteJob(job *store.Job, req InstallRequest) {
	go m.processJob(job, req)
}

// ProcessJob executes the job synchronously (used by workers).
func (m *Manager) ProcessJob(job *store.Job, req InstallRequest) {
	m.processJob(job, req)
}

// GetJob loads a job by ID.
func (m *Manager) GetJob(id string) (*store.Job, error) {
	if m.store == nil {
		return nil, fmt.Errorf("job manager not configured")
	}
	return m.store.GetJob(id)
}

func (m *Manager) processJob(job *store.Job, req InstallRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	start := time.Now()
	finalStatus := "failed"
	defer func() {
		metrics.ObserveJobCompletion(job.Type, finalStatus, time.Since(start))
	}()

	job.Attempt++
	m.logJob(job, "info", "queued", fmt.Sprintf("Attempt %d/%d scheduled", job.Attempt, job.MaxAttempts))
	m.updateJob(job, store.JobRunning, 5, "queued", fmt.Sprintf("Attempt %d/%d queued", job.Attempt, job.MaxAttempts))
	m.logJob(job, "info", "preparing", "Preparing cache directory")
	m.updateJob(job, store.JobRunning, 15, "preparing", "Preparing cache directory")

	m.updateJob(job, store.JobRunning, 25, "downloading", "Downloading weights via Hugging Face CLI (this may take a while)")
	info, err := m.weights.InstallFromHuggingFace(ctx, weights.InstallOptions{
		ModelID:   req.ModelID,
		Revision:  req.Revision,
		Target:    req.Target,
		Files:     req.Files,
		Token:     m.hfToken,
		Overwrite: req.Overwrite,
	})

	if err != nil {
		job.Error = err.Error()
		m.updateJob(job, store.JobFailed, job.Progress, "failed", err.Error())
		m.appendHistory(job.ID, "weight_install_failed", req.ModelID, map[string]interface{}{
			"error": err.Error(),
		})
		m.logJob(job, "error", "failed", err.Error())
		logutil.Error("weights_install_failed", err, map[string]interface{}{
			"jobId":   job.ID,
			"modelId": req.ModelID,
			"target":  req.Target,
		})
		return
	}
	finalStatus = "success"

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
	m.logJob(job, "info", "completed", "Weights ready")

	m.appendHistory(job.ID, "weight_install_completed", req.ModelID, job.Result)
	logutil.Info("weights_install_completed", map[string]interface{}{
		"jobId":    job.ID,
		"modelId":  req.ModelID,
		"target":   req.Target,
		"duration": time.Since(start).String(),
	})
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
	timestamp := job.UpdatedAt
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.events.Publish(ctx, events.Event{
		ID:        job.ID,
		Type:      fmt.Sprintf("job.%s", job.Status),
		Timestamp: timestamp,
		Data:      payload,
	}); err != nil {
		log.Printf("jobs: failed to publish event for job %s: %v", job.ID, err)
	}
}

func (m *Manager) logJob(job *store.Job, level, stage, message string) {
	if m.store == nil || job == nil {
		return
	}
	entry := store.JobLogEntry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Stage:     stage,
		Message:   message,
	}
	if err := m.store.AppendJobLog(job.ID, entry); err != nil {
		log.Printf("jobs: failed to append log for job %s: %v", job.ID, err)
		return
	}
	m.emitJobLogEvent(job.ID, entry)
}

func (m *Manager) emitJobLogEvent(jobID string, entry store.JobLogEntry) {
	if m.events == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.events.Publish(ctx, events.Event{
		ID:        fmt.Sprintf("%s-log-%d", jobID, entry.Timestamp.UnixNano()),
		Type:      "job.log",
		Timestamp: entry.Timestamp,
		Data: map[string]interface{}{
			"jobId": jobID,
			"log":   entry,
		},
	}); err != nil {
		log.Printf("jobs: failed to publish log event for job %s: %v", jobID, err)
	}
}
