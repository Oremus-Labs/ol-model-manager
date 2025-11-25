package mllmcli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Manage notification channels",
}

var notifyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notification channels",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Notifications []Notification `json:"notifications"`
		}
		if err := client.GetJSON("/notifications", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Notifications); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Notifications) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No notification channels configured.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "NAME\tTYPE\tTARGET\tUPDATED\n")
		for _, n := range resp.Notifications {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.Name, n.Type, n.Target, formatTimestamp(n.UpdatedAt))
		}
		flushTable(tw)
	},
}

var (
	notifyAddType   string
	notifyAddTarget string
	notifyAddMeta   []string
)

var notifyAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create or update a notification channel",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if notifyAddTarget == "" {
			exitWithError(cmd, fmt.Errorf("--target is required"))
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		meta, err := parseKeyValuePairs(notifyAddMeta)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]interface{}{
			"type":     notifyAddType,
			"target":   notifyAddTarget,
			"metadata": meta,
		}
		var record Notification
		if err := client.PutJSON("/notifications/"+args[0], payload, &record); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, record); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Channel %s -> %s configured.\n", record.Name, record.Target)
	},
}

var notifyDeleteForce bool

var notifyDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a notification channel",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if !notifyDeleteForce {
			ok, err := confirmPrompt(fmt.Sprintf("Delete notification %s? [y/N]: ", args[0]), cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				exitWithError(cmd, err)
				return
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
				return
			}
		}
		if err := client.Delete("/notifications/" + args[0]); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Notification %s deleted.\n", args[0])
	},
}

var notifyTestMessage string

var notifyTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send a test notification via the control plane",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"message": strings.TrimSpace(notifyTestMessage)}
		if err := client.PostJSON("/notifications/test", payload, nil); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Notification dispatched.")
	},
}

var notifyHistoryLimit int

var notifyHistoryCmd = &cobra.Command{
	Use:   "history <name>",
	Short: "Show recent history for a notification channel",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		path := fmt.Sprintf("/notifications/%s/history?limit=%d", args[0], notifyHistoryLimit)
		var resp struct {
			History []HistoryEntry `json:"history"`
		}
		if err := client.GetJSON(path, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.History); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.History) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No notification history for %s.\n", args[0])
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "EVENT\tWHEN\tMESSAGE\n")
		for _, entry := range resp.History {
			msg := "-"
			if entry.Metadata != nil {
				if val, ok := entry.Metadata["message"].(string); ok {
					msg = val
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", entry.Event, formatTimestamp(entry.CreatedAt), msg)
		}
		flushTable(tw)
	},
}

func init() {
	notifyAddCmd.Flags().StringVar(&notifyAddType, "type", "slack-webhook", "Channel type (slack-webhook, webhook, etc.)")
	notifyAddCmd.Flags().StringVar(&notifyAddTarget, "target", "", "Channel target URL or identifier")
	notifyAddCmd.Flags().StringSliceVar(&notifyAddMeta, "meta", nil, "key=value metadata pairs (repeatable)")
	notifyDeleteCmd.Flags().BoolVar(&notifyDeleteForce, "yes", false, "Skip confirmation prompt")
	notifyTestCmd.Flags().StringVar(&notifyTestMessage, "message", "Model Manager notification test", "Message body to send")
	notifyHistoryCmd.Flags().IntVar(&notifyHistoryLimit, "limit", 20, "Number of history entries to return")

	notifyCmd.AddCommand(notifyListCmd)
	notifyCmd.AddCommand(notifyAddCmd)
	notifyCmd.AddCommand(notifyDeleteCmd)
	notifyCmd.AddCommand(notifyTestCmd)
	notifyCmd.AddCommand(notifyHistoryCmd)
}

type Notification struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Target    string            `json:"target"`
	Metadata  map[string]string `json:"metadata"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

type HistoryEntry struct {
	ID        string                 `json:"id"`
	Event     string                 `json:"event"`
	ModelID   string                 `json:"modelId"`
	Metadata  map[string]interface{} `json:"metadata"`
	CreatedAt time.Time              `json:"createdAt"`
}

func parseKeyValuePairs(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	data := make(map[string]string)
	for _, kv := range entries {
		key, value, err := splitAssignment(kv)
		if err != nil {
			return nil, err
		}
		data[key] = value
	}
	return data, nil
}
