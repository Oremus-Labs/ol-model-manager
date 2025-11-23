package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
	"github.com/oremus-labs/ol-model-manager/internal/weights"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestListWeights(t *testing.T) {
	t.Parallel()

	store := &fakeWeightStore{
		listResp: []weights.WeightInfo{{
			Name:      "qwen2.5-0.5b",
			SizeBytes: 1234,
		}},
	}

	handler := New(nil, nil, store, nil, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/weights", nil)
	c.Request = req

	handler.ListWeights(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 got %d", w.Code)
	}

	var body struct {
		Weights []weights.WeightInfo `json:"weights"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(body.Weights) != 1 || body.Weights[0].Name != "qwen2.5-0.5b" {
		t.Fatalf("unexpected weights payload: %+v", body)
	}
}

func TestInstallWeightsDerivesFilesFromHuggingFace(t *testing.T) {
	t.Parallel()

	store := &fakeWeightStore{
		installResp: &weights.WeightInfo{
			Name: "qwen2.5-0.5b",
		},
	}

	discovery := &fakeDiscovery{
		hfModel: &vllm.HuggingFaceModel{
			ID: "Qwen/Qwen2.5-0.5B",
			Siblings: []vllm.HFSibling{
				{RFileName: "config.json"},
				{RFileName: "pytorch_model.bin"},
			},
		},
	}

	handler := New(nil, nil, store, discovery, nil, nil, nil, Options{
		WeightsPVCName:     "venus-model-storage",
		InferenceModelRoot: "/mnt/models",
	})

	reqBody := `{"hfModelId":"Qwen/Qwen2.5-0.5B"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/weights/install", strings.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.InstallWeights(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 got %d body=%s", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["storageUri"] != "pvc://venus-model-storage/qwen2.5-0.5b" {
		t.Fatalf("storageUri mismatch: %v", body["storageUri"])
	}

	if body["inferenceModelPath"] != "/mnt/models/qwen2.5-0.5b" {
		t.Fatalf("inferenceModelPath mismatch: %v", body["inferenceModelPath"])
	}

	if !store.installCalled {
		t.Fatalf("expected install to be called")
	}

	wantFiles := []string{"config.json", "pytorch_model.bin"}
	if !reflect.DeepEqual(store.lastInstallOpts.Files, wantFiles) {
		t.Fatalf("install files mismatch: got %v want %v", store.lastInstallOpts.Files, wantFiles)
	}

	if store.lastInstallOpts.ModelID != "Qwen/Qwen2.5-0.5B" {
		t.Fatalf("unexpected modelID: %s", store.lastInstallOpts.ModelID)
	}
}

func TestInstallWeightsRejectsInvalidHFID(t *testing.T) {
	t.Parallel()

	handler := New(nil, nil, &fakeWeightStore{}, &fakeDiscovery{}, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := strings.NewReader(`{"hfModelId":"bad-id"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/weights/install", body)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.InstallWeights(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGenerateCatalogEntry(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{
		modelResp: &catalog.Model{ID: "draft-model", HFModelID: "foo/bar"},
	}

	handler := New(nil, nil, nil, discovery, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := strings.NewReader(`{"hfModelId":"foo/bar","storageUri":"pvc://venus/foo"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/catalog/generate", body)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.GenerateCatalogEntry(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Model catalog.Model `json:"model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Model.StorageURI != "pvc://venus/foo" {
		t.Fatalf("storage override not applied: %+v", resp.Model)
	}
}

func TestCreateCatalogPR(t *testing.T) {
	t.Parallel()

	writer := &fakeCatalogWriter{
		saveResult: &catalogwriter.SaveResult{
			RelativePath: "models/foo.json",
		},
		pr: &catalogwriter.PullRequest{
			Number:  42,
			HTMLURL: "https://github.com/example/pull/42",
		},
	}

	handler := New(nil, nil, nil, nil, nil, writer, nil, Options{
		GitHubToken: "token",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := strings.NewReader(`{"model":{"id":"foo","hfModelId":"foo/bar"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/catalog/pr", body)
	c.Request.Header.Set("Content-Type", "application/json")

	handler.CreateCatalogPR(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["pullRequest"] == nil {
		t.Fatalf("expected pullRequest in response: %v", resp)
	}

	if !writer.commitCalled {
		t.Fatalf("expected commit to be called")
	}
	if writer.lastBranch != "model/foo" {
		t.Fatalf("unexpected branch: %s", writer.lastBranch)
	}
}

type fakeWeightStore struct {
	listResp        []weights.WeightInfo
	getResp         *weights.WeightInfo
	statsResp       *weights.StorageStats
	installResp     *weights.WeightInfo
	installErr      error
	installCalled   bool
	lastInstallOpts weights.InstallOptions
}

func (f *fakeWeightStore) List() ([]weights.WeightInfo, error) {
	return f.listResp, nil
}

func (f *fakeWeightStore) Get(name string) (*weights.WeightInfo, error) {
	return f.getResp, nil
}

func (f *fakeWeightStore) Delete(name string) error {
	return nil
}

func (f *fakeWeightStore) GetStats() (*weights.StorageStats, error) {
	return f.statsResp, nil
}

func (f *fakeWeightStore) InstallFromHuggingFace(ctx context.Context, opts weights.InstallOptions) (*weights.WeightInfo, error) {
	f.installCalled = true
	f.lastInstallOpts = opts
	return f.installResp, f.installErr
}

type fakeDiscovery struct {
	hfModel   *vllm.HuggingFaceModel
	modelResp *catalog.Model
}

func (f *fakeDiscovery) ListSupportedArchitectures() ([]vllm.ModelArchitecture, error) {
	return nil, nil
}

func (f *fakeDiscovery) GenerateModelConfig(req vllm.GenerateRequest) (*catalog.Model, error) {
	if f.modelResp != nil {
		model := *f.modelResp
		if req.HFModelID != "" {
			model.HFModelID = req.HFModelID
		}
		if req.DisplayName != "" {
			model.DisplayName = req.DisplayName
		}
		return &model, nil
	}
	return &catalog.Model{
		ID:          "auto-model",
		HFModelID:   req.HFModelID,
		DisplayName: req.DisplayName,
	}, nil
}

func (f *fakeDiscovery) GetHuggingFaceModel(modelID string) (*vllm.HuggingFaceModel, error) {
	model := *f.hfModel
	model.ID = modelID
	model.ModelID = modelID
	return &model, nil
}

type fakeCatalogWriter struct {
	saveResult   *catalogwriter.SaveResult
	saveErr      error
	commitErr    error
	pr           *catalogwriter.PullRequest
	prErr        error
	commitCalled bool
	lastBranch   string
	lastMessage  string
	lastPaths    []string
}

func (f *fakeCatalogWriter) Save(model *catalog.Model) (*catalogwriter.SaveResult, error) {
	return f.saveResult, f.saveErr
}

func (f *fakeCatalogWriter) CommitAndPush(ctx context.Context, branch, base, message string, paths ...string) error {
	f.commitCalled = true
	f.lastBranch = branch
	f.lastMessage = message
	f.lastPaths = paths
	return f.commitErr
}

func (f *fakeCatalogWriter) CreatePullRequest(ctx context.Context, opts catalogwriter.PullRequestOptions) (*catalogwriter.PullRequest, error) {
	return f.pr, f.prErr
}
