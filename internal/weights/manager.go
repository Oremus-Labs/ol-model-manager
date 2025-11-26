// Package weights manages model weights on the persistent volume.
package weights

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Manager handles model weight operations on the Venus PVC.
type Manager struct {
	storagePath   string
	reservedNames map[string]struct{}
	hfDownloader  func(context.Context, InstallOptions, string, string) error
}

// Option configures a Manager at construction.
type Option func(*Manager)

// WithHFDownloader overrides the default Hugging Face CLI download runner (useful for tests).
func WithHFDownloader(fn func(context.Context, InstallOptions, string, string) error) Option {
	return func(m *Manager) {
		if fn != nil {
			m.hfDownloader = fn
		}
	}
}

// WeightInfo contains information about cached model weights.
type WeightInfo struct {
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"sizeBytes"`
	SizeHuman    string    `json:"sizeHuman"`
	ModifiedTime time.Time `json:"modifiedTime"`
	FileCount    int       `json:"fileCount"`
	HFModelID    string    `json:"hfModelId,omitempty"`
	Revision     string    `json:"revision,omitempty"`
	InstalledAt  time.Time `json:"installedAt,omitempty"`
}

// StorageStats provides overall storage statistics.
type StorageStats struct {
	TotalBytes     int64        `json:"totalBytes"`
	TotalHuman     string       `json:"totalHuman"`
	UsedBytes      int64        `json:"usedBytes"`
	UsedHuman      string       `json:"usedHuman"`
	AvailableBytes int64        `json:"availableBytes"`
	AvailableHuman string       `json:"availableHuman"`
	ModelCount     int          `json:"modelCount"`
	Models         []WeightInfo `json:"models"`
}

const metadataFilename = ".model-manager"

type weightMetadata struct {
	ModelID     string    `json:"modelId"`
	Revision    string    `json:"revision,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

// InstallOptions controls how weights are installed for a model.
type InstallOptions struct {
	ModelID       string
	Revision      string
	Target        string
	Files         []string
	Token         string
	Overwrite     bool
	Progress      func(file string, completed, total int)
	ProgressBytes func(file string, fileIndex, totalFiles int, downloaded, totalBytes int64)
}

// New creates a new weight manager.
func New(storagePath string, opts ...Option) *Manager {
	m := &Manager{
		storagePath: storagePath,
		reservedNames: map[string]struct{}{
			".hf-cache":  {},
			"modules":    {},
			"lost+found": {},
		},
		hfDownloader: runHFDownload,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// List returns all cached model weights.
func (m *Manager) List() ([]WeightInfo, error) {
	roots, err := m.installRoots()
	if err != nil {
		return nil, err
	}
	weights := make([]WeightInfo, 0, len(roots))
	for _, rel := range roots {
		modelPath := filepath.Join(m.storagePath, toFilesystemPath(rel))
		info, err := m.getWeightInfo(modelPath, rel)
		if err != nil {
			// Log but continue with other models
			continue
		}

		weights = append(weights, *info)
	}

	// Sort by size descending
	sort.Slice(weights, func(i, j int) bool {
		return weights[i].SizeBytes > weights[j].SizeBytes
	})

	return weights, nil
}

func (m *Manager) installRoots() ([]string, error) {
	var roots []string

	err := filepath.WalkDir(m.storagePath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || d.Name() != metadataFilename {
			return nil
		}
		rel, err := filepath.Rel(m.storagePath, filepath.Dir(p))
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "" {
			return nil
		}
		roots = append(roots, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	if len(roots) == 0 {
		entries, err := os.ReadDir(m.storagePath)
		if err != nil {
			if os.IsNotExist(err) {
				return []string{}, nil
			}
			return nil, fmt.Errorf("failed to read storage directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() && !m.isReserved(entry.Name()) {
				roots = append(roots, entry.Name())
			}
		}
	}

	return roots, nil
}

// Get returns information about a specific model's weights.
func (m *Manager) Get(modelName string) (*WeightInfo, error) {
	rel, err := normalizeRelativePath(modelName)
	if err != nil {
		return nil, fmt.Errorf("invalid model path: %w", err)
	}
	if m.isReserved(rel) {
		return nil, fmt.Errorf("model weights not found: %s", rel)
	}
	modelPath := filepath.Join(m.storagePath, toFilesystemPath(rel))

	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("model weights not found: %s", rel)
	}

	return m.getWeightInfo(modelPath, rel)
}

// Delete removes a model's weights from storage.
func (m *Manager) Delete(modelName string) error {
	rel, err := normalizeRelativePath(modelName)
	if err != nil {
		return fmt.Errorf("invalid model path: %w", err)
	}
	if m.isReserved(rel) {
		return fmt.Errorf("model weights not found: %s", rel)
	}
	modelPath := filepath.Join(m.storagePath, toFilesystemPath(rel))

	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return fmt.Errorf("model weights not found: %s", rel)
	}

	// Security check: ensure path is within storage directory
	absStoragePath, err := filepath.Abs(m.storagePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute storage path: %w", err)
	}

	absModelPath, err := filepath.Abs(modelPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute model path: %w", err)
	}

	if !strings.HasPrefix(absModelPath, absStoragePath) {
		return fmt.Errorf("invalid model path: path traversal detected")
	}

	var modelMeta *weightMetadata
	if meta, err := readMetadata(modelPath); err == nil {
		modelMeta = meta
	}

	if err := os.RemoveAll(modelPath); err != nil {
		return fmt.Errorf("failed to delete model weights: %w", err)
	}

	m.cleanupEmptyParents(modelPath)
	if modelMeta != nil {
		m.purgeHFCache(modelMeta.ModelID)
	}

	return nil
}

// PruneOlderThan deletes cached weights that have not been modified within the provided age.
func (m *Manager) PruneOlderThan(maxAge time.Duration) ([]string, error) {
	if maxAge <= 0 {
		return nil, nil
	}
	cutoff := time.Now().Add(-maxAge)
	weights, err := m.List()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, info := range weights {
		if info.ModifiedTime.After(cutoff) {
			continue
		}
		if err := m.Delete(info.Name); err != nil {
			log.Printf("weights: failed to prune %s: %v", info.Name, err)
			continue
		}
		removed = append(removed, info.Name)
	}
	return removed, nil
}

// GetStats returns overall storage statistics.
func (m *Manager) GetStats() (*StorageStats, error) {
	weights, err := m.List()
	if err != nil {
		return nil, err
	}

	var totalUsed int64
	for _, w := range weights {
		totalUsed += w.SizeBytes
	}

	// Get filesystem stats
	var stat filesystemStats
	if err := readFilesystemStats(m.storagePath, &stat); err != nil {
		// If we can't get filesystem stats, just use calculated values
		return &StorageStats{
			TotalBytes:     0,
			TotalHuman:     "unknown",
			UsedBytes:      totalUsed,
			UsedHuman:      formatBytes(totalUsed),
			AvailableBytes: 0,
			AvailableHuman: "unknown",
			ModelCount:     len(weights),
			Models:         weights,
		}, nil
	}

	totalBytes := int64(stat.Blocks) * int64(stat.Bsize)
	availBytes := int64(stat.Bavail) * int64(stat.Bsize)

	return &StorageStats{
		TotalBytes:     totalBytes,
		TotalHuman:     formatBytes(totalBytes),
		UsedBytes:      totalUsed,
		UsedHuman:      formatBytes(totalUsed),
		AvailableBytes: availBytes,
		AvailableHuman: formatBytes(availBytes),
		ModelCount:     len(weights),
		Models:         weights,
	}, nil
}

// InstallFromHuggingFace downloads weights for a HuggingFace model into storage.
func (m *Manager) InstallFromHuggingFace(ctx context.Context, opts InstallOptions) (*WeightInfo, error) {
	if opts.ModelID == "" {
		return nil, fmt.Errorf("model ID is required")
	}

	target, err := CanonicalTarget(opts.ModelID, opts.Target)
	if err != nil {
		return nil, err
	}

	if m.isReserved(target) {
		return nil, fmt.Errorf("cannot install weights into reserved path: %s", target)
	}

	revision := opts.Revision
	if revision == "" {
		revision = "main"
	}

	destPath := filepath.Join(m.storagePath, toFilesystemPath(target))
	if _, err := os.Stat(destPath); err == nil {
		if !opts.Overwrite {
			return nil, fmt.Errorf("weights already exist for %s", target)
		}
		if err := os.RemoveAll(destPath); err != nil {
			return nil, fmt.Errorf("failed to remove existing weights: %w", err)
		}
	}

	tmpPath := destPath + ".tmp"
	_ = os.RemoveAll(tmpPath)

	if err := os.MkdirAll(tmpPath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	if err := m.hfDownloader(ctx, opts, tmpPath, revision); err != nil {
		_ = os.RemoveAll(tmpPath)
		return nil, err
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.RemoveAll(tmpPath)
		return nil, fmt.Errorf("failed to finalize weights: %w", err)
	}

	meta := weightMetadata{
		ModelID:     opts.ModelID,
		Revision:    revision,
		InstalledAt: time.Now().UTC(),
	}
	if err := writeMetadata(destPath, meta); err != nil {
		log.Printf("weights: failed to write metadata for %s: %v", target, err)
	}

	info, err := m.getWeightInfo(destPath, target)
	if err != nil {
		return nil, err
	}

	return info, nil
}

func (m *Manager) isReserved(name string) bool {
	if name == "" {
		return true
	}
	segments := strings.Split(strings.Trim(name, "/"), "/")
	for _, segment := range segments {
		if segment == "" {
			return true
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
		if _, ok := m.reservedNames[segment]; ok {
			return true
		}
	}
	return false
}

func (m *Manager) getWeightInfo(path, name string) (*WeightInfo, error) {
	var totalSize int64
	var fileCount int
	var modTime time.Time

	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if info.Name() == metadataFilename {
				return nil
			}
			totalSize += info.Size()
			fileCount++
			if info.ModTime().After(modTime) {
				modTime = info.ModTime()
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	info := &WeightInfo{
		Path:         path,
		Name:         name,
		SizeBytes:    totalSize,
		SizeHuman:    formatBytes(totalSize),
		ModifiedTime: modTime,
		FileCount:    fileCount,
	}

	if meta, err := readMetadata(path); err == nil && meta != nil {
		info.HFModelID = meta.ModelID
		info.Revision = meta.Revision
		info.InstalledAt = meta.InstalledAt
	}

	return info, nil
}

// downloadWithHFCLI shells out to the Hugging Face CLI for robust large-model transfers.
func (m *Manager) downloadWithHFCLI(ctx context.Context, opts InstallOptions, tmpPath, revision string) error {
	var combinedOut []byte

	if revision == "" {
		revision = "main"
	}
	args := []string{
		"download",
		opts.ModelID,
		"--local-dir", tmpPath,
		"--cache-dir", filepath.Join(tmpPath, ".cache"),
	}
	if revision != "" {
		args = append(args, "--revision", revision)
	}
	if opts.Overwrite {
		args = append(args, "--force-download")
	}
	if len(opts.Files) > 0 {
		args = append(args, "--include", strings.Join(opts.Files, ","))
	}

	// Prefer the modern "hf" entrypoint; fall back to "huggingface-cli" for older installs.
	bin, _ := exec.LookPath("hf")
	if bin == "" {
		bin, _ = exec.LookPath("huggingface-cli")
	}
	if bin == "" {
		return fmt.Errorf("huggingface CLI is not available in PATH (expected hf or huggingface-cli)")
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = os.Environ()
	if opts.Token != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("HUGGING_FACE_HUB_TOKEN=%s", opts.Token))
	}
	out, err := cmd.CombinedOutput()
	combinedOut = out
	if err != nil {
		return fmt.Errorf("%s download failed: %w\n%s", filepath.Base(bin), err, string(out))
	}

	var fileCount int64
	err = filepath.Walk(tmpPath, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fileCount++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to verify download contents: %w", err)
	}
	if fileCount == 0 {
		return fmt.Errorf("hf download succeeded but no files were written to %s\noutput:\n%s", tmpPath, string(combinedOut))
	}

	return nil
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

var segmentSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// CanonicalTarget derives a normalized relative path for a model installation.
func CanonicalTarget(modelID, override string) (string, error) {
	candidates := []string{override, modelID}
	for _, candidate := range candidates {
		if rel, err := normalizeRelativePath(candidate); err == nil && rel != "" {
			return rel, nil
		}
	}
	return "", fmt.Errorf("failed to derive target directory")
}

func normalizeRelativePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty path")
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return "", fmt.Errorf("empty path")
	}
	parts := strings.Split(raw, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		seg := segmentSanitizer.ReplaceAllString(part, "-")
		seg = strings.Trim(seg, "-")
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		cleaned = append(cleaned, seg)
	}
	if len(cleaned) == 0 {
		return "", fmt.Errorf("invalid path %q", raw)
	}
	return strings.Join(cleaned, "/"), nil
}

func toFilesystemPath(rel string) string {
	if rel == "" {
		return rel
	}
	parts := strings.Split(rel, "/")
	return filepath.Join(parts...)
}

func writeMetadata(dir string, meta weightMetadata) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metadataFilename), data, 0o644)
}

func readMetadata(dir string) (*weightMetadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, metadataFilename))
	if err != nil {
		return nil, err
	}
	var meta weightMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (m *Manager) cleanupEmptyParents(modelPath string) {
	absStorage, err := filepath.Abs(m.storagePath)
	if err != nil {
		return
	}
	current := filepath.Dir(modelPath)
	for {
		absCurrent, err := filepath.Abs(current)
		if err != nil {
			return
		}
		if !strings.HasPrefix(absCurrent, absStorage) || absCurrent == absStorage {
			return
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			return
		}
		if len(entries) > 0 {
			return
		}
		if err := os.Remove(current); err != nil {
			return
		}
		current = filepath.Dir(current)
	}
}

func runHFDownload(ctx context.Context, opts InstallOptions, tmpPath, revision string) error {
	bin, err := findHFCommand()
	if err != nil {
		return err
	}
	args := []string{"download", opts.ModelID, "--local-dir", tmpPath, "--revision", revision, "--resume-download"}
	if len(opts.Files) > 0 {
		args = append(args, opts.Files...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	env := append([]string{}, os.Environ()...)
	if opts.Token != "" {
		env = append(env, fmt.Sprintf("HF_TOKEN=%s", opts.Token), fmt.Sprintf("HUGGING_FACE_HUB_TOKEN=%s", opts.Token))
	}
	if !envHas(env, "HF_HOME") {
		env = append(env, fmt.Sprintf("HF_HOME=%s", filepath.Join(filepath.Dir(tmpPath), ".hf-cache")))
	}
	cmd.Env = env

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s download failed: %w\n%s", filepath.Base(bin), err, output.String())
	}

	hasFiles, err := hasAnyFiles(tmpPath)
	if err != nil {
		return err
	}
	if !hasFiles {
		return fmt.Errorf("hf download succeeded but no files were written to %s\n%s", tmpPath, output.String())
	}
	return nil
}

func findHFCommand() (string, error) {
	if bin, err := exec.LookPath("hf"); err == nil {
		return bin, nil
	}
	if bin, err := exec.LookPath("huggingface-cli"); err == nil {
		return bin, nil
	}
	return "", fmt.Errorf("hugging face CLI is not installed in PATH (expected hf or huggingface-cli)")
}

func envHas(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func hasAnyFiles(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".hf-cache" || path == dir {
				return nil
			}
			return nil
		}
		if d.Name() == metadataFilename {
			return nil
		}
		found = true
		return io.EOF
	})
	if err != nil && err != io.EOF {
		return false, err
	}
	return found, nil
}

func (m *Manager) purgeHFCache(modelID string) {
	if modelID == "" {
		return
	}
	bin, err := findHFCommand()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "cache", "rm", fmt.Sprintf("model/%s", modelID), "-y")
	cmd.Env = os.Environ()
	_ = cmd.Run()
}
