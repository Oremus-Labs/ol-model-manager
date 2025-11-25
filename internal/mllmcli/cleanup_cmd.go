package mllmcli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Cleanup cached resources",
}

var cleanupWeightNames []string

var cleanupWeightsCmd = &cobra.Command{
	Use:   "weights",
	Short: "Delete cached weight directories by name",
	Run: func(cmd *cobra.Command, args []string) {
		if len(cleanupWeightNames) == 0 {
			exitWithError(cmd, fmt.Errorf("at least one --name is required"))
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]interface{}{
			"names": cleanupWeightNames,
		}
		var resp struct {
			Results map[string]string `json:"results"`
		}
		if err := client.PostJSON("/cleanup/weights", payload, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Results); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "NAME\tRESULT\n")
		for name, result := range resp.Results {
			fmt.Fprintf(tw, "%s\t%s\n", name, result)
		}
		flushTable(tw)
	},
}

func init() {
	cleanupWeightsCmd.Flags().StringSliceVar(&cleanupWeightNames, "name", nil, "Weight directory to delete (repeatable)")
	cleanupCmd.AddCommand(cleanupWeightsCmd)
}
