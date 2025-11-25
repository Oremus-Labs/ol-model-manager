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

var (
	policyFile            string
	policyVersionsLimit   int
	policyBundleOutput    string
	policyRollbackVersion int
)

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

var policyGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show a policy",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var policy Policy
		if err := client.GetJSON("/policies/"+args[0], &policy); err != nil {
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
		fmt.Fprintf(cmd.OutOrStdout(), "%s (updated %s)\n%s\n", policy.Name, formatTimestamp(policy.UpdatedAt), policy.Document)
	},
}

var policyVersionsCmd = &cobra.Command{
	Use:   "versions <name>",
	Short: "List previous versions",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		path := fmt.Sprintf("/policies/%s/versions?limit=%d", args[0], policyVersionsLimit)
		var resp struct {
			Versions []PolicyVersion `json:"versions"`
		}
		if err := client.GetJSON(path, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Versions); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Versions) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No versions recorded for %s.\n", args[0])
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "VERSION\tCREATED\n")
		for _, v := range resp.Versions {
			fmt.Fprintf(tw, "%d\t%s\n", v.Version, formatTimestamp(v.CreatedAt))
		}
		flushTable(tw)
	},
}

var policyLintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Validate a policy document",
	Run: func(cmd *cobra.Command, args []string) {
		doc, err := readPolicyDocument(policyFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"document": doc}
		if err := client.PostJSON("/policies/lint", payload, nil); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Policy is valid JSON.")
	},
}

var policyBundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Download all policies as a zip",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if policyBundleOutput == "" {
			policyBundleOutput = "policies.zip"
		}
		if err := client.DownloadToFile("/policies/bundle", policyBundleOutput); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Bundle written to %s\n", policyBundleOutput)
	},
}

var policyRollbackCmd = &cobra.Command{
	Use:   "rollback <name>",
	Short: "Rollback to a previous version",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]int{"version": policyRollbackVersion}
		var policy Policy
		if err := client.PostJSON("/policies/"+args[0]+"/rollback", payload, &policy); err != nil {
			exitWithError(cmd, err)
			return
		}
		label := "previous version"
		if policyRollbackVersion > 0 {
			label = fmt.Sprintf("version %d", policyRollbackVersion)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Rolled back %s to %s\n", policy.Name, label)
	},
}

func init() {
	policyApplyCmd.Flags().StringVar(&policyFile, "file", "", "Path to policy file (defaults to STDIN)")
	policyDeleteCmd.Flags().BoolVar(&policyDeleteForce, "yes", false, "Skip confirmation")
	policyVersionsCmd.Flags().IntVar(&policyVersionsLimit, "limit", 5, "Number of versions to display")
	policyLintCmd.Flags().StringVar(&policyFile, "file", "", "Path to policy file (defaults to STDIN)")
	policyBundleCmd.Flags().StringVar(&policyBundleOutput, "output", "policies.zip", "Destination zip file")
	policyRollbackCmd.Flags().IntVar(&policyRollbackVersion, "version", 0, "Version to restore (0 = latest)")

	policyCmd.AddCommand(policyListCmd)
	policyCmd.AddCommand(policyApplyCmd)
	policyCmd.AddCommand(policyGetCmd)
	policyCmd.AddCommand(policyVersionsCmd)
	policyCmd.AddCommand(policyLintCmd)
	policyCmd.AddCommand(policyBundleCmd)
	policyCmd.AddCommand(policyRollbackCmd)
	policyCmd.AddCommand(policyDeleteCmd)
}

type Policy struct {
	Name      string    `json:"name"`
	Document  string    `json:"document"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type PolicyVersion struct {
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	Document  string    `json:"document"`
	CreatedAt time.Time `json:"createdAt"`
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
