package mllmcli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var tokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "Manage API tokens",
}

var tokensListCmd = &cobra.Command{
	Use:   "list",
	Short: "List issued API tokens",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Tokens []APIToken `json:"tokens"`
		}
		if err := client.GetJSON("/tokens", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Tokens); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Tokens) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No tokens issued yet.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "ID\tNAME\tSCOPES\tCREATED\n")
		for _, token := range resp.Tokens {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", token.ID, token.Name, strings.Join(token.Scopes, ","), formatTimestamp(token.CreatedAt))
		}
		flushTable(tw)
	},
}

var (
	tokenScopes []string
)

var tokensIssueCmd = &cobra.Command{
	Use:   "issue <name>",
	Short: "Issue a new API token",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		scopes := normalizeScopeArgs(tokenScopes)
		payload := map[string]interface{}{
			"name":   args[0],
			"scopes": scopes,
		}
		var resp struct {
			Token     string    `json:"token"`
			TokenID   string    `json:"tokenId"`
			Name      string    `json:"name"`
			Scopes    []string  `json:"scopes"`
			CreatedAt time.Time `json:"createdAt"`
		}
		if err := client.PostJSON("/tokens", payload, &resp); err != nil {
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
		fmt.Fprintf(cmd.OutOrStdout(), "Token ID: %s\n", resp.TokenID)
		fmt.Fprintf(cmd.OutOrStdout(), "Plain token (store securely, only shown once):\n%s\n", resp.Token)
	},
}

var tokensRevokeForce bool

var tokensRevokeCmd = &cobra.Command{
	Use:   "revoke <token-id>",
	Short: "Revoke an API token",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if !tokensRevokeForce {
			ok, err := confirmPrompt(fmt.Sprintf("Revoke token %s? [y/N]: ", args[0]), cmd.InOrStdin(), cmd.OutOrStdout())
			if err != nil {
				exitWithError(cmd, err)
				return
			}
			if !ok {
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
				return
			}
		}
		if err := client.Delete("/tokens/" + args[0]); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Token %s revoked.\n", args[0])
	},
}

func init() {
	tokensIssueCmd.Flags().StringSliceVar(&tokenScopes, "scope", nil, "Scope assigned to the token (repeatable)")
	tokensRevokeCmd.Flags().BoolVar(&tokensRevokeForce, "yes", false, "Skip confirmation prompt")

	tokensCmd.AddCommand(tokensListCmd)
	tokensCmd.AddCommand(tokensIssueCmd)
	tokensCmd.AddCommand(tokensRevokeCmd)
}

type APIToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"createdAt"`
}

func normalizeScopeArgs(scopes []string) []string {
	if len(scopes) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}
