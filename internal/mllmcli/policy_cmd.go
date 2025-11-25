package mllmcli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Manage control-plane policies",
}

var policyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored policies",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Policies []Policy `json:"policies"`
		}
		if err := client.GetJSON("/policies", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Policies); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Policies) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No policies configured.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "NAME\tUPDATED\n")
		for _, p := range resp.Policies {
			fmt.Fprintf(tw, "%s\t%s\n", p.Name, formatTimestamp(p.UpdatedAt))
		}
		flushTable(tw)
	},
}

var policyFile string

var policyApplyCmd = &cobra.Command{
	Use:   "apply <name>",
	Short: "Create or update a policy",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		document, err := readPolicyDocument(policyFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"document": document}
		var policy Policy
		if err := client.PutJSON("/policies/"+args[0], payload, &policy); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, policy); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Policy %s updated.\n", policy.Name)
	},
}

var policyDeleteForce bool

var policyDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a policy",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if !policyDeleteForce {
			ok, err := confirmPrompt(fmt.Sprintf("Delete policy %s? [y/N]: ", args[0]), cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				exitWithError(cmd, err)
				return
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
				return
			}
		}
		if err := client.Delete("/policies/" + args[0]); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Policy %s deleted.\n", args[0])
	},
}

func init() {
	policyApplyCmd.Flags().StringVar(&policyFile, "file", "", "Path to policy file (defaults to STDIN)")
	policyDeleteCmd.Flags().BoolVar(&policyDeleteForce, "yes", false, "Skip confirmation")

	policyCmd.AddCommand(policyListCmd)
	policyCmd.AddCommand(policyApplyCmd)
	policyCmd.AddCommand(policyDeleteCmd)
}

type Policy struct {
	Name      string    `json:"name"`
	Document  string    `json:"document"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func readPolicyDocument(path string) (string, error) {
	if path == "" || path == "-" {
		bytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(bytes), nil
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
