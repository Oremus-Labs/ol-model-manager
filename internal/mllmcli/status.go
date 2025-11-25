package mllmcli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show control-plane status",
	Run: func(cmd *cobra.Command, args []string) {
		client, ctx, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var summary SystemSummary
		if err := client.GetJSON("/system/summary", &summary); err == nil && summary.Version != "" {
			if err := writeOutput(cmd, summary); err != nil {
				exitWithError(cmd, err)
				return
			}
			if outputFormat == "json" {
				_ = printJSON(summary)
				return
			}
			printSummary(cmd, summary, ctx.Namespace)
			return
		}

		var info SystemInfo
		if err := client.GetJSON("/system/info", &info); err != nil {
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
		fmt.Fprintf(tw, "Version\t%s\n", info.Version)
		if info.Catalog != nil {
			fmt.Fprintf(tw, "Catalog Count\t%d\n", info.Catalog.Count)
			fmt.Fprintf(tw, "Catalog Source\t%s\n", info.Catalog.Source)
		}
		if info.Storage != nil {
			fmt.Fprintf(tw, "Weights Path\t%s\n", info.Weights.Path)
			fmt.Fprintf(tw, "Weights PVC\t%s\n", info.Weights.PVCName)
			fmt.Fprintf(tw, "Used\t%s\n", info.Storage.UsedHuman)
			fmt.Fprintf(tw, "Capacity\t%s\n", info.Storage.TotalHuman)
		}
		fmt.Fprintf(tw, "Namespace\t%s\n", ctx.Namespace)
		flushTable(tw)
	},
}

type SystemInfo struct {
	Version string        `json:"version"`
	Catalog *CatalogInfo  `json:"catalog"`
	Weights WeightInfo    `json:"weights"`
	Storage *StorageStats `json:"storage"`
}

type CatalogInfo struct {
	Count  int    `json:"count"`
	Source string `json:"source"`
}

type WeightInfo struct {
	Path    string `json:"path"`
	PVCName string `json:"pvcName"`
}

type StorageStats struct {
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	TotalHuman string `json:"totalHuman"`
	UsedHuman  string `json:"usedHuman"`
}

type SystemSummary struct {
	Version     string         `json:"version"`
	Timestamp   time.Time      `json:"timestamp"`
	Catalog     SummaryCatalog `json:"catalog"`
	Weights     SummaryWeights `json:"weights"`
	Jobs        map[string]int `json:"jobs"`
	Queue       SummaryQueue   `json:"queue"`
	HuggingFace SummaryHF      `json:"huggingface"`
	Runtime     *RuntimeStatus `json:"runtime"`
	Alerts      []AlertSummary `json:"alerts"`
}

type SummaryCatalog struct {
	Count  int    `json:"count"`
	Source string `json:"source"`
}

type SummaryWeights struct {
	Path      string        `json:"path"`
	PVCName   string        `json:"pvcName"`
	Installed int           `json:"installed"`
	Usage     *StorageStats `json:"usage"`
}

type SummaryQueue struct {
	Depth int64 `json:"depth"`
}

type SummaryHF struct {
	CachedModels int `json:"cachedModels"`
}

type AlertSummary struct {
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

func printSummary(cmd *cobra.Command, summary SystemSummary, namespace string) {
	tw := newTable()
	fmt.Fprintf(tw, "Field\tValue\n")
	fmt.Fprintf(tw, "Version\t%s\n", summary.Version)
	fmt.Fprintf(tw, "Timestamp\t%s\n", summary.Timestamp.Format(time.RFC3339))
	fmt.Fprintf(tw, "Catalog Count\t%d\n", summary.Catalog.Count)
	fmt.Fprintf(tw, "Catalog Source\t%s\n", summary.Catalog.Source)
	fmt.Fprintf(tw, "Weights Path\t%s\n", summary.Weights.Path)
	fmt.Fprintf(tw, "Weights PVC\t%s\n", summary.Weights.PVCName)
	if summary.Weights.Usage != nil {
		fmt.Fprintf(tw, "Weights Used\t%s\n", summary.Weights.Usage.UsedHuman)
		fmt.Fprintf(tw, "Weights Capacity\t%s\n", summary.Weights.Usage.TotalHuman)
	}
	if summary.Weights.Installed > 0 {
		fmt.Fprintf(tw, "Models Installed\t%d\n", summary.Weights.Installed)
	}
	if summary.Queue.Depth > 0 {
		fmt.Fprintf(tw, "Queue Depth\t%d\n", summary.Queue.Depth)
	}
	if summary.HuggingFace.CachedModels > 0 {
		fmt.Fprintf(tw, "HF Cached Models\t%d\n", summary.HuggingFace.CachedModels)
	}
	fmt.Fprintf(tw, "Namespace\t%s\n", namespace)
	flushTable(tw)

	if len(summary.Jobs) > 0 {
		jobsTable := newTable()
		fmt.Fprintf(jobsTable, "Job Status\tCount\n")
		for status, count := range summary.Jobs {
			fmt.Fprintf(jobsTable, "%s\t%d\n", status, count)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nJob Queue:")
		flushTable(jobsTable)
	}

	if len(summary.Alerts) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nAlerts:")
		for _, alert := range summary.Alerts {
			fmt.Fprintf(cmd.OutOrStdout(), "- [%s] %s\n", strings.ToUpper(alert.Level), alert.Message)
		}
	}
}
