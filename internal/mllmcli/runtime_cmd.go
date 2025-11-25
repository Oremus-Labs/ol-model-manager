package mllmcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var runtimeCmd = &cobra.Command{
	Use:   "runtime",
	Short: "Inspect and control the active inference runtime",
}

var (
	runtimeStatusWatch    bool
	runtimeStatusInterval time.Duration
	runtimeStatusDetails  bool
)

var runtimeStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show KServe/Knative runtime status",
	Run: func(cmd *cobra.Command, args []string) {
		if runtimeStatusWatch && strings.EqualFold(outputFormat, "json") {
			exitWithError(cmd, fmt.Errorf("--watch is not supported with -o json"))
			return
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}

		for {
			status, err := fetchRuntimeStatus(client)
			if err != nil {
				exitWithError(cmd, err)
				return
			}
			if strings.EqualFold(outputFormat, "json") {
				if err := printJSON(status); err != nil {
					exitWithError(cmd, err)
					return
				}
			} else {
				renderRuntimeStatus(cmd, status, runtimeStatusDetails)
			}
			if !runtimeStatusWatch {
				return
			}
			select {
			case <-cmd.Context().Done():
				return
			case <-time.After(runtimeStatusInterval):
			}
		}
	},
}

var (
	runtimeActivateWait    bool
	runtimeActivateTimeout time.Duration
)

var runtimeActivateCmd = &cobra.Command{
	Use:   "activate <model-id>",
	Short: "Activate a catalog model via KServe",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{"modelId": args[0]}
		if err := postRuntimeJSON(client, "/runtime/activate", payload, "/models/activate", map[string]string{"id": args[0]}); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Activation request submitted for %s\n", args[0])
		if runtimeActivateWait {
			ctx := cmd.Context()
			if runtimeActivateTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, runtimeActivateTimeout)
				defer cancel()
			}
			if err := waitForActivation(ctx, client, args[0], cmd.OutOrStdout()); err != nil {
				exitWithError(cmd, err)
			}
		}
	},
}

var (
	runtimeDeactivateWait    bool
	runtimeDeactivateTimeout time.Duration
)

var runtimeSwitchCurrent string

var runtimeSwitchCmd = &cobra.Command{
	Use:   "switch <model-id>",
	Short: "Promote a candidate model (blue/green style) to active",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]string{
			"candidateId": args[0],
		}
		if runtimeSwitchCurrent != "" {
			payload["currentId"] = runtimeSwitchCurrent
		}
		if err := postRuntimeJSON(client, "/runtime/promote", payload, "/models/activate", map[string]string{"id": args[0]}); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Promotion requested for %s\n", args[0])
	},
}
var runtimeDeactivateCmd = &cobra.Command{
	Use:   "deactivate",
	Short: "Deactivate the active model",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := postRuntimeJSON(client, "/runtime/deactivate", map[string]string{}, "/models/deactivate", map[string]string{}); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Deactivation request submitted.")
		if runtimeDeactivateWait {
			ctx := cmd.Context()
			if runtimeDeactivateTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, runtimeDeactivateTimeout)
				defer cancel()
			}
			if err := waitForDeactivation(ctx, client, cmd.OutOrStdout()); err != nil {
				exitWithError(cmd, err)
			}
		}
	},
}

func init() {
	runtimeStatusCmd.Flags().BoolVar(&runtimeStatusWatch, "watch", false, "Continuously watch status updates")
	runtimeStatusCmd.Flags().DurationVar(&runtimeStatusInterval, "interval", 5*time.Second, "Interval between status refreshes")
	runtimeStatusCmd.Flags().BoolVar(&runtimeStatusDetails, "details", false, "Show pod-level details")

	runtimeActivateCmd.Flags().BoolVar(&runtimeActivateWait, "wait", false, "Wait for the activation to complete")
	runtimeActivateCmd.Flags().DurationVar(&runtimeActivateTimeout, "timeout", 5*time.Minute, "Timeout for --wait")

	runtimeDeactivateCmd.Flags().BoolVar(&runtimeDeactivateWait, "wait", false, "Wait until the runtime fully deactivates")
	runtimeDeactivateCmd.Flags().DurationVar(&runtimeDeactivateTimeout, "timeout", 2*time.Minute, "Timeout for --wait")
	runtimeSwitchCmd.Flags().StringVar(&runtimeSwitchCurrent, "current", "", "Expected currently running model (optional safety check)")

	runtimeCmd.AddCommand(runtimeStatusCmd)
	runtimeCmd.AddCommand(runtimeActivateCmd)
	runtimeCmd.AddCommand(runtimeDeactivateCmd)
	runtimeCmd.AddCommand(runtimeSwitchCmd)
}

func renderRuntimeStatus(cmd *cobra.Command, status *RuntimeStatus, details bool) {
	if status == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "No runtime status available")
		return
	}
	tw := newTable()
	fmt.Fprintf(tw, "Component\tState\tDetails\n")
	if status.InferenceService != nil {
		ready := status.InferenceService.Ready
		if ready == "" {
			ready = "Unknown"
		}
		fmt.Fprintf(tw, "InferenceService\t%s\t%s\n", ready, status.InferenceService.URL)
		for _, cond := range status.InferenceService.Conditions {
			fmt.Fprintf(tw, "  - %s\t%s\t%s\n", cond.Type, cond.Status, cond.Message)
		}
	} else {
		fmt.Fprintf(tw, "InferenceService\t-\t(none)\n")
	}
	info := "-"
	if len(status.GPUAllocations) > 0 {
		agg := make([]string, 0, len(status.GPUAllocations))
		for k, v := range status.GPUAllocations {
			agg = append(agg, fmt.Sprintf("%s=%s", k, v))
		}
		sort.Strings(agg)
		info = strings.Join(agg, ", ")
	}
	fmt.Fprintf(tw, "Deployments\t%d tracked\t%s\n", len(status.Deployments), info)
	flushTable(tw)

	if details && len(status.Pods) > 0 {
		tw = newTable()
		fmt.Fprintf(tw, "Pod\tPhase\tReady\tMessage\n")
		for _, pod := range status.Pods {
			total := pod.TotalContainers
			if total == 0 {
				total = int32(len(pod.Containers))
			}
			ready := fmt.Sprintf("%d/%d", pod.ReadyContainers, total)
			msg := pod.Message
			if msg == "" && len(pod.Conditions) > 0 {
				msg = pod.Conditions[len(pod.Conditions)-1].Message
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", pod.Name, pod.Phase, ready, msg)
		}
		flushTable(tw)
	}
	if msg, busy := detectGPUContention(status); busy {
		fmt.Fprintf(cmd.OutOrStdout(), "GPU contention detected: %s\n", msg)
	}
}

func waitForActivation(ctx context.Context, client *Client, modelID string, out io.Writer) error {
	fmt.Fprintf(out, "Waiting for %s to become Ready...\n", modelID)
	updates := make(chan activationUpdate, 32)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go streamActivationEvents(ctx, client, modelID, updates)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			fmt.Fprintf(out, "[activation] %s\n", update.message)
			if update.err != nil {
				return update.err
			}
			if update.done {
				return nil
			}
		case <-ticker.C:
			status, err := fetchRuntimeStatus(client)
			if err != nil {
				continue
			}
			if status.InferenceService != nil && strings.EqualFold(status.InferenceService.Ready, "True") {
				fmt.Fprintf(out, "InferenceService %s is Ready.\n", status.InferenceService.Name)
				return nil
			}
		}
	}
}

func waitForDeactivation(ctx context.Context, client *Client, out io.Writer) error {
	fmt.Fprintln(out, "Waiting for runtime to deactivate...")
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var resp struct {
				Status string `json:"status"`
			}
			if err := client.GetJSON("/active", &resp); err != nil {
				continue
			}
			if strings.EqualFold(resp.Status, "none") || resp.Status == "" {
				fmt.Fprintln(out, "Runtime deactivated.")
				return nil
			}
		}
	}
}

type activationUpdate struct {
	message string
	done    bool
	err     error
}

func streamActivationEvents(ctx context.Context, client *Client, modelID string, ch chan<- activationUpdate) {
	defer close(ch)
	for {
		if ctx.Err() != nil {
			return
		}
		var finished bool
		handler := func(ev EventEnvelope) bool {
			if !isLifecycleEvent(ev.Type) {
				return true
			}
			var payload struct {
				ModelID     string `json:"modelId"`
				DisplayName string `json:"displayName"`
				Action      string `json:"action"`
				Error       string `json:"error"`
			}
			if err := json.Unmarshal(ev.Data, &payload); err != nil {
				return true
			}
			if modelID != "" && payload.ModelID != "" && payload.ModelID != modelID {
				return true
			}
			switch ev.Type {
			case "model.activation.started":
				ch <- activationUpdate{message: fmt.Sprintf("start requested for %s", payload.DisplayName)}
			case "model.activation.completed":
				ch <- activationUpdate{message: fmt.Sprintf("activation completed (%s)", payload.Action), done: true}
				finished = true
				return false
			case "model.activation.failed":
				errMsg := payload.Error
				if errMsg == "" {
					errMsg = "activation failed"
				}
				ch <- activationUpdate{message: errMsg, err: errors.New(errMsg)}
				finished = true
				return false
			case "model.deactivation.completed":
				ch <- activationUpdate{message: "deactivation completed", done: true}
				finished = true
				return false
			case "model.deactivation.failed":
				errMsg := payload.Error
				if errMsg == "" {
					errMsg = "deactivation failed"
				}
				ch <- activationUpdate{message: errMsg, err: errors.New(errMsg)}
				finished = true
				return false
			default:
				ch <- activationUpdate{message: ev.Type}
			}
			return true
		}
		if err := client.StreamEvents(ctx, handler); err != nil && ctx.Err() == nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if finished || ctx.Err() != nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func isLifecycleEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "model.activation.") ||
		strings.HasPrefix(eventType, "model.deactivation.")
}

func postRuntimeJSON(client *Client, path string, payload interface{}, legacyPath string, legacyPayload interface{}) error {
	if err := client.PostJSON(path, payload, nil); err != nil {
		if legacyPath != "" && isNotFoundError(err) {
			return client.PostJSON(legacyPath, legacyPayload, nil)
		}
		return err
	}
	return nil
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "404") || strings.Contains(lower, "not found")
}
