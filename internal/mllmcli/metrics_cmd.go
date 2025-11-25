package mllmcli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Inspect aggregated metrics",
}

var metricsTopCmd = &cobra.Command{
	Use:   "top",
	Short: "Show summarized metrics from the control plane",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp map[string]interface{}
		if err := client.GetJSON("/metrics/summary", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		printMetricsSummary(cmd, resp)
	},
}

func init() {
	metricsCmd.AddCommand(metricsTopCmd)
}

func printMetricsSummary(cmd *cobra.Command, payload map[string]interface{}) {
	if queue, ok := payload["queue"].(map[string]interface{}); ok {
		if depth, ok := queue["depth"]; ok {
			fmt.Fprintf(cmd.OutOrStdout(), "Queue depth: %v\n", depth)
		}
	}
	if jobs, ok := payload["jobs"].(map[string]interface{}); ok {
		fmt.Fprintln(cmd.OutOrStdout(), "\nJob status counts:")
		tw := newTable()
		fmt.Fprintf(tw, "STATUS\tCOUNT\n")
		for status, count := range jobs {
			fmt.Fprintf(tw, "%s\t%v\n", status, count)
		}
		flushTable(tw)
	}
	if alerts, ok := payload["alerts"].([]interface{}); ok && len(alerts) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nActive alerts:")
		for _, raw := range alerts {
			if alert, ok := raw.(map[string]interface{}); ok {
				fmt.Fprintf(cmd.OutOrStdout(), "- [%s] %s\n", alert["level"], alert["message"])
			}
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "\nNo active alerts.")
	}
	if prom, ok := payload["prometheus"].(map[string]interface{}); ok && len(prom) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nPrometheus gauges:")
		tw := newTable()
		fmt.Fprintf(tw, "METRIC\tVALUE\n")
		for name, value := range prom {
			fmt.Fprintf(tw, "%s\t%v\n", name, value)
		}
		flushTable(tw)
	}
}
