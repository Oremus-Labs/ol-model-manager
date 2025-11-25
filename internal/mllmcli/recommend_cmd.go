package mllmcli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var recommendCmd = &cobra.Command{
	Use:   "recommend",
	Short: "GPU placement and runtime recommendations",
}

var recommendProfilesCmd = &cobra.Command{
	Use:   "profiles",
	Short: "List GPU profiles configured for the cluster",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Profiles []GPUProfile `json:"profiles"`
		}
		if err := client.GetJSON("/recommendations/profiles", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Profiles); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(resp.Profiles)
			return
		}
		if len(resp.Profiles) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No GPU profiles configured.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "GPU TYPE\tMEMORY\tVENDOR\tFEATURES\n")
		for _, profile := range resp.Profiles {
			fmt.Fprintf(tw, "%s\t%d GiB\t%s\t%s\n", profile.Name, profile.MemoryGB, profile.Vendor, strings.Join(profile.Features, ", "))
		}
		flushTable(tw)
	},
}

var recommendGPUCmd = &cobra.Command{
	Use:   "gpu <gpu-type>",
	Short: "Show runtime flag recommendations for a GPU type",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var rec Recommendation
		if err := client.GetJSON("/recommendations/"+args[0], &rec); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, rec); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(rec)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "GPU: %s (%d GiB)\n", rec.GPUType, rec.MemoryGB)
		if len(rec.Flags) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Flags: %s\n", strings.Join(rec.Flags, " "))
		}
		if len(rec.Notes) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Notes:")
			for _, note := range rec.Notes {
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", note)
			}
		}
	},
}

var recommendCompatGPU string

var recommendCompatCmd = &cobra.Command{
	Use:   "compatibility <model-id>",
	Short: "Check GPU compatibility for a catalog model",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		path := fmt.Sprintf("/models/%s/compatibility", args[0])
		if recommendCompatGPU != "" {
			path += "?gpuType=" + recommendCompatGPU
		}
		var report CompatibilityReport
		if err := client.GetJSON(path, &report); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, report); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(report)
			return
		}
		if recommendCompatGPU != "" {
			verdict := "NOT COMPATIBLE"
			if report.Compatible {
				verdict = "compatible"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s on %s: %s (%s)\n", report.ModelID, report.GPUType, verdict, report.Reason)
			if len(report.Suggestions) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Suggestions:")
				for _, s := range report.Suggestions {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", s)
				}
			}
			return
		}
		if len(report.Candidates) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Estimated VRAM required: %d GiB\n", report.EstimatedVRAMGB)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "GPU\tCOMPATIBLE\tREASON\n")
		for _, cand := range report.Candidates {
			status := "no"
			if cand.Compatible {
				status = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", cand.GPU, status, cand.Reason)
		}
		flushTable(tw)
	},
}

func init() {
	recommendCompatCmd.Flags().StringVar(&recommendCompatGPU, "gpu", "", "Specific GPU type to evaluate")
	recommendCmd.AddCommand(recommendProfilesCmd)
	recommendCmd.AddCommand(recommendGPUCmd)
	recommendCmd.AddCommand(recommendCompatCmd)
}

type GPUProfile struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	MemoryGB    int      `json:"memoryGB"`
	Vendor      string   `json:"vendor"`
	Features    []string `json:"features"`
}

type Recommendation struct {
	GPUType  string   `json:"gpuType"`
	MemoryGB int      `json:"memoryGB"`
	Flags    []string `json:"flags"`
	Notes    []string `json:"notes"`
}

type CompatibilityReport struct {
	ModelID         string      `json:"modelId"`
	GPUType         string      `json:"gpuType"`
	EstimatedVRAMGB int         `json:"estimatedVramGb"`
	Reason          string      `json:"reason"`
	Compatible      bool        `json:"compatible"`
	Candidates      []Candidate `json:"candidates"`
	Suggestions     []string    `json:"suggestions"`
}

type Candidate struct {
	GPU        string `json:"gpu"`
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason"`
}
