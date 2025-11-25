package mllmcli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Inspect recent activity",
}

var (
	auditSince string
	auditLimit int
	auditEvent string
	auditModel string
)

var auditListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent audit events",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		params := []string{}
		if auditLimit > 0 {
			params = append(params, fmt.Sprintf("limit=%d", auditLimit))
		}
		if auditSince != "" {
			params = append(params, "since="+auditSince)
		}
		if auditEvent != "" {
			params = append(params, "event="+auditEvent)
		}
		if auditModel != "" {
			params = append(params, "modelId="+auditModel)
		}
		path := "/history"
		if len(params) > 0 {
			path += "?" + strings.Join(params, "&")
		}
		var resp struct {
			Events []AuditEvent `json:"events"`
		}
		if err := client.GetJSON(path, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Events); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Events) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No audit events found.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "TIME\tEVENT\tMODEL\tDETAILS\n")
		for _, evt := range resp.Events {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", formatTimestamp(evt.CreatedAt), evt.Event, evt.ModelID, evt.Metadata)
		}
		flushTable(tw)
	},
}

func init() {
	auditListCmd.Flags().StringVar(&auditSince, "since", "24h", "Only show events since duration or RFC3339 timestamp")
	auditListCmd.Flags().IntVar(&auditLimit, "limit", 50, "Maximum events to return")
	auditListCmd.Flags().StringVar(&auditEvent, "event", "", "Filter by event name")
	auditListCmd.Flags().StringVar(&auditModel, "model", "", "Filter by model ID")
	auditCmd.AddCommand(auditListCmd)
}

type AuditEvent struct {
	Event     string                 `json:"event"`
	ModelID   string                 `json:"modelId"`
	Metadata  map[string]interface{} `json:"metadata"`
	CreatedAt time.Time              `json:"createdAt"`
}
