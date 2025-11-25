package mllmcli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var modelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Manage catalog models",
}

var modelsListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List catalog models",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var models []ModelSummary
		if err := client.GetJSON("/models", &models); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, models); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(models)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "ID\tDISPLAY NAME\tRUNTIME\tHF MODEL\n")
		for _, m := range models {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				m.ID,
				m.DisplayName,
				m.Runtime,
				m.HFModelID)
		}
		flushTable(tw)
	},
}

var modelsGetCmd = &cobra.Command{
	Use:   "get <model-id>",
	Short: "Describe a model",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		path := fmt.Sprintf("/models/%s", args[0])
		var model Model
		if err := client.GetJSON(path, &model); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, model); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(model)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "Field\tValue\n")
		fmt.Fprintf(tw, "ID\t%s\n", model.ID)
		fmt.Fprintf(tw, "Display Name\t%s\n", model.DisplayName)
		fmt.Fprintf(tw, "Runtime\t%s\n", model.Runtime)
		fmt.Fprintf(tw, "HF Model ID\t%s\n", model.HFModelID)
		fmt.Fprintf(tw, "Served Name\t%s\n", model.ServedModelName)
		fmt.Fprintf(tw, "Storage URI\t%s\n", model.StorageURI)
		fmt.Fprintf(tw, "Env\t%s\n", joinEnv(model.Env))
		flushTable(tw)
	},
}

var (
	initID          string
	initDisplayName string
	initHFModelID   string
	initRuntime     string
	initOutputPath  string
)

var modelsInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a starter YAML manifest",
	Run: func(cmd *cobra.Command, args []string) {
		if initID == "" {
			exitWithError(cmd, fmt.Errorf("--id is required"))
			return
		}
		if initHFModelID == "" {
			initHFModelID = initID
		}
		if initDisplayName == "" {
			initDisplayName = initID
		}
		doc := map[string]interface{}{
			"id":          initID,
			"displayName": initDisplayName,
			"hfModelId":   initHFModelID,
			"runtime":     initRuntime,
			"storageUri":  fmt.Sprintf("pvc://venus-model-storage/%s", initID),
			"env": []map[string]string{
				{"name": "MODEL_ID", "value": initHFModelID},
				{"name": "MODEL_DIR", "value": "/mnt/models"},
			},
			"vllm": map[string]interface{}{
				"tensorParallelSize":   1,
				"dtype":                "float16",
				"gpuMemoryUtilization": 0.9,
				"maxModelLen":          4096,
			},
		}
		if err := writeYAMLDocument(doc, initOutputPath, cmd); err != nil {
			exitWithError(cmd, err)
		}
	},
}

var modelsValidateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Validate a YAML manifest against the control plane",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload, _, err := loadModelDocument(args[0])
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var result ValidationResult
		if err := client.PostRawJSON("/catalog/validate", payload, &result); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, result); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(result)
			return
		}
		printValidationSummary(result, cmd)
		if !result.Valid {
			exitWithError(cmd, fmt.Errorf("validation failed"))
		}
	},
}

var applyActivate bool

var modelsApplyCmd = &cobra.Command{
	Use:   "apply <file>",
	Short: "Validate a YAML manifest (and optionally activate it)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload, model, err := loadModelDocument(args[0])
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var result ValidationResult
		if err := client.PostRawJSON("/catalog/validate", payload, &result); err != nil {
			exitWithError(cmd, err)
			return
		}
		if !result.Valid {
			printValidationSummary(result, cmd)
			exitWithError(cmd, fmt.Errorf("validation failed"))
			return
		}
		if outputFormat != "json" {
			printValidationSummary(result, cmd)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Model %s validated successfully.\n", model.ID)
		if applyActivate {
			if model.ID == "" {
				exitWithError(cmd, fmt.Errorf("manifest is missing 'id'"))
				return
			}
			payload := map[string]string{"id": model.ID}
			if err := client.PostJSON("/models/activate", payload, nil); err != nil {
				exitWithError(cmd, err)
				return
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Activation request submitted for %s.\n", model.ID)
		}
	},
}

var modelsDiffCmd = &cobra.Command{
	Use:   "diff <file>",
	Short: "Show differences between YAML manifest and the live catalog entry",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload, model, err := loadModelDocument(args[0])
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if model.ID == "" {
			exitWithError(cmd, fmt.Errorf("manifest missing id"))
			return
		}
		var current interface{}
		if err := client.GetJSON(fmt.Sprintf("/models/%s", model.ID), &current); err != nil {
			exitWithError(cmd, err)
			return
		}
		var desired interface{}
		if err := json.Unmarshal(payload, &desired); err != nil {
			exitWithError(cmd, err)
			return
		}
		diff := cmp.Diff(current, desired)
		if diff == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "No differences detected.")
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), diff)
	},
}

var modelsDeactivateCmd = &cobra.Command{
	Use:   "deactivate",
	Short: "Deactivate the currently active model",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := client.PostJSON("/models/deactivate", map[string]string{}, nil); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Active model deactivated.")
	},
}

func init() {
	modelsInitCmd.Flags().StringVar(&initID, "id", "", "Model ID")
	modelsInitCmd.Flags().StringVar(&initDisplayName, "display-name", "", "Display name")
	modelsInitCmd.Flags().StringVar(&initHFModelID, "hf-model-id", "", "Hugging Face model ID")
	modelsInitCmd.Flags().StringVar(&initRuntime, "runtime", "vllm-runtime", "Runtime name")
	modelsInitCmd.Flags().StringVarP(&initOutputPath, "output", "f", "", "File path to write (defaults to stdout)")
	modelsApplyCmd.Flags().BoolVar(&applyActivate, "activate", false, "Activate immediately after validation")
	modelsCmd.AddCommand(modelsListCmd)
	modelsCmd.AddCommand(modelsGetCmd)
	modelsCmd.AddCommand(modelsInitCmd)
	modelsCmd.AddCommand(modelsValidateCmd)
	modelsCmd.AddCommand(modelsApplyCmd)
	modelsCmd.AddCommand(modelsDiffCmd)
	modelsCmd.AddCommand(modelsDeactivateCmd)
}

type ModelSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	HFModelID   string `json:"hfModelId"`
	Runtime     string `json:"runtime"`
}

type Model struct {
	ModelSummary
	ServedModelName string   `json:"servedModelName"`
	StorageURI      string   `json:"storageUri"`
	Env             []EnvVar `json:"env"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func joinEnv(env []EnvVar) string {
	if len(env) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(env))
	for _, e := range env {
		if e.Value != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", e.Name, e.Value))
		} else {
			parts = append(parts, e.Name)
		}
	}
	return strings.Join(parts, ", ")
}

type ValidationResult struct {
	Valid       bool              `json:"valid"`
	Errors      []string          `json:"errors,omitempty"`
	Checks      []ValidationCheck `json:"checks,omitempty"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
}

type ValidationCheck struct {
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Message  string            `json:"message"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func writeYAMLDocument(doc interface{}, outputPath string, cmd *cobra.Command) error {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if outputPath == "" || outputPath == "-" {
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}
	dir := filepath.Dir(outputPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(outputPath, data, 0o644)
}

func loadModelDocument(path string) ([]byte, *Model, error) {
	data, err := readInputFile(path)
	if err != nil {
		return nil, nil, err
	}
	jsonPayload, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert YAML to JSON: %w", err)
	}
	var model Model
	if err := yaml.Unmarshal(data, &model); err != nil {
		return nil, nil, fmt.Errorf("failed to parse manifest: %w", err)
	}
	return jsonPayload, &model, nil
}

func readInputFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(filepath.Clean(path))
}

func printValidationSummary(result ValidationResult, cmd *cobra.Command) {
	fmt.Fprintf(cmd.OutOrStdout(), "Valid: %t (generated %s)\n", result.Valid, result.GeneratedAt.Format(time.RFC3339))
	if len(result.Errors) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Errors:")
		for _, err := range result.Errors {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", err)
		}
	}
	if len(result.Checks) == 0 {
		return
	}
	tw := newTable()
	fmt.Fprintf(tw, "CHECK\tSTATUS\tMESSAGE\n")
	for _, check := range result.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", check.Name, check.Status, check.Message)
	}
	flushTable(tw)
}
