// Package weights manages model weights on the persistent volume.
package weights

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Manager handles model weight operations on the Venus PVC.
type Manager struct {
	storagePath    string
	downloadClient *http.Client
	hfBaseURL      string
	reservedNames  map[string]struct{}
}

// Option configures a Manager at construction.
type Option func(*Manager)

// WithHTTPClient overrides the HTTP client used for downloads.
func WithHTTPClient(client *http.Client) Option {
	return func(m *Manager) {
		if client != nil {
			m.downloadClient = client
		}
	}
}

// WithHFBaseURL overrides the base HuggingFace URL (useful for tests).
func WithHFBaseURL(base string) Option {
	return func(m *Manager) {
		if base != "" {
			m.hfBaseURL = strings.TrimSuffix(base, "/")
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
		storagePath:    storagePath,
		downloadClient: &http.Client{Timeout: 5 * time.Minute},
		hfBaseURL:      "https://huggingface.co",
		reservedNames: map[string]struct{}{
			".hf-cache":  {},
			"modules":    {},
			"lost+found": {},
		},
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

	if err := os.RemoveAll(modelPath); err != nil {
		return fmt.Errorf("failed to delete model weights: %w", err)
	}

	m.cleanupEmptyParents(modelPath)
	m.removeHuggingFaceCache(rel)

	return nil
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

	if len(opts.Files) == 0 {
		return nil, fmt.Errorf("no files specified for download")
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

	client := m.downloadClient
	if client == nil {
		client = http.DefaultClient
	}
	var downloadedFiles int

	totalFiles := len(opts.Files)
	for _, file := range opts.Files {
		select {
		case <-ctx.Done():
			_ = os.RemoveAll(tmpPath)
			return nil, ctx.Err()
		default:
		}

		// Skip directories
		if strings.HasSuffix(file, "/") || file == "" {
			continue
		}

		url := fmt.Sprintf("%s/%s/resolve/%s/%s", m.hfBaseURL, opts.ModelID, revision, file)
		destFile := filepath.Join(tmpPath, file)
		currentIndex := downloadedFiles
		if err := downloadFile(ctx, client, url, destFile, opts.Token, func(downloaded, total int64) {
			if opts.ProgressBytes != nil && totalFiles > 0 {
				opts.ProgressBytes(file, currentIndex, totalFiles, downloaded, total)
			}
		}); err != nil {
			_ = os.RemoveAll(tmpPath)
			return nil, fmt.Errorf("failed to download %s: %w", file, err)
		}
		downloadedFiles++
		if opts.Progress != nil {
			opts.Progress(file, downloadedFiles, len(opts.Files))
		}
	}

	if downloadedFiles == 0 {
		_ = os.RemoveAll(tmpPath)
		return nil, fmt.Errorf("no files downloaded for %s", opts.ModelID)
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

	if err := m.populateHuggingFaceCache(opts.ModelID, revision, destPath); err != nil {
		log.Printf("weights: failed to hydrate HuggingFace cache for %s: %v", target, err)
	}

	if opts.Progress != nil {
		opts.Progress("", len(opts.Files), len(opts.Files))
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

func downloadFile(ctx context.Context, client *http.Client, url, destPath, token string, progress func(downloaded, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("failed to prepare path: %w", err)
	}

	tmpFile := destPath + ".part"
	file, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	var written int64
	total := resp.ContentLength
	buf := make([]byte, 2<<20)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := file.Write(buf[:n]); err != nil {
				file.Close()
				_ = os.Remove(tmpFile)
				return fmt.Errorf("failed to write file: %w", err)
			}
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			file.Close()
			_ = os.Remove(tmpFile)
			return fmt.Errorf("failed to download file: %w", readErr)
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to close file: %w", err)
	}

	if err := os.Rename(tmpFile, destPath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to finalize file: %w", err)
	}

	return nil
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

func (m *Manager) populateHuggingFaceCache(modelID, revision, sourceDir string) error {
	if modelID == "" {
		return nil
	}
	if revision == "" {
		revision = "main"
	}
	cacheBase := filepath.Join(m.storagePath, ".hf-cache", "hub", formatHFCacheModelID(modelID))
	snapshotDir := filepath.Join(cacheBase, "snapshots", revision)
	if err := os.RemoveAll(snapshotDir); err != nil {
		return err
	}
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return err
	}

	err := filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(snapshotDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Name() == metadataFilename {
			return nil
		}
		_ = os.Remove(target)
		return os.Symlink(path, target)
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(cacheBase, "refs"), 0o755); err != nil {
		return err
	}
	refFile := filepath.Join(cacheBase, "refs", revision)
	return os.WriteFile(refFile, []byte(revision), 0o644)
}

func (m *Manager) removeHuggingFaceCache(modelName string) {
	if modelName == "" {
		return
	}
	cacheDir := filepath.Join(m.storagePath, ".hf-cache", "hub", formatHFCacheModelID(modelName))
	_ = os.RemoveAll(cacheDir)
}

func formatHFCacheModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	modelID = strings.Trim(modelID, "/")
	modelID = strings.ReplaceAll(modelID, "/", "--")
	return fmt.Sprintf("models--%s", modelID)
}
