package mllmcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
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
var jobRetryWatch bool
var jobLogsFollow bool

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

var jobsCancelCmd = &cobra.Command{
	Use:   "cancel <job-id>",
	Short: "Cancel a pending or running job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Status string `json:"status"`
			Job    Job    `json:"job"`
		}
		if err := client.PostJSON(fmt.Sprintf("/jobs/%s/cancel", args[0]), map[string]string{}, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Cancelled job %s (%s)\n", resp.Job.ID, resp.Status)
	},
}

var jobsRetryCmd = &cobra.Command{
	Use:   "retry <job-id>",
	Short: "Retry a failed or cancelled job",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Status string `json:"status"`
			Job    Job    `json:"job"`
		}
		if err := client.PostJSON(fmt.Sprintf("/jobs/%s/retry", args[0]), map[string]string{}, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Queued retry for job %s (attempt %d/%d)\n", resp.Job.ID, resp.Job.Attempt+1, resp.Job.MaxAttempts)
		if jobRetryWatch {
			if err := watchJob(cmd.Context(), cmd, client, args[0]); err != nil {
				exitWithError(cmd, err)
			}
		}
	},
}

var jobsLogsCmd = &cobra.Command{
	Use:   "logs <job-id>",
	Short: "Show job log entries",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Logs []JobLogEntry `json:"logs"`
		}
		if err := client.GetJSON(fmt.Sprintf("/jobs/%s/logs", args[0]), &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		printJobLogs(cmd, resp.Logs)
		if jobLogsFollow {
			fmt.Fprintln(cmd.OutOrStdout(), "--- streaming new log entries ---")
			err := streamJobLogs(cmd.Context(), client, args[0], cmd.OutOrStdout())
			if err != nil && cmd.Context().Err() == nil {
				exitWithError(cmd, err)
			}
		}
	},
}

func init() {
	jobsListCmd.Flags().IntVar(&jobsLimit, "limit", 20, "Maximum jobs to return")
	jobsListCmd.Flags().StringVar(&jobsStatus, "status", "", "Filter by status (pending|running|completed|failed)")
	jobsListCmd.Flags().StringVar(&jobsType, "type", "", "Filter by job type")
	jobsListCmd.Flags().StringVar(&jobsModelID, "model-id", "", "Filter by model ID")
	jobsWatchCmd.Flags().DurationVar(&jobWatchTimeout, "timeout", 0, "Stop watching after the specified duration (0 = wait forever)")
	jobsRetryCmd.Flags().BoolVar(&jobRetryWatch, "watch", false, "Watch the job after retrying")
	jobsLogsCmd.Flags().BoolVar(&jobLogsFollow, "follow", false, "Stream new log entries")
	jobsCmd.AddCommand(jobsListCmd)
	jobsCmd.AddCommand(jobsGetCmd)
	jobsCmd.AddCommand(jobsWatchCmd)
	jobsCmd.AddCommand(jobsCancelCmd)
	jobsCmd.AddCommand(jobsRetryCmd)
	jobsCmd.AddCommand(jobsLogsCmd)
}

// Job mirrors the API job payload.
type Job struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Status      string                 `json:"status"`
	Stage       string                 `json:"stage"`
	Progress    int                    `json:"progress"`
	Message     string                 `json:"message"`
	Payload     map[string]interface{} `json:"payload,omitempty"`
	Result      map[string]interface{} `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Attempt     int                    `json:"attempt"`
	MaxAttempts int                    `json:"maxAttempts"`
	CancelledAt *time.Time             `json:"cancelledAt"`
	Logs        []JobLogEntry          `json:"logs,omitempty"`
	CreatedAt   time.Time              `json:"createdAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
}

type JobLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	Message   string    `json:"message"`
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	updates := make(chan Job, 32)
	var wg sync.WaitGroup

	sendUpdate := func(job Job) {
		select {
		case updates <- job:
		default:
		}
		if isTerminalStatus(job.Status) {
			cancel()
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				job, err := fetchJob(client, jobID)
				if err != nil {
					continue
				}
				sendUpdate(*job)
				if job.Status == "completed" || job.Status == "failed" {
					return
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
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
				sendUpdate(job)
				return !isTerminalStatus(job.Status)
			}
			if err := client.StreamEvents(ctx, handler); err != nil && ctx.Err() == nil {
				time.Sleep(2 * time.Second)
				continue
			}
			return
		}
	}()

	go func() {
		wg.Wait()
		close(updates)
	}()

	lastStage := ""
	lastProgress := -1
	lastMessage := ""
	var finalJob *Job

	for job := range updates {
		stage := job.Stage
		if stage == "" {
			stage = job.Status
		}
		if job.Stage != lastStage || job.Progress != lastProgress || job.Message != lastMessage {
			msg := job.Message
			if msg == "" {
				msg = stage
			}
			fmt.Fprintf(cmd.OutOrStdout(), "[%s] %3d%% %s\n", stage, job.Progress, msg)
			lastStage = job.Stage
			lastProgress = job.Progress
			lastMessage = job.Message
		}
		if isTerminalStatus(job.Status) {
			j := job
			finalJob = &j
			break
		}
	}

	if finalJob == nil {
		job, err := fetchJob(client, jobID)
		if err != nil {
			return err
		}
		finalJob = job
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Final status: %s (%d%%) - %s\n", finalJob.Status, finalJob.Progress, finalJob.Message)
	if finalJob.Error != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Error: %s\n", finalJob.Error)
	}
	return nil
}

func isTerminalStatus(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func printJobLogs(cmd *cobra.Command, logs []JobLogEntry) {
	if len(logs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No log entries recorded.")
		return
	}
	for _, entry := range logs {
		printJobLogEntry(cmd, entry)
	}
}

func printJobLogEntry(cmd *cobra.Command, entry JobLogEntry) {
	stage := entry.Stage
	if stage == "" {
		stage = "-"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [%s] %s\n",
		entry.Timestamp.Format(time.RFC3339),
		stage,
		entry.Message)
}

func streamJobLogs(ctx context.Context, client *Client, jobID string, out io.Writer) error {
	handler := func(ev EventEnvelope) bool {
		if ev.Type != "job.log" {
			return true
		}
		var payload struct {
			JobID string      `json:"jobId"`
			Log   JobLogEntry `json:"log"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			return true
		}
		if payload.JobID != jobID {
			return true
		}
		fmt.Fprintf(out, "%s [%s] %s\n",
			payload.Log.Timestamp.Format(time.RFC3339),
			payload.Log.Stage,
			payload.Log.Message)
		return true
	}
	return client.StreamEvents(ctx, handler)
}
