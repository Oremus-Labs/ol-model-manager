package mllmcli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/weights"
	"github.com/spf13/cobra"
)

var weightsCmd = &cobra.Command{
	Use:   "weights",
	Short: "Inspect cached weights",
}

var weightsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List cached weight directories",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Weights []WeightRecord `json:"weights"`
		}
		if err := client.GetJSON("/weights", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Weights); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(resp.Weights)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "NAME\tSIZE\tFILES\tUPDATED\tMODEL\n")
		for _, w := range resp.Weights {
			model := w.HFModelID
			if model == "" {
				model = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n",
				w.Name,
				w.SizeHuman,
				w.FileCount,
				relativeTime(w.ModifiedTime),
				model)
		}
		flushTable(tw)
	},
}

var weightsUsageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show PVC usage statistics",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var stats WeightStorageStats
		if err := client.GetJSON("/weights/usage", &stats); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, stats); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(stats)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "Metric\tValue\n")
		fmt.Fprintf(tw, "Total\t%s\n", stats.TotalHuman)
		fmt.Fprintf(tw, "Used\t%s\n", stats.UsedHuman)
		fmt.Fprintf(tw, "Available\t%s\n", stats.AvailableHuman)
		fmt.Fprintf(tw, "Models\t%d\n", stats.ModelCount)
		flushTable(tw)
	},
}

var weightsInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show detailed info for a weight directory",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		query := url.Values{}
		query.Set("name", args[0])
		var info WeightRecord
		if err := client.GetJSON("/weights/info?"+query.Encode(), &info); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, info); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(info)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "Field\tValue\n")
		fmt.Fprintf(tw, "Name\t%s\n", info.Name)
		fmt.Fprintf(tw, "Path\t%s\n", info.Path)
		fmt.Fprintf(tw, "HF Model ID\t%s\n", info.HFModelID)
		fmt.Fprintf(tw, "Size\t%s (%d bytes)\n", info.SizeHuman, info.SizeBytes)
		fmt.Fprintf(tw, "Files\t%d\n", info.FileCount)
		fmt.Fprintf(tw, "Last Modified\t%s\n", info.ModifiedTime.Format(time.RFC3339))
		fmt.Fprintf(tw, "Installed\t%s\n", info.InstalledAt.Format(time.RFC3339))
		flushTable(tw)
	},
}

var (
	installRevision  string
	installTarget    string
	installOverwrite bool
	installWatch     bool
	installFiles     []string
	installPreempt   bool
)

var weightsInstallCmd = &cobra.Command{
	Use:   "install <hf-model-id>",
	Short: "Install Hugging Face weights onto the PVC",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		reactivate, err := ensureGPUCapacity(cmd, client, installPreempt)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if reactivate != nil {
			defer reactivate()
		}
		req := installWeightsPayload{
			HFModelID: args[0],
			Revision:  installRevision,
			Target:    installTarget,
			Files:     installFiles,
			Overwrite: installOverwrite,
		}
		if req.Target == "" {
			if target, err := weights.CanonicalTarget(req.HFModelID, ""); err == nil {
				req.Target = target
			}
		}
		var resp weightInstallResponse
		if err := client.PostJSON("/weights/install", req, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if resp.Job != nil && resp.Job.ID != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Job %s queued to install %s into %s\n", resp.Job.ID, args[0], resp.Target)
			if installWatch {
				if err := watchJob(cmd.Context(), cmd, client, resp.Job.ID); err != nil {
					exitWithError(cmd, err)
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Follow progress: %s/jobs/%s\n", strings.TrimRight(client.BaseURL, "/"), resp.Job.ID)
			}
			return
		}
		if resp.Weights != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Weights installed at %s (%s)\n", resp.Weights.Path, resp.Weights.SizeHuman)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Installation request submitted.")
	},
}

var weightsDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete cached weights",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"name": args[0]}
		if err := client.DeleteJSON("/weights", payload, nil); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted weights for %s\n", args[0])
	},
}

func init() {
	weightsInstallCmd.Flags().StringVar(&installRevision, "revision", "", "Specific Hugging Face revision to install")
	weightsInstallCmd.Flags().StringVar(&installTarget, "target", "", "Override target directory name (defaults to the HF model ID)")
	weightsInstallCmd.Flags().BoolVar(&installOverwrite, "overwrite", false, "Overwrite the target directory if it exists")
	weightsInstallCmd.Flags().StringSliceVar(&installFiles, "file", nil, "Restrict download to specific files (repeatable)")
	weightsInstallCmd.Flags().BoolVar(&installWatch, "watch", false, "Wait for the install job to finish")
	weightsInstallCmd.Flags().BoolVar(&installPreempt, "preempt-active", false, "Automatically deactivate the active model if GPUs are unavailable")
	weightsCmd.AddCommand(weightsListCmd)
	weightsCmd.AddCommand(weightsUsageCmd)
	weightsCmd.AddCommand(weightsInfoCmd)
	weightsCmd.AddCommand(weightsInstallCmd)
	weightsCmd.AddCommand(weightsDeleteCmd)
}

type WeightRecord struct {
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"sizeBytes"`
	SizeHuman    string    `json:"sizeHuman"`
	ModifiedTime time.Time `json:"modifiedTime"`
	FileCount    int       `json:"fileCount"`
	HFModelID    string    `json:"hfModelId"`
	Revision     string    `json:"revision,omitempty"`
	InstalledAt  time.Time `json:"installedAt,omitempty"`
}

type WeightStorageStats struct {
	TotalBytes     int64          `json:"totalBytes"`
	TotalHuman     string         `json:"totalHuman"`
	UsedBytes      int64          `json:"usedBytes"`
	UsedHuman      string         `json:"usedHuman"`
	AvailableBytes int64          `json:"availableBytes"`
	AvailableHuman string         `json:"availableHuman"`
	ModelCount     int            `json:"modelCount"`
	Models         []WeightRecord `json:"models"`
}

type installWeightsPayload struct {
	HFModelID string   `json:"hfModelId"`
	Revision  string   `json:"revision,omitempty"`
	Target    string   `json:"target,omitempty"`
	Files     []string `json:"files,omitempty"`
	Overwrite bool     `json:"overwrite"`
}

type weightInstallResponse struct {
	Status             string        `json:"status"`
	Job                *Job          `json:"job"`
	JobURL             string        `json:"jobUrl"`
	Target             string        `json:"target"`
	StorageURI         string        `json:"storageUri"`
	InferenceModelPath string        `json:"inferenceModelPath"`
	Weights            *WeightRecord `json:"weights"`
}

func ensureGPUCapacity(cmd *cobra.Command, client *Client, auto bool) (func(), error) {
	var reactivate func()
	status, err := fetchRuntimeStatus(client)
	if err != nil {
		return nil, nil
	}
	reason, busy := detectGPUContention(status)
	if !busy {
		return nil, nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "GPU scheduling blocked (%s).\n", reason)
	proceed := auto
	if !auto {
		answer, err := confirmPrompt("Deactivate the active model to free the GPU? [y/N]: ", cmd.InOrStdin(), cmd.OutOrStdout())
		if err != nil {
			return nil, err
		}
		proceed = answer
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Preempting active model as requested...")
	}
	if !proceed {
		return nil, fmt.Errorf("operation aborted: GPU is fully allocated")
	}

	activeModel, _ := fetchActiveModelID(client)
	if err := client.PostJSON("/models/deactivate", map[string]string{}, nil); err != nil {
		return nil, fmt.Errorf("failed to deactivate active model: %w", err)
	}

	reactivate = func() {
		if activeModel == "" {
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Reactivating model %s...\n", activeModel)
		if err := client.PostJSON("/models/activate", map[string]string{"id": activeModel}, nil); err != nil {
			printErrorLine("Failed to reactivate %s: %v", activeModel, err)
		}
		activeModel = ""
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Waiting for pending GPU workloads to drain...")
	waitCtx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err := waitForGPUClear(waitCtx, client); err != nil {
		if reactivate != nil {
			reactivate()
		}
		return nil, fmt.Errorf("GPU did not become available: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "GPU available. Continuing with installation.")
	return reactivate, nil
}
