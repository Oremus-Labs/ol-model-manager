package mllmcli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	cfgFile       string
	contextName   string
	overrideURL   string
	overrideToken string
	overrideNS    string
	outputFormat  string

	appConfig *Config
)

// Execute runs the CLI.
func Execute() error {
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	return rootCmd.Execute()
}

var rootCmd = &cobra.Command{
	Use:   "mllm",
	Short: "Manage LLM workloads on Kubernetes",
	Long: `mllm is the official CLI for the Oremus Labs Model Manager control plane.
Most commands require a configured context (see 'mllm config set-context').`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Config commands load/save the file manually.
		if strings.HasPrefix(cmd.CommandPath(), "mllm config") {
			return nil
		}
		if appConfig == nil {
			var err error
			appConfig, err = LoadConfig(cfgFile)
			if err != nil {
				return err
			}
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", defaultConfigPath(), "Path to the mllm config file")
	rootCmd.PersistentFlags().StringVar(&contextName, "context", "", "Context name to use (overrides current)")
	rootCmd.PersistentFlags().StringVar(&overrideURL, "server", "", "Override API server URL")
	rootCmd.PersistentFlags().StringVar(&overrideToken, "token", "", "Override API token")
	rootCmd.PersistentFlags().StringVar(&overrideNS, "namespace", "", "Override namespace for commands")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "Output format: table|json")

	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(modelsCmd)
	rootCmd.AddCommand(weightsCmd)
	rootCmd.AddCommand(jobsCmd)
	rootCmd.AddCommand(runtimeCmd)
	rootCmd.AddCommand(recommendCmd)
	rootCmd.AddCommand(notifyCmd)
	rootCmd.AddCommand(tokensCmd)
	rootCmd.AddCommand(policyCmd)
	rootCmd.AddCommand(auditCmd)
	rootCmd.AddCommand(backupsCmd)
	rootCmd.AddCommand(cleanupCmd)
	rootCmd.AddCommand(secretsCmd)
	rootCmd.AddCommand(playbooksCmd)
	rootCmd.AddCommand(configCmd)
}

// resolvedContext merges config state with flag overrides.
func resolvedContext() (*Context, error) {
	if appConfig == nil {
		return nil, fmt.Errorf("configuration not loaded")
	}
	ctxName := contextName
	if ctxName == "" {
		ctxName = appConfig.CurrentContext
	}
	ctx, ok := appConfig.Contexts[ctxName]
	if !ok {
		return nil, fmt.Errorf("context %q not found; use 'mllm config set-context'", ctxName)
	}
	if overrideURL != "" {
		ctx.Server = overrideURL
	}
	if overrideToken != "" {
		ctx.Token = overrideToken
	}
	if overrideNS != "" {
		ctx.Namespace = overrideNS
	}
	if ctx.Namespace == "" {
		ctx.Namespace = "default"
	}
	if ctx.Server == "" {
		return nil, fmt.Errorf("context %q is missing a server URL", ctxName)
	}
	return &ctx, nil
}

func mustClient() (*Client, *Context, error) {
	ctx, err := resolvedContext()
	if err != nil {
		return nil, nil, err
	}
	client := &Client{
		BaseURL: ctx.Server,
		Token:   ctx.Token,
		Timeout: 15 * time.Second,
	}
	return client, ctx, nil
}

func writeOutput(cmd *cobra.Command, data interface{}) error {
	switch strings.ToLower(outputFormat) {
	case "json":
		return printJSON(data)
	case "table", "":
		// Table is handled by the caller.
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", outputFormat)
	}
}

func exitWithError(cmd *cobra.Command, err error) {
	cmd.SilenceUsage = true
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}
