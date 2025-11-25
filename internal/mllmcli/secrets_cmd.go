package mllmcli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage Kubernetes secrets used by Model Manager",
}

var secretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List managed secrets",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Secrets []SecretMeta `json:"secrets"`
		}
		if err := client.GetJSON("/secrets", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Secrets); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Secrets) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No managed secrets found.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "NAME\tKEYS\tUPDATED\n")
		for _, sec := range resp.Secrets {
			keys := strings.Join(sec.Keys, ",")
			updated := formatTimestamp(sec.UpdatedAt)
			fmt.Fprintf(tw, "%s\t%s\t%s\n", sec.Name, keys, updated)
		}
		flushTable(tw)
	},
}

var secretsGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show a secret's values",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var record SecretRecord
		if err := client.GetJSON("/secrets/"+args[0], &record); err != nil {
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
		tw := newTable()
		fmt.Fprintf(tw, "KEY\tVALUE\n")
		for key, value := range record.Data {
			fmt.Fprintf(tw, "%s\t%s\n", key, value)
		}
		flushTable(tw)
	},
}

var (
	secretLiterals []string
	secretFiles    []string
)

var secretsSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Create or update a secret",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		data, err := buildSecretData(secretLiterals, secretFiles)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if len(data) == 0 {
			exitWithError(cmd, fmt.Errorf("no secret data provided; use --data or --from-file"))
			return
		}
		payload := map[string]interface{}{
			"data": data,
		}
		var record SecretRecord
		if err := client.PutJSON("/secrets/"+args[0], payload, &record); err != nil {
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
		fmt.Fprintf(cmd.OutOrStdout(), "Secret %s updated (%d keys)\n", record.Name, len(record.Data))
	},
}

var secretsDeleteForce bool

var secretsDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a managed secret",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if !secretsDeleteForce {
			ok, err := confirmPrompt(fmt.Sprintf("Delete secret %s? [y/N]: ", args[0]), cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				exitWithError(cmd, err)
				return
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
				return
			}
		}
		if err := client.Delete("/secrets/" + args[0]); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Secret %s deleted.\n", args[0])
	},
}

func init() {
	secretsSetCmd.Flags().StringSliceVar(&secretLiterals, "data", nil, "Literal key=value pairs to include (may be repeated)")
	secretsSetCmd.Flags().StringSliceVar(&secretFiles, "from-file", nil, "key=path entries to read from file (may be repeated)")
	secretsDeleteCmd.Flags().BoolVar(&secretsDeleteForce, "yes", false, "Skip confirmation prompt")

	secretsCmd.AddCommand(secretsListCmd)
	secretsCmd.AddCommand(secretsGetCmd)
	secretsCmd.AddCommand(secretsSetCmd)
	secretsCmd.AddCommand(secretsDeleteCmd)
}

func buildSecretData(literals, files []string) (map[string]string, error) {
	data := make(map[string]string)
	for _, lit := range literals {
		key, value, err := splitAssignment(lit)
		if err != nil {
			return nil, err
		}
		data[key] = value
	}
	for _, spec := range files {
		key, path, err := splitAssignment(spec)
		if err != nil {
			return nil, err
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data[key] = strings.TrimRight(string(bytes), "\r\n")
	}
	return data, nil
}

func splitAssignment(input string) (string, string, error) {
	parts := strings.SplitN(input, "=", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid assignment %q (expected key=value)", input)
	}
	key := strings.TrimSpace(parts[0])
	value := parts[1]
	if key == "" {
		return "", "", fmt.Errorf("invalid assignment %q (empty key)", input)
	}
	return key, value, nil
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return formatTimestamp(*t)
}

type SecretMeta struct {
	Name      string    `json:"name"`
	Keys      []string  `json:"keys"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SecretRecord struct {
	Name      string            `json:"name"`
	Data      map[string]string `json:"data"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}
