package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/store"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

type fakeInstaller struct {
	info *weights.WeightInfo
	err  error
}

func (f *fakeInstaller) InstallFromHuggingFace(ctx context.Context, opts weights.InstallOptions) (*weights.WeightInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func TestManagerEnqueueWeightInstallSuccess(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	m := New(s, &fakeInstaller{
		info: &weights.WeightInfo{
			Name:      "qwen2.5-0.5b",
			Path:      "/mnt/models/qwen2.5-0.5b",
			SizeBytes: 123,
		},
	}, "token")

	job, err := m.EnqueueWeightInstall(InstallRequest{
		ModelID: "Qwen/Qwen2.5-0.5B",
		Files:   []string{"config.json"},
	})
	if err != nil {
		t.Fatalf("EnqueueWeightInstall: %v", err)
	}

	waitForJobStatus(t, s, job.ID, store.JobDone)

	waitForHistoryEvent(t, s, "weight_install_completed")
}

func TestManagerEnqueueWeightInstallFailure(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	m := New(s, &fakeInstaller{
		err: errors.New("boom"),
	}, "token")

	job, err := m.EnqueueWeightInstall(InstallRequest{
		ModelID: "Qwen/Qwen2.5-0.5B",
		Files:   []string{"config.json"},
	})
	if err != nil {
		t.Fatalf("EnqueueWeightInstall: %v", err)
	}

	waitForJobStatus(t, s, job.ID, store.JobFailed)

	waitForHistoryEvent(t, s, "weight_install_failed")
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "state.db")
	s, err := store.Open(dsn, "sqlite")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func waitForJobStatus(t *testing.T, s *store.Store, id string, status store.JobStatus) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for job %s to reach %s", id, status)
		case <-ticker.C:
			job, err := s.GetJob(id)
			if err != nil {
				continue
			}
			if job.Status == status {
				return
			}
		}
	}
}

func waitForHistoryEvent(t *testing.T, s *store.Store, event string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for history event %s", event)
		case <-ticker.C:
			entries, err := s.ListHistory(10)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.Event == event {
					return
				}
			}
		}
	}
}
