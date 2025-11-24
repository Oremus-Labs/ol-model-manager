package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/catalogwriter"
	"github.com/oremus-labs/ol-model-manager/internal/recommendations"
	"github.com/oremus-labs/ol-model-manager/internal/store"
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
			Name:      "Qwen/Qwen2.5-0.5B",
			SizeBytes: 1234,
		}},
	}

	handler := New(nil, nil, store, nil, nil, nil, nil, nil, nil, Options{})

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

	if len(body.Weights) != 1 || body.Weights[0].Name != "Qwen/Qwen2.5-0.5B" {
		t.Fatalf("unexpected weights payload: %+v", body)
	}
}

func TestInstallWeightsDerivesFilesFromHuggingFace(t *testing.T) {
	t.Parallel()

	store := &fakeWeightStore{
		installResp: &weights.WeightInfo{
			Name: "Qwen/Qwen2.5-0.5B",
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

	handler := New(nil, nil, store, discovery, nil, nil, nil, nil, nil, Options{
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

	if body["storageUri"] != "pvc://venus-model-storage/Qwen/Qwen2.5-0.5B" {
		t.Fatalf("storageUri mismatch: %v", body["storageUri"])
	}

	if body["inferenceModelPath"] != "/mnt/models/Qwen/Qwen2.5-0.5B" {
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

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"), "sqlite")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestDeleteJobsEndpoint(t *testing.T) {
	t.Parallel()

	stateStore := openTestStore(t)
	handler := New(nil, nil, nil, nil, nil, nil, nil, stateStore, nil, Options{})

	if err := stateStore.CreateJob(&store.Job{ID: "job-delete", Type: "weight_install"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/jobs?status=pending", nil)

	handler.DeleteJobs(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 got %d", w.Code)
	}
	if jobs, err := stateStore.ListJobs(10); err != nil || len(jobs) != 0 {
		t.Fatalf("expected jobs cleared, got %+v err=%v", jobs, err)
	}
}

func TestClearHistoryEndpoint(t *testing.T) {
	t.Parallel()

	stateStore := openTestStore(t)
	handler := New(nil, nil, nil, nil, nil, nil, nil, stateStore, nil, Options{})

	if err := stateStore.AppendHistory(&store.HistoryEntry{Event: "test"}); err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/history", nil)

	handler.ClearHistory(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", w.Code)
	}
	if history, err := stateStore.ListHistory(10); err != nil || len(history) != 0 {
		t.Fatalf("expected history cleared, got %+v err=%v", history, err)
	}
}

func TestInstallWeightsRejectsInvalidHFID(t *testing.T) {
	t.Parallel()

	handler := New(nil, nil, &fakeWeightStore{}, &fakeDiscovery{}, nil, nil, nil, nil, nil, Options{})

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

	handler := New(nil, nil, nil, discovery, nil, nil, nil, nil, nil, Options{})

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

	handler := New(nil, nil, nil, nil, nil, writer, nil, nil, nil, Options{
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

func TestDescribeVLLMModel(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{
		modelInfo: &vllm.ModelInsight{
			Compatible:           true,
			MatchedArchitectures: []string{"qwen"},
			SuggestedCatalog:     &catalog.Model{ID: "foo"},
		},
	}

	handler := New(nil, nil, nil, discovery, nil, nil, &fakeAdvisor{}, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/vllm/model-info", strings.NewReader(`{"hfModelId":"foo/bar"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler.DescribeVLLMModel(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Insight struct {
			Compatible bool `json:"compatible"`
		} `json:"insight"`
		Recommendations []recommendations.Recommendation `json:"recommendations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !resp.Insight.Compatible {
		t.Fatalf("expected compatible flag")
	}
	if len(resp.Recommendations) == 0 {
		t.Fatalf("expected recommendations")
	}
}

func TestGetHuggingFaceModel(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{
		modelInfo: &vllm.ModelInsight{
			HFModel: &vllm.HuggingFaceModel{ModelID: "foo/bar"},
		},
	}
	handler := New(nil, nil, nil, discovery, nil, nil, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{{Key: "id", Value: "foo/bar"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/huggingface/models/foo/bar", nil)

	handler.GetHuggingFaceModel(c)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetVLLMArchitecture(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{
		archDetail: &vllm.ArchitectureDetail{
			ModelArchitecture: vllm.ModelArchitecture{Name: "qwen", FilePath: "models/qwen.py"},
			Source:            "class Qwen: pass",
		},
	}
	handler := New(nil, nil, nil, discovery, nil, nil, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = []gin.Param{{Key: "architecture", Value: "qwen"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/vllm/model/qwen", nil)

	handler.GetVLLMArchitecture(c)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
}

func TestSystemInfo(t *testing.T) {
	t.Parallel()

	wm := &fakeWeightStore{
		statsResp: &weights.StorageStats{ModelCount: 1},
	}
	h := New(&catalog.Catalog{}, nil, wm, nil, nil, nil, &fakeAdvisor{}, nil, nil, Options{
		Version:                "0.0.1",
		CatalogRoot:            "/catalog",
		CatalogModelsDir:       "models",
		WeightsPath:            "/mnt/models",
		StatePath:              "/app/state",
		AuthEnabled:            true,
		DataStoreDriver:        "bolt",
		DataStoreDSN:           "/app/state/state.db",
		DatabasePVCName:        "model-manager-db",
		HuggingFaceCacheTTL:    time.Minute,
		VLLMCacheTTL:           2 * time.Minute,
		RecommendationCacheTTL: 3 * time.Minute,
		SlackWebhookURL:        "https://hooks.slack.invalid",
		PVCAlertThreshold:      0.9,
		GPUProfilesPath:        "/app/config/gpu-profiles.json",
		GPUInventorySource:     "k8s-nodes",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/system/info", nil)

	h.SystemInfo(c)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if body["version"] != "0.0.1" {
		t.Fatalf("expected version in response: %+v", body)
	}
	cache, ok := body["cache"].(map[string]interface{})
	if !ok || cache["catalogTTL"] == "" {
		t.Fatalf("cache metadata missing: %+v", body["cache"])
	}
	persist, ok := body["persistence"].(map[string]interface{})
	if !ok || persist["driver"] != "bolt" {
		t.Fatalf("persistence metadata missing: %+v", persist)
	}
	notifications, ok := body["notifications"].(map[string]interface{})
	if !ok || notifications["slackWebhookConfigured"] != true {
		t.Fatalf("notification metadata missing: %+v", notifications)
	}
}

func TestListJobsFilters(t *testing.T) {
	t.Parallel()

	st := newTempStore(t)
	h := New(nil, nil, nil, nil, nil, nil, nil, st, nil, Options{HistoryLimit: 5})

	job1 := &store.Job{
		ID:      "job-1",
		Type:    "weight_install",
		Status:  store.JobDone,
		Payload: map[string]interface{}{"hfModelId": "foo/bar"},
	}
	_ = st.CreateJob(job1)
	job1.Status = store.JobDone
	_ = st.UpdateJob(job1)

	job2 := &store.Job{
		ID:      "job-2",
		Type:    "weight_install",
		Status:  store.JobFailed,
		Payload: map[string]interface{}{"hfModelId": "other"},
	}
	_ = st.CreateJob(job2)
	job2.Status = store.JobFailed
	_ = st.UpdateJob(job2)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/jobs?status=completed&type=weight_install&modelId=foo/bar", nil)
	c.Request = req

	h.ListJobs(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", w.Code)
	}
	var payload struct {
		Jobs []store.Job `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed decoding jobs: %v", err)
	}
	if len(payload.Jobs) != 1 || payload.Jobs[0].ID != "job-1" {
		t.Fatalf("unexpected jobs payload: %+v", payload)
	}
}

func TestListHistoryFilters(t *testing.T) {
	t.Parallel()

	st := newTempStore(t)
	h := New(nil, nil, nil, nil, nil, nil, nil, st, nil, Options{HistoryLimit: 5})

	_ = st.AppendHistory(&store.HistoryEntry{ID: "1", Event: "weight_install_completed", ModelID: "foo"})
	_ = st.AppendHistory(&store.HistoryEntry{ID: "2", Event: "model_activated", ModelID: "bar"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/history?event=weight_install_completed&modelId=foo", nil)
	c.Request = req

	h.ListHistory(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 got %d", w.Code)
	}
	var resp struct {
		Events []store.HistoryEntry `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].ModelID != "foo" {
		t.Fatalf("unexpected history filter result: %+v", resp.Events)
	}
}

func TestOpenAPISpecEndpoint(t *testing.T) {
	t.Parallel()

	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/openapi", nil)

	h.OpenAPISpec(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "\"openapi\"") {
		t.Fatalf("expected openapi json, got %s", w.Body.String())
	}
}

func TestSearchHuggingFaceParsesFilters(t *testing.T) {
	t.Parallel()

	discovery := &fakeDiscovery{
		modelInfo: &vllm.ModelInsight{
			HFModel: &vllm.HuggingFaceModel{ID: "test/model"},
		},
	}

	h := New(nil, nil, nil, discovery, nil, nil, nil, nil, nil, Options{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/huggingface/search?q=Qwen&limit=5&pipelineTag=text-generation&author=hf&license=apache-2.0&tag=quantized&tags=gguf,ggml&compatibleOnly=true&sort=downloads&direction=desc", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.SearchHuggingFace(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", w.Code, w.Body.String())
	}

	opts := discovery.lastSearch
	if opts.Query != "Qwen" || opts.Limit != 5 {
		t.Fatalf("unexpected search options: %+v", opts)
	}
	if !opts.OnlyCompatible {
		t.Fatalf("expected compatibleOnly true")
	}
	if opts.PipelineTag != "text-generation" || opts.Author != "hf" || opts.License != "apache-2.0" {
		t.Fatalf("filter mismatch: %+v", opts)
	}
	if opts.Sort != "downloads" || opts.Direction != "desc" {
		t.Fatalf("sort mismatch: %+v", opts)
	}
	if len(opts.Tags) != 3 {
		t.Fatalf("expected tags to be parsed: %+v", opts.Tags)
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
	hfModel    *vllm.HuggingFaceModel
	modelResp  *catalog.Model
	modelInfo  *vllm.ModelInsight
	archDetail *vllm.ArchitectureDetail
	lastSearch vllm.SearchOptions
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

func (f *fakeDiscovery) DescribeModel(id string, auto bool) (*vllm.ModelInsight, error) {
	if f.modelInfo == nil {
		return nil, fmt.Errorf("not found")
	}
	info := *f.modelInfo
	return &info, nil
}

func (f *fakeDiscovery) SearchModels(opts vllm.SearchOptions) ([]*vllm.ModelInsight, error) {
	f.lastSearch = opts
	if f.modelInfo == nil {
		return []*vllm.ModelInsight{}, nil
	}
	info := *f.modelInfo
	return []*vllm.ModelInsight{&info}, nil
}

func (f *fakeDiscovery) GetArchitectureDetail(name string) (*vllm.ArchitectureDetail, error) {
	if f.archDetail == nil {
		return nil, fmt.Errorf("not found")
	}
	detail := *f.archDetail
	return &detail, nil
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

type fakeAdvisor struct{}

func (f *fakeAdvisor) Compatibility(model *catalog.Model, gpuType string) recommendations.CompatibilityReport {
	return recommendations.CompatibilityReport{
		ModelID:         model.ID,
		GPUType:         gpuType,
		EstimatedVRAMGB: 12,
		Compatible:      true,
	}
}

func (f *fakeAdvisor) Recommend(gpuType string) recommendations.Recommendation {
	return recommendations.Recommendation{GPUType: gpuType}
}

func (f *fakeAdvisor) RecommendForModel(model *catalog.Model, gpuType string) recommendations.Recommendation {
	return recommendations.Recommendation{GPUType: gpuType}
}

func (f *fakeAdvisor) Profiles() []recommendations.GPUProfile {
	return []recommendations.GPUProfile{
		{Name: "test-gpu", MemoryGB: 32},
	}
}

func newTempStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "state.db")
	s, err := store.Open(dsn, "sqlite")
	if err != nil {
		t.Fatalf("failed opening store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}
