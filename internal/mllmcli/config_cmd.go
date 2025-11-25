package mllmcli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CLI configuration",
}

var configSetContextCmd = &cobra.Command{
	Use:   "set-context <name>",
	Short: "Create or update a context",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := args[0]
		server, _ := cmd.Flags().GetString("server")
		token, _ := cmd.Flags().GetString("token")
		namespace, _ := cmd.Flags().GetString("namespace")
		makeCurrent, _ := cmd.Flags().GetBool("current")

		if server == "" {
			exitWithError(cmd, fmt.Errorf("--server is required"))
			return
		}
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		ctx := Context{
			Name:      name,
			Server:    server,
			Token:     token,
			Namespace: namespace,
		}
		setContext(cfg, ctx, makeCurrent)
		if err := SaveConfig(cfg, cfgFile); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Context %q updated.\n", name)
	},
}

var configUseContextCmd = &cobra.Command{
	Use:   "use-context <name>",
	Short: "Switch the current context",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := ensureContextExists(cfg, args[0]); err != nil {
			exitWithError(cmd, err)
			return
		}
		cfg.CurrentContext = args[0]
		if err := SaveConfig(cfg, cfgFile); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Switched to context %q.\n", args[0])
	},
}

var configCurrentContextCmd = &cobra.Command{
	Use:   "current-context",
	Short: "Print the current context",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if cfg.CurrentContext == "" {
			fmt.Fprintln(cmd.OutOrStdout(), "No context configured.")
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), cfg.CurrentContext)
	},
}

var configViewCmd = &cobra.Command{
	Use:   "view",
	Short: "Show the raw configuration",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := LoadConfig(cfgFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			if err := printJSON(cfg); err != nil {
				exitWithError(cmd, err)
			}
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Config file: %s\n", cfgFile)
		for name, ctx := range cfg.Contexts {
			current := ""
			if cfg.CurrentContext == name {
				current = "*"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%s)\n", current, name, ctx.Server)
		}
	},
}

func init() {
	configSetContextCmd.Flags().String("server", "", "API server URL")
	configSetContextCmd.Flags().String("token", "", "API token")
	configSetContextCmd.Flags().String("namespace", "ai", "Default namespace")
	configSetContextCmd.Flags().Bool("current", true, "Set as current context")
	configCmd.AddCommand(configSetContextCmd)
	configCmd.AddCommand(configUseContextCmd)
	configCmd.AddCommand(configCurrentContextCmd)
	configCmd.AddCommand(configViewCmd)
}
