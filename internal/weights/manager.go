// Package weights manages model weights on the persistent volume.
package weights

import (
	"context"
	"fmt"
	"io"
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

// InstallOptions controls how weights are installed for a model.
type InstallOptions struct {
	ModelID   string
	Revision  string
	Target    string
	Files     []string
	Token     string
	Overwrite bool
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
	if _, err := os.Stat(m.storagePath); os.IsNotExist(err) {
		return []WeightInfo{}, nil
	}

	var weights []WeightInfo

	entries, err := os.ReadDir(m.storagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read storage directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if m.isReserved(entry.Name()) {
			continue
		}

		modelPath := filepath.Join(m.storagePath, entry.Name())
		info, err := m.getWeightInfo(modelPath, entry.Name())
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

// Get returns information about a specific model's weights.
func (m *Manager) Get(modelName string) (*WeightInfo, error) {
	if m.isReserved(modelName) {
		return nil, fmt.Errorf("model weights not found: %s", modelName)
	}
	modelPath := filepath.Join(m.storagePath, modelName)

	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("model weights not found: %s", modelName)
	}

	return m.getWeightInfo(modelPath, modelName)
}

// Delete removes a model's weights from storage.
func (m *Manager) Delete(modelName string) error {
	if m.isReserved(modelName) {
		return fmt.Errorf("model weights not found: %s", modelName)
	}
	modelPath := filepath.Join(m.storagePath, modelName)

	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return fmt.Errorf("model weights not found: %s", modelName)
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

	target := opts.Target
	if target == "" {
		target = sanitizeName(opts.ModelID)
	}
	if target == "" {
		return nil, fmt.Errorf("failed to derive target directory name")
	}

	if m.isReserved(target) {
		return nil, fmt.Errorf("cannot install weights into reserved path: %s", target)
	}

	revision := opts.Revision
	if revision == "" {
		revision = "main"
	}

	destPath := filepath.Join(m.storagePath, target)
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
		if err := downloadFile(ctx, client, url, destFile, opts.Token); err != nil {
			_ = os.RemoveAll(tmpPath)
			return nil, fmt.Errorf("failed to download %s: %w", file, err)
		}
		downloadedFiles++
	}

	if downloadedFiles == 0 {
		_ = os.RemoveAll(tmpPath)
		return nil, fmt.Errorf("no files downloaded for %s", opts.ModelID)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.RemoveAll(tmpPath)
		return nil, fmt.Errorf("failed to finalize weights: %w", err)
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
	if strings.HasPrefix(name, ".") {
		return true
	}
	_, ok := m.reservedNames[name]
	return ok
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

	return &WeightInfo{
		Path:         path,
		Name:         name,
		SizeBytes:    totalSize,
		SizeHuman:    formatBytes(totalSize),
		ModifiedTime: modTime,
		FileCount:    fileCount,
	}, nil
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

func sanitizeName(value string) string {
	v := strings.ToLower(value)
	v = strings.ReplaceAll(v, "/", "-")
	v = strings.ReplaceAll(v, "_", "-")
	v = strings.TrimSpace(v)
	re := regexp.MustCompile(`-+`)
	v = re.ReplaceAllString(v, "-")
	return strings.Trim(v, "-")
}

func downloadFile(ctx context.Context, client *http.Client, url, destPath, token string) error {
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

	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to write file: %w", err)
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
