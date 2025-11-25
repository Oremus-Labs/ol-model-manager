package mllmcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Inspect asynchronous jobs",
}

var (
	jobsLimit   int
	jobsStatus  string
	jobsType    string
	jobsModelID string
)

var jobsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent jobs",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		query := url.Values{}
		if jobsLimit > 0 {
			query.Set("limit", fmt.Sprintf("%d", jobsLimit))
		}
		if jobsStatus != "" {
			query.Set("status", jobsStatus)
		}
		if jobsType != "" {
			query.Set("type", jobsType)
		}
		if jobsModelID != "" {
			query.Set("modelId", jobsModelID)
		}
		path := "/jobs"
		if len(query) > 0 {
			path += "?" + query.Encode()
		}
		var resp struct {
			Jobs []Job `json:"jobs"`
		}
		if err := client.GetJSON(path, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, resp.Jobs); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(resp.Jobs)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "ID\tTYPE\tSTATUS\tPROGRESS\tUPDATED\n")
		for _, job := range resp.Jobs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d%%\t%s\n",
				shortID(job.ID),
				job.Type,
				job.Status,
				job.Progress,
				relativeTime(job.UpdatedAt))
		}
		flushTable(tw)
	},
}

var jobsGetCmd = &cobra.Command{
	Use:   "get <job-id>",
	Short: "Describe a job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		job, err := fetchJob(client, args[0])
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, job); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(job)
			return
		}
		printJobDetails(cmd, job)
	},
}

var jobWatchTimeout time.Duration

var jobsWatchCmd = &cobra.Command{
	Use:   "watch <job-id>",
	Short: "Stream job progress until completion",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		ctx := cmd.Context()
		if jobWatchTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, jobWatchTimeout)
			defer cancel()
		}
		if err := watchJob(ctx, cmd, client, args[0]); err != nil {
			exitWithError(cmd, err)
		}
	},
}

func init() {
	jobsListCmd.Flags().IntVar(&jobsLimit, "limit", 20, "Maximum jobs to return")
	jobsListCmd.Flags().StringVar(&jobsStatus, "status", "", "Filter by status (pending|running|completed|failed)")
	jobsListCmd.Flags().StringVar(&jobsType, "type", "", "Filter by job type")
	jobsListCmd.Flags().StringVar(&jobsModelID, "model-id", "", "Filter by model ID")
	jobsWatchCmd.Flags().DurationVar(&jobWatchTimeout, "timeout", 0, "Stop watching after the specified duration (0 = wait forever)")
	jobsCmd.AddCommand(jobsListCmd)
	jobsCmd.AddCommand(jobsGetCmd)
	jobsCmd.AddCommand(jobsWatchCmd)
}

// Job mirrors the API job payload.
type Job struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Status    string                 `json:"status"`
	Stage     string                 `json:"stage"`
	Progress  int                    `json:"progress"`
	Message   string                 `json:"message"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

func fetchJob(client *Client, id string) (*Job, error) {
	var job Job
	if err := client.GetJSON("/jobs/"+id, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func printJobDetails(cmd *cobra.Command, job *Job) {
	tw := newTable()
	fmt.Fprintf(tw, "Field\tValue\n")
	fmt.Fprintf(tw, "ID\t%s\n", job.ID)
	fmt.Fprintf(tw, "Type\t%s\n", job.Type)
	fmt.Fprintf(tw, "Status\t%s\n", job.Status)
	fmt.Fprintf(tw, "Stage\t%s\n", job.Stage)
	fmt.Fprintf(tw, "Progress\t%d%%\n", job.Progress)
	fmt.Fprintf(tw, "Message\t%s\n", job.Message)
	fmt.Fprintf(tw, "Updated\t%s\n", job.UpdatedAt.Format(time.RFC3339))
	if job.Error != "" {
		fmt.Fprintf(tw, "Error\t%s\n", job.Error)
	}
	flushTable(tw)
	if len(job.Payload) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nPayload:")
		_ = printJSON(job.Payload)
	}
	if len(job.Result) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nResult:")
		_ = printJSON(job.Result)
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func watchJob(ctx context.Context, cmd *cobra.Command, client *Client, jobID string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Watching job %s...\n", jobID)
	for {
		jobFinished := false
		handler := func(ev EventEnvelope) bool {
			if !strings.HasPrefix(ev.Type, "job.") {
				return true
			}
			var payload struct {
				Data Job `json:"data"`
			}
			if err := json.Unmarshal(ev.Data, &payload); err != nil {
				printErrorLine("event decode error: %v", err)
				return true
			}
			job := payload.Data
			if job.ID != jobID {
				return true
			}
			fmt.Fprintf(cmd.OutOrStdout(), "[%s] %-10s %3d%% %s\n", ev.Type, job.Stage, job.Progress, job.Message)
			if job.Status == "completed" || job.Status == "failed" {
				if job.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Job error: %s\n", job.Error)
				}
				jobFinished = true
				return false
			}
			return true
		}

		err := client.StreamEvents(ctx, handler)
		if err != nil && !errors.Is(err, context.Canceled) {
			printErrorLine("event stream interrupted: %v", err)
		}

		job, jobErr := fetchJob(client, jobID)
		if jobErr != nil {
			return jobErr
		}
		if job.Status == "completed" || job.Status == "failed" {
			fmt.Fprintf(cmd.OutOrStdout(), "Final status: %s (%d%%) - %s\n", job.Status, job.Progress, job.Message)
			if job.Error != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Error: %s\n", job.Error)
			}
			return nil
		}

		if jobFinished {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
