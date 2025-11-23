package weights

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallFromHuggingFaceDownloadsFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("/Qwen/Qwen2.5-0.5B/resolve/main/model.safetensors", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tiny-model"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	manager := New(tmpDir, WithHFBaseURL(srv.URL), WithHTTPClient(srv.Client()))

	info, err := manager.InstallFromHuggingFace(context.Background(), InstallOptions{
		ModelID: "Qwen/Qwen2.5-0.5B",
		Files:   []string{"model.safetensors"},
		Target:  "qwen2.5-0.5b",
	})
	if err != nil {
		t.Fatalf("InstallFromHuggingFace() error = %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "qwen2.5-0.5b", "model.safetensors")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("failed to read downloaded file: %v", err)
	}

	if string(data) != "tiny-model" {
		t.Fatalf("unexpected file contents %q", string(data))
	}

	if info.Name != "qwen2.5-0.5b" {
		t.Fatalf("expected info.Name qwen2.5-0.5b, got %s", info.Name)
	}

	if info.SizeBytes != int64(len("tiny-model")) {
		t.Fatalf("expected size %d, got %d", len("tiny-model"), info.SizeBytes)
	}
}

func TestListSkipsReservedAndHiddenDirs(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	dirs := []struct {
		name string
		file string
	}{
		{"qwen2.5-0.5b", "model.safetensors"},
		{".hf-cache", "cache.bin"},
		{"modules", "readme.txt"},
	}

	for _, d := range dirs {
		dirPath := filepath.Join(tmpDir, d.name)
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dirPath, err)
		}
		if err := os.WriteFile(filepath.Join(dirPath, d.file), []byte("data"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	manager := New(tmpDir)

	list, err := manager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d: %+v", len(list), list)
	}

	if list[0].Name != "qwen2.5-0.5b" {
		t.Fatalf("unexpected entry %+v", list[0])
	}

	if _, err := manager.Get(".hf-cache"); err == nil {
		t.Fatalf("expected error when getting reserved directory")
	}
}
