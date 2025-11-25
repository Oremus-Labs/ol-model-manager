package mllmcli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var (
	supportBundleOutput string
	supportBundleStdout bool
)

var supportCmd = &cobra.Command{
	Use:   "support",
	Short: "Support and troubleshooting utilities",
}

var supportBundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Download a support bundle (summary, jobs, metrics) as a zip",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}

		target := supportBundleOutput
		if target == "" {
			target = fmt.Sprintf("support-bundle-%s.zip", time.Now().UTC().Format("20060102-150405"))
		}

		data, err := client.GetBinary("/support/bundle")
		if err != nil {
			exitWithError(cmd, err)
			return
		}

		if supportBundleStdout {
			if _, err := os.Stdout.Write(data); err != nil {
				exitWithError(cmd, err)
				return
			}
			return
		}

		if err := os.WriteFile(filepath.Clean(target), data, 0o644); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Support bundle saved to %s (%d bytes)\n", target, len(data))
	},
}

func init() {
	supportBundleCmd.Flags().StringVarP(&supportBundleOutput, "output", "o", "", "Output file path (default support-bundle-<timestamp>.zip)")
	supportBundleCmd.Flags().BoolVar(&supportBundleStdout, "stdout", false, "Write bundle bytes to stdout")
	supportCmd.AddCommand(supportBundleCmd)
}
