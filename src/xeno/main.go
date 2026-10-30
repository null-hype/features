// xeno answers one question when an agent finishes: did that work run with
// both a positive and a negative control?
//
// Herdr runs it on pane.agent_status_changed. When the pane becomes done, xeno
// reads the Proton Pass agent monitor. Every fetch carries a plain-sentence
// PROTON_PASS_AGENT_REASON; a reason starting "positive control" or "negative
// control" counts as that control having run. Without both, the pane's done
// state is labelled uncontrolled. If the monitor cannot be read, it is labelled
// unverified: missing evidence is not a clean result.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const defaultMonitor = "pass-cli agent monitor --output json"

type event struct {
	Data struct {
		PaneID      string `json:"pane_id"`
		AgentStatus string `json:"agent_status"`
	} `json:"data"`
}

func main() {
	var ev event
	if err := json.Unmarshal([]byte(os.Getenv("HERDR_PLUGIN_EVENT_JSON")), &ev); err != nil {
		fail("event: %v", err)
	}
	if ev.Data.AgentStatus != "done" || ev.Data.PaneID == "" {
		return
	}

	label := "done"
	out, err := monitor()
	if err == nil {
		var positive, negative bool
		if positive, negative, err = controls(out); err == nil && !(positive && negative) {
			label = "done · uncontrolled"
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "xeno: monitor: %v\n", err)
		label = "done · unverified"
	}

	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	cmd := exec.Command(herdr, "pane", "report-metadata", ev.Data.PaneID,
		"--source", "xeno", "--state-label", "done="+label)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fail("report-metadata: %v", err)
	}
}

// monitor returns the agent monitor's JSON. XENO_MONITOR replaces the command,
// for example to name the agent: "pass-cli agent monitor my-agent --output json".
func monitor() ([]byte, error) {
	argv := strings.Fields(os.Getenv("XENO_MONITOR"))
	if len(argv) == 0 {
		argv = strings.Fields(defaultMonitor)
	}
	return exec.Command(argv[0], argv[1:]...).Output()
}

// controls reports whether the monitor holds a positive and a negative control
// reason. The monitor's JSON shape is not documented, so every "reason" string
// anywhere in it counts.
func controls(out []byte) (positive, negative bool, err error) {
	var doc any
	if err := json.Unmarshal(out, &doc); err != nil {
		return false, false, err
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if s, ok := x.(string); ok && k == "reason" {
					r := strings.ToLower(strings.TrimSpace(s))
					positive = positive || strings.HasPrefix(r, "positive control")
					negative = negative || strings.HasPrefix(r, "negative control")
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(doc)
	return positive, negative, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "xeno: "+format+"\n", args...)
	os.Exit(1)
}
