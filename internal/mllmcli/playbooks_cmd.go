package mllmcli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var playbooksCmd = &cobra.Command{
	Use:   "playbooks",
	Short: "Manage curated install/activate playbooks",
}

var playbooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored playbooks",
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp struct {
			Playbooks []map[string]interface{} `json:"playbooks"`
		}
		if err := client.GetJSON("/playbooks", &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(resp.Playbooks)
			return
		}
		tw := newTable()
		fmt.Fprintf(tw, "NAME\tDESCRIPTION\tTAGS\n")
		for _, pb := range resp.Playbooks {
			name, _ := pb["name"].(string)
			desc, _ := pb["description"].(string)
			var tags []string
			if raw, ok := pb["tags"].([]interface{}); ok {
				for _, item := range raw {
					if s, ok := item.(string); ok {
						tags = append(tags, s)
					}
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", name, desc, strings.Join(tags, ","))
		}
		flushTable(tw)
	},
}

var playbooksGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show a playbook definition",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var record map[string]interface{}
		if err := client.GetJSON(fmt.Sprintf("/playbooks/%s", args[0]), &record); err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := writeOutput(cmd, record); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(record)
		}
	},
}

var (
	playbooksApplyFile        string
	playbooksApplyDescription string
	playbooksApplyTags        []string
)

var playbooksApplyCmd = &cobra.Command{
	Use:   "apply <name>",
	Short: "Create or update a playbook",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if playbooksApplyFile == "" {
			exitWithError(cmd, fmt.Errorf("--file is required"))
			return
		}
		data, err := readSpecFile(playbooksApplyFile)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		specJSON, err := toJSON(data)
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		payload := map[string]interface{}{
			"description": playbooksApplyDescription,
			"tags":        playbooksApplyTags,
			"spec":        json.RawMessage(specJSON),
		}
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp map[string]interface{}
		if err := client.PutJSON(fmt.Sprintf("/playbooks/%s", args[0]), payload, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Playbook %s saved.\n", args[0])
	},
}

var playbooksDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a playbook",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		if err := client.Delete(fmt.Sprintf("/playbooks/%s", args[0])); err != nil {
			exitWithError(cmd, err)
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted playbook %s\n", args[0])
	},
}

var (
	playbooksRunWatch        bool
	playbooksRunAutoActivate bool
)

type playbookRunInstallStep struct {
	Target         string `json:"target"`
	StorageURI     string `json:"storageUri"`
	InferenceModel string `json:"inferenceModelPath"`
	Job            *Job   `json:"job"`
}

type playbookRunActivateStep struct {
	Status   string `json:"status"`
	ModelID  string `json:"modelId"`
	Strategy string `json:"strategy"`
}

type playbookRunSteps struct {
	Install  *playbookRunInstallStep  `json:"install"`
	Activate *playbookRunActivateStep `json:"activate"`
}

type playbookRunResponse struct {
	Status   string           `json:"status"`
	Playbook map[string]any   `json:"playbook"`
	Steps    playbookRunSteps `json:"steps"`
}

var playbooksRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Execute a stored playbook",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		var resp playbookRunResponse
		if err := client.PostJSON(fmt.Sprintf("/playbooks/%s/run", args[0]), map[string]string{}, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}
		if outputFormat == "json" {
			_ = printJSON(resp)
		} else {
			renderPlaybookRun(cmd, &resp)
		}
		if playbooksRunWatch && resp.Steps.Install != nil && resp.Steps.Install.Job != nil {
			if err := watchJob(cmd.Context(), cmd, client, resp.Steps.Install.Job.ID); err != nil {
				exitWithError(cmd, err)
				return
			}
			if playbooksRunAutoActivate && resp.Steps.Activate != nil && strings.EqualFold(resp.Steps.Activate.Status, "pending_install") && resp.Steps.Activate.ModelID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Installing complete. Activating %s ...\n", resp.Steps.Activate.ModelID)
				if err := postRuntimeJSON(client, "/runtime/activate", map[string]string{"modelId": resp.Steps.Activate.ModelID}, "/models/activate", map[string]string{"id": resp.Steps.Activate.ModelID}); err != nil {
					exitWithError(cmd, err)
					return
				}
			}
		}
	},
}

func init() {
	playbooksApplyCmd.Flags().StringVarP(&playbooksApplyFile, "file", "f", "", "Path to playbook spec file (JSON or YAML)")
	playbooksApplyCmd.Flags().StringVar(&playbooksApplyDescription, "description", "", "Human-readable description")
	playbooksApplyCmd.Flags().StringSliceVar(&playbooksApplyTags, "tag", nil, "Tag labels (repeatable)")

	playbooksRunCmd.Flags().BoolVar(&playbooksRunWatch, "watch", false, "Watch the install job until completion")
	playbooksRunCmd.Flags().BoolVar(&playbooksRunAutoActivate, "auto-activate", false, "Activate automatically after install when possible")

	playbooksCmd.AddCommand(playbooksListCmd)
	playbooksCmd.AddCommand(playbooksGetCmd)
	playbooksCmd.AddCommand(playbooksApplyCmd)
	playbooksCmd.AddCommand(playbooksDeleteCmd)
	playbooksCmd.AddCommand(playbooksRunCmd)
}

func renderPlaybookRun(cmd *cobra.Command, resp *playbookRunResponse) {
	if resp == nil {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", resp.Status)
	if resp.Steps.Install != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Install target: %s (%s)\n", resp.Steps.Install.Target, resp.Steps.Install.StorageURI)
		if resp.Steps.Install.Job != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", resp.Steps.Install.Job.ID)
		}
	}
	if resp.Steps.Activate != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Activate model: %s [%s]\n", resp.Steps.Activate.ModelID, resp.Steps.Activate.Status)
	}
}

func readSpecFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func toJSON(data []byte) ([]byte, error) {
	trim := strings.TrimSpace(string(data))
	if trim == "" {
		return nil, fmt.Errorf("spec file is empty")
	}
	if trim[0] == '{' || trim[0] == '[' {
		return []byte(trim), nil
	}
	return yaml.YAMLToJSON([]byte(trim))
}
