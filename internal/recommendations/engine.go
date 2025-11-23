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
	Name     string   `json:"name"`
	MemoryGB int      `json:"memoryGB"`
	Vendor   string   `json:"vendor,omitempty"`
	Features []string `json:"features,omitempty"`
}

// Engine produces compatibility reports and runtime recommendations.
type Engine struct {
	profiles map[string]GPUProfile
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
	GPUType string   `json:"gpuType"`
	Flags   []string `json:"flags"`
	Notes   []string `json:"notes"`
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
	for k, v := range profiles {
		copies[k] = v
	}
	return &Engine{profiles: copies}
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

	keys := make([]string, 0, len(e.profiles))
	for name := range e.profiles {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, key := range keys {
		profile := e.profiles[key]
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
	profile, ok := e.profiles[strings.ToLower(gpuType)]
	if !ok {
		return Recommendation{GPUType: gpuType, Notes: []string{"unknown gpu type"}}
	}

	rec := Recommendation{GPUType: profile.Name}
	if profile.MemoryGB <= 24 {
		rec.Flags = append(rec.Flags, "--dtype", "float16")
		rec.Notes = append(rec.Notes, "Prefer 4-bit/8-bit weights for models >=7B")
	} else {
		rec.Flags = append(rec.Flags, "--dtype", "bfloat16")
	}

	if profile.MemoryGB <= 16 {
		rec.Notes = append(rec.Notes, "Enable tensor parallel when running multi-billion parameter models")
	}

	if strings.Contains(strings.ToLower(profile.Vendor), "amd") {
		rec.Notes = append(rec.Notes, "Set HIP_VISIBLE_DEVICES and ensure ROCm-specific env vars are present")
	}

	return rec
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
