package mllmcli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var backupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Record or inspect backups",
}

var backupsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded backups",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Backups []Backup `json:"backups"`
		}
		if err := client.GetJSON("/backups", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Backups); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			return
		}
		if len(resp.Backups) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No backups recorded.")
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "ID\tTYPE\tLOCATION\tCREATED\n")
		for _, bkp := range resp.Backups {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", bkp.ID, bkp.Type, bkp.Location, formatTimestamp(bkp.CreatedAt))
		}
		flushTable(tw)
	},
}

var (
	backupType            string
	backupLocation        string
	backupNotes           string
	backupRestoreID       string
	backupRestoreLocation string
	backupRestoreNotes    string
)

var backupsRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Record a backup entry",
	Run: func(cmd *cobra.Command, args []string) {
		if backupType == "" || backupLocation == "" {
			exitWithError(cmd, fmt.Errorf("--type and --location are required"))
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{
			"type":     backupType,
			"location": backupLocation,
			"notes":    backupNotes,
		}
		var record Backup
		if err := client.PostJSON("/backups", payload, &record); err != nil {
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
		fmt.Fprintf(cmd.OutOrStdout(), "Recorded backup %s -> %s\n", record.Type, record.Location)
	},
}

var backupsRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Trigger a backup run",
	Run: func(cmd *cobra.Command, args []string) {
		if backupType == "" || backupLocation == "" {
			exitWithError(cmd, fmt.Errorf("--type and --location are required"))
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"type": backupType, "location": backupLocation, "notes": backupNotes}
		if err := client.PostJSON("/backups/run", payload, nil); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Backup job recorded.")
	},
}

var backupsRestoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Record a restore request",
	Run: func(cmd *cobra.Command, args []string) {
		if backupRestoreLocation == "" {
			exitWithError(cmd, fmt.Errorf("--restore-location is required"))
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"location": backupRestoreLocation, "notes": backupRestoreNotes}
		if backupRestoreID != "" {
			payload["id"] = backupRestoreID
		}
		if err := client.PostJSON("/backups/restore", payload, nil); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Restore request submitted.")
	},
}

func init() {
	backupsRecordCmd.Flags().StringVar(&backupType, "type", "manual", "Backup type label")
	backupsRecordCmd.Flags().StringVar(&backupLocation, "location", "", "Location (PVC, S3 path, etc.)")
	backupsRecordCmd.Flags().StringVar(&backupNotes, "notes", "", "Optional notes")
	backupsRunCmd.Flags().StringVar(&backupType, "type", "manual", "Backup type label")
	backupsRunCmd.Flags().StringVar(&backupLocation, "location", "", "Location (PVC, S3 path, etc.)")
	backupsRunCmd.Flags().StringVar(&backupNotes, "notes", "", "Optional notes")
	backupsRestoreCmd.Flags().StringVar(&backupRestoreID, "restore-id", "", "Backup identifier to restore")
	backupsRestoreCmd.Flags().StringVar(&backupRestoreLocation, "restore-location", "", "Location of backup to restore")
	backupsRestoreCmd.Flags().StringVar(&backupRestoreNotes, "restore-notes", "", "Restore notes")

	backupsCmd.AddCommand(backupsListCmd)
	backupsCmd.AddCommand(backupsRecordCmd)
	backupsCmd.AddCommand(backupsRunCmd)
	backupsCmd.AddCommand(backupsRestoreCmd)
}

type Backup struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Location  string    `json:"location"`
	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"createdAt"`
}
