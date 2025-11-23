package catalogwriter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
)

// Options configures a Writer instance.
type Options struct {
	Root        string
	ModelsDir   string
	RepoSlug    string
	BaseBranch  string
	AuthorName  string
	AuthorEmail string
	GitBinary   string
	HTTPClient  *http.Client
}

// Writer automates model catalog contributions.
type Writer struct {
	root        string
	modelsDir   string
	repoSlug    string
	baseBranch  string
	authorName  string
	authorEmail string
	gitBinary   string
	httpClient  *http.Client
}

// SaveResult describes the outcome of persisting a model file.
type SaveResult struct {
	AbsolutePath string
	RelativePath string
}

// PullRequestOptions describe how to open a GitHub PR.
type PullRequestOptions struct {
	Branch string
	Base   string
	Title  string
	Body   string
	Draft  bool
	Token  string
}

// PullRequest contains the subset of GitHub PR metadata we care about.
type PullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	URL     string `json:"url"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
}

// New constructs a Writer with sane defaults.
func New(opts Options) (*Writer, error) {
	if opts.Root == "" {
		return nil, errors.New("catalog root is required")
	}
	modelsDir := opts.ModelsDir
	if modelsDir == "" {
		modelsDir = "models"
	}
	gitBinary := opts.GitBinary
	if gitBinary == "" {
		gitBinary = "git"
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	return &Writer{
		root:        opts.Root,
		modelsDir:   modelsDir,
		repoSlug:    opts.RepoSlug,
		baseBranch:  opts.BaseBranch,
		authorName:  opts.AuthorName,
		authorEmail: opts.AuthorEmail,
		gitBinary:   gitBinary,
		httpClient:  client,
	}, nil
}

// Save writes the catalog entry to disk and returns the file metadata.
func (w *Writer) Save(model *catalog.Model) (*SaveResult, error) {
	if model == nil {
		return nil, errors.New("model cannot be nil")
	}
	if model.ID == "" {
		return nil, errors.New("model id is required")
	}

	fileName := fmt.Sprintf("%s.json", model.ID)
	absPath := filepath.Join(w.root, w.modelsDir, fileName)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create model directory: %w", err)
	}

	data, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal model: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("failed to write model file: %w", err)
	}

	rel, err := filepath.Rel(w.root, absPath)
	if err != nil {
		rel = absPath
	}

	return &SaveResult{AbsolutePath: absPath, RelativePath: rel}, nil
}

// CommitAndPush stages the given paths, commits, and pushes to the remote branch.
func (w *Writer) CommitAndPush(ctx context.Context, branch, base, message string, paths ...string) error {
	if branch == "" {
		return errors.New("branch is required")
	}
	if message == "" {
		return errors.New("commit message is required")
	}
	if len(paths) == 0 {
		return errors.New("at least one path must be provided")
	}

	if err := w.ensureAuthor(ctx); err != nil {
		return err
	}
	if err := w.checkoutBranch(ctx, branch, base); err != nil {
		return err
	}

	addArgs := append([]string{"add"}, paths...)
	if _, err := w.runGit(ctx, addArgs...); err != nil {
		return err
	}

	if _, err := w.runGit(ctx, "commit", "-m", message); err != nil {
		return err
	}

	if _, err := w.runGit(ctx, "push", "-u", "origin", branch); err != nil {
		return err
	}

	return nil
}

// CreatePullRequest opens a GitHub pull request for the prepared branch.
func (w *Writer) CreatePullRequest(ctx context.Context, opts PullRequestOptions) (*PullRequest, error) {
	if w.repoSlug == "" {
		return nil, errors.New("repo slug is not configured")
	}
	if opts.Branch == "" {
		return nil, errors.New("branch is required")
	}
	if opts.Token == "" {
		return nil, errors.New("GitHub token is required to open PRs")
	}
	if opts.Base == "" {
		opts.Base = w.baseBranch
	}
	if opts.Base == "" {
		return nil, errors.New("base branch is required")
	}
	if opts.Title == "" {
		opts.Title = fmt.Sprintf("Add model %s", opts.Branch)
	}

	payload := map[string]interface{}{
		"title": opts.Title,
		"head":  opts.Branch,
		"base":  opts.Base,
		"body":  opts.Body,
		"draft": opts.Draft,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode PR payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://api.github.com/repos/%s/pulls", w.repoSlug), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to construct PR request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+opts.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub PR request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub PR request failed: %s", strings.TrimSpace(string(buf)))
	}

	var pr PullRequest
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("failed to decode PR response: %w", err)
	}

	return &pr, nil
}

func (w *Writer) ensureAuthor(ctx context.Context) error {
	if w.authorName != "" {
		if _, err := w.runGit(ctx, "config", "user.name", w.authorName); err != nil {
			return err
		}
	}
	if w.authorEmail != "" {
		if _, err := w.runGit(ctx, "config", "user.email", w.authorEmail); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) checkoutBranch(ctx context.Context, branch, base string) error {
	if base == "" {
		base = w.baseBranch
	}

	if base != "" {
		_, _ = w.runGit(ctx, "fetch", "origin", base)
		if _, err := w.runGit(ctx, "checkout", "-B", branch, "origin/"+base); err == nil {
			return nil
		}
		return w.simpleCheckout(ctx, branch, base)
	}

	_, err := w.runGit(ctx, "checkout", "-B", branch)
	return err
}

func (w *Writer) simpleCheckout(ctx context.Context, branch, base string) error {
	if base == "" {
		base = branch
	}
	_, err := w.runGit(ctx, "checkout", "-B", branch, base)
	return err
}

func (w *Writer) runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, w.gitBinary, args...)
	cmd.Dir = w.root
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	if err != nil {
		return buf.String(), fmt.Errorf("git %s failed: %w - %s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String(), nil
}
