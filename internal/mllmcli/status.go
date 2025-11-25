package mllmcli

import (
	"fmt"

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
