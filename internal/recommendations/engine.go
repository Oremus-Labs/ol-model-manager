package recommendations

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
)

// GPUProfile describes a GPU class available for inference.
type GPUProfile struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	MemoryGB    int               `json:"memoryGB"`
	Vendor      string            `json:"vendor,omitempty"`
	DeviceID    string            `json:"deviceId,omitempty"`
	Features    []string          `json:"features,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// Engine produces compatibility reports and runtime recommendations.
type Engine struct {
	profiles map[string]GPUProfile
	ordered  []GPUProfile
}

// CompatibilityReport summarizes whether a model fits on a GPU.
type CompatibilityReport struct {
	ModelID         string      `json:"modelId"`
	GPUType         string      `json:"gpuType,omitempty"`
	EstimatedVRAMGB int         `json:"estimatedVramGb"`
	Reason          string      `json:"reason,omitempty"`
	Compatible      bool        `json:"compatible"`
	Candidates      []Candidate `json:"candidates,omitempty"`
	Suggestions     []string    `json:"suggestions,omitempty"`
}

// Candidate conveys compatibility per GPU profile.
type Candidate struct {
	GPU        string `json:"gpu"`
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

// Recommendation captures runtime hints for a GPU.
type Recommendation struct {
	GPUType  string   `json:"gpuType"`
	MemoryGB int      `json:"memoryGB,omitempty"`
	Flags    []string `json:"flags"`
	Notes    []string `json:"notes"`
}

// LoadProfiles loads GPU profiles from a JSON file.
func LoadProfiles(path string) (map[string]GPUProfile, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to read gpu profile file: %w", err)
	}
	var profiles []GPUProfile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, fmt.Errorf("failed to decode gpu profile file: %w", err)
	}
	result := make(map[string]GPUProfile, len(profiles))
	for _, profile := range profiles {
		if profile.Name == "" {
			continue
		}
		result[strings.ToLower(profile.Name)] = profile
	}
	return result, nil
}

// New constructs an Engine from GPU profiles.
func New(profiles map[string]GPUProfile) *Engine {
	copies := make(map[string]GPUProfile, len(profiles))
	ordered := make([]GPUProfile, 0, len(profiles))
	for k, v := range profiles {
		copies[k] = v
		ordered = append(ordered, v)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].Name) < strings.ToLower(ordered[j].Name)
	})
	return &Engine{profiles: copies, ordered: ordered}
}

// Compatibility evaluates whether the model can fit on the provided GPU type.
func (e *Engine) Compatibility(model *catalog.Model, gpuType string) CompatibilityReport {
	required, reason := estimateModelVRAM(model)
	report := CompatibilityReport{
		ModelID:         model.ID,
		EstimatedVRAMGB: required,
		Reason:          reason,
	}

	if len(e.profiles) == 0 {
		report.Reason = "no gpu profiles configured"
		return report
	}

	if gpuType != "" {
		profile, ok := e.profiles[strings.ToLower(gpuType)]
		report.GPUType = gpuType
		if !ok {
			report.Reason = "unknown gpu type"
			return report
		}
		report.GPUType = profile.Name
		report.Compatible = profile.MemoryGB >= required
		if !report.Compatible {
			report.Reason = fmt.Sprintf("requires %d GiB, only %d GiB available", required, profile.MemoryGB)
		}
		report.Suggestions = buildSuggestions(profile)
		return report
	}

	for _, profile := range e.ordered {
		candidate := Candidate{
			GPU:        profile.Name,
			Compatible: profile.MemoryGB >= required,
		}
		if !candidate.Compatible {
			short := required - profile.MemoryGB
			if short < 0 {
				short = 0
			}
			candidate.Reason = fmt.Sprintf("short by %d GiB", short)
		}
		report.Candidates = append(report.Candidates, candidate)
	}

	return report
}

// Recommend returns runtime flag suggestions for the GPU.
func (e *Engine) Recommend(gpuType string) Recommendation {
	return e.RecommendForModel(nil, gpuType)
}

// RecommendForModel tailors flags to a GPU + catalog model.
func (e *Engine) RecommendForModel(model *catalog.Model, gpuType string) Recommendation {
	profile, ok := e.profiles[strings.ToLower(gpuType)]
	if !ok {
		return Recommendation{GPUType: gpuType, Notes: []string{"unknown gpu type"}}
	}

	rec := Recommendation{
		GPUType:  profile.Name,
		MemoryGB: profile.MemoryGB,
	}

	var required int
	if model != nil {
		required, _ = estimateModelVRAM(model)
	} else {
		required = 16
	}
	margin := profile.MemoryGB - required

	if hasFeature(profile, "bf16") && profile.MemoryGB >= 32 {
		rec.Flags = append(rec.Flags, "--dtype", "bfloat16")
	} else if hasFeature(profile, "fp16") {
		rec.Flags = append(rec.Flags, "--dtype", "float16")
	}

	if profile.MemoryGB >= 80 {
		rec.Notes = append(rec.Notes, "Enough VRAM for most 70B models without quantization")
	} else if profile.MemoryGB <= 32 {
		rec.Notes = append(rec.Notes, "Plan for 4-bit/8-bit quantization on >7B models")
		rec.Flags = append(rec.Flags, "--tensor-parallel-size", "2")
	}

	if model != nil {
		switch {
		case margin >= 32:
			rec.Notes = append(rec.Notes, fmt.Sprintf("~%d GiB headroom for %s", margin, model.ID))
		case margin >= 8:
			rec.Notes = append(rec.Notes, fmt.Sprintf("fits %s with modest headroom (~%d GiB)", model.ID, margin))
		case margin >= 0:
			rec.Notes = append(rec.Notes, fmt.Sprintf("VRAM margin for %s is tight (~%d GiB)", model.ID, margin))
		default:
			rec.Notes = append(rec.Notes, fmt.Sprintf("requires quantization or swap space (%d GiB short)", -margin))
			rec.Flags = append(rec.Flags, "--swap-space", "4")
		}
	}

	if strings.Contains(strings.ToLower(profile.Vendor), "amd") {
		rec.Notes = append(rec.Notes, "Ensure HIP_VISIBLE_DEVICES / ROCm env vars are set")
	}

	if hasFeature(profile, "pcie-gen4") {
		rec.Notes = append(rec.Notes, "Use --max-num-batched-tokens to stay within PCIe limits")
	}

	return rec
}

// Profiles returns the known GPU profiles in deterministic order.
func (e *Engine) Profiles() []GPUProfile {
	out := make([]GPUProfile, len(e.ordered))
	copy(out, e.ordered)
	return out
}

var sizePattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(b|m)`)

func estimateModelVRAM(model *catalog.Model) (int, string) {
	source := ""
	if model != nil {
		source = model.HFModelID
		if source == "" {
			source = model.ID
		}
	}
	matches := sizePattern.FindStringSubmatch(source)
	if len(matches) == 3 {
		value, _ := strconv.ParseFloat(matches[1], 64)
		unit := strings.ToLower(matches[2])
		var required float64
		switch unit {
		case "b":
			required = value*2.0 + 6
			if value >= 40 {
				required = math.Max(required, 80)
			}
		case "m":
			required = value*0.002 + 6
		}
		if required < 8 {
			required = 8
		}
		return int(math.Ceil(required)), fmt.Sprintf("derived from %s", matches[0])
	}

	return 16, "default requirement"
}

func buildSuggestions(profile GPUProfile) []string {
	var notes []string
	if profile.MemoryGB <= 16 {
		notes = append(notes, "Use quantization or swap space for >7B parameters")
	}
	if profile.MemoryGB >= 40 {
		notes = append(notes, "Enable tensor parallel for fastest throughput")
	}
	return notes
}

func hasFeature(profile GPUProfile, feature string) bool {
	target := strings.ToLower(feature)
	for _, f := range profile.Features {
		if strings.ToLower(f) == target {
			return true
		}
	}
	return false
}
