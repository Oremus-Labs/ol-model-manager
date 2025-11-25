package mllmcli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func printJSON(data interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func flushTable(tw *tabwriter.Writer) {
	_ = tw.Flush()
}

func printErrorLine(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	units := []struct {
		Dur  time.Duration
		Name string
	}{
		{time.Hour, "h"},
		{time.Minute, "m"},
		{time.Second, "s"},
	}
	var parts []string
	remainder := d
	for _, unit := range units {
		if remainder >= unit.Dur {
			value := remainder / unit.Dur
			remainder -= value * unit.Dur
			parts = append(parts, fmt.Sprintf("%d%s", value, unit.Name))
			if len(parts) == 2 {
				break
			}
		}
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	diff := time.Since(t)
	suffix := "ago"
	if diff < 0 {
		diff = -diff
		suffix = "from now"
	}
	return fmt.Sprintf("%s %s", humanDuration(diff), suffix)
}

func confirmPrompt(prompt string, in io.Reader, out io.Writer) (bool, error) {
	reader := bufio.NewReader(in)
	fmt.Fprint(out, prompt)
	input, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes", nil
}
