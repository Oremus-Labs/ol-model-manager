package store

import (
	"path/filepath"
	"testing"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
)

func TestStoreJobsAndHistory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "state.db")
	s, err := Open(dsn, "sqlite")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})

	job := &Job{ID: "job-1", Type: "weight_install"}
	if err := s.CreateJob(job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	job.Status = JobRunning
	if err := s.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	stored, err := s.GetJob("job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if stored.Status != JobRunning {
		t.Fatalf("expected status %s got %s", JobRunning, stored.Status)
	}

	jobs, err := s.ListJobs(5)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job got %d", len(jobs))
	}

	if err := s.AppendHistory(&HistoryEntry{
		Event:   "weight_install_completed",
		ModelID: "foo",
	}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	history, err := s.ListHistory(1)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 || history[0].Event != "weight_install_completed" {
		t.Fatalf("unexpected history payload: %+v", history)
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.db")

	s, err := Open(path, "sqlite")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
}

func TestCatalogSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state.db"), "sqlite")
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})

	models := []*catalog.Model{
		{ID: "foo", DisplayName: "Foo", HFModelID: "org/foo"},
		{ID: "bar", DisplayName: "Bar", HFModelID: "org/bar"},
	}
	if err := s.SaveCatalogSnapshot(models); err != nil {
		t.Fatalf("SaveCatalogSnapshot: %v", err)
	}

	loaded, updated, err := s.LoadCatalogSnapshot()
	if err != nil {
		t.Fatalf("LoadCatalogSnapshot: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 models, got %d", len(loaded))
	}
	if updated.IsZero() {
		t.Fatalf("expected non-zero timestamp")
	}
	if loaded[0].ID == loaded[1].ID {
		t.Fatalf("expected unique models in snapshot")
	}
}
