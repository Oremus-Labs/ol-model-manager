package mllmcli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var (
	searchTypes []string
	searchLimit int
	searchOpen  bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search models, weights, jobs, HuggingFace cache, and notifications",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		client, _, err := mustClient()
		if err != nil {
			exitWithError(cmd, err)
			return
		}
		query := strings.Join(args, " ")
		path := fmt.Sprintf("/search?q=%s", url.QueryEscape(query))
		for _, t := range searchTypes {
			path += "&type=" + url.QueryEscape(t)
		}
		if searchLimit > 0 {
			path += fmt.Sprintf("&limit=%d", searchLimit)
		}

		var resp struct {
			Results []searchResult `json:"results"`
		}
		if err := client.GetJSON(path, &resp); err != nil {
			exitWithError(cmd, err)
			return
		}

		if err := writeOutput(cmd, resp.Results); err != nil {
			exitWithError(cmd, err)
			return
		}
		if strings.EqualFold(outputFormat, "json") {
			_ = printJSON(resp.Results)
		} else {
			renderSearchTable(cmd, resp.Results)
		}

		if searchOpen && len(resp.Results) > 0 && len(resp.Results[0].NextActions) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Suggested next action: %s\n", resp.Results[0].NextActions[0])
		}
	},
}

type searchResult struct {
	Type        string                 `json:"type"`
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Score       int                    `json:"score"`
	Metadata    map[string]interface{} `json:"metadata"`
	NextActions []string               `json:"nextActions"`
}

func init() {
	searchCmd.Flags().StringSliceVar(&searchTypes, "type", nil, "Restrict search to specific types (models,weights,jobs,hf_models,notifications)")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "Maximum number of results to return")
	searchCmd.Flags().BoolVar(&searchOpen, "open", false, "Print the suggested next action for the top result")
}

func renderSearchTable(cmd *cobra.Command, results []searchResult) {
	if len(results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No results.")
		return
	}
	tw := newTable()
	fmt.Fprintf(tw, "TYPE\tID\tNAME\tSCORE\tNEXT ACTIONS\n")
	for _, res := range results {
		next := strings.Join(res.NextActions, " | ")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", res.Type, res.ID, res.Name, res.Score, next)
	}
	flushTable(tw)
}
