package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// herdr runs the Herdr CLI the plugin was started with. Tests replace it.
var herdr = func(args ...string) ([]byte, error) {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	return exec.Command(bin, args...).Output()
}

// xeno replay opens a Herdr workspace with one pane per recorded thread. Each
// pane resumes the thread's own agent (Pi, from its session file), so Herdr
// shows the agent and its history; nothing is prompted, so no model is called.
// replay then assesses each thread and lights that pane's row, so the
// workspace reads as the run's heat map. Run it with TYPESAFE_API_KEY
// resolved, e.g. under `pass-cli run`.
func replay(args []string) error {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	reasons := flags.String("reasons", "", "retained Pass audit JSON whose ItemRead reasons each thread is assessed against")
	label := flags.String("label", "xeno replay", "label for the new workspace")
	positive := flags.String("positive", "", "control reason every thread should fit; lights the verifier")
	negative := flags.String("negative", "", "control reason no thread should fit; lights the falsifier")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: xeno replay --reasons FILE [--label TEXT] [--positive R --negative R] SESSION...")
		fmt.Fprintln(flags.Output(), "\nResumes each Pi session file as an agent in its own Herdr pane, assesses it and lights the pane.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *reasons == "" || flags.NArg() == 0 {
		flags.Usage()
		return errors.New("replay needs --reasons and at least one session")
	}
	audit, err := filepath.Abs(*reasons)
	if err != nil {
		return err
	}
	// Panes start here; each thread's copy and evidence live under .witness.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	created, err := herdrResult("workspace", "create", "--label", *label, "--cwd", cwd, "--no-focus")
	if err != nil {
		return err
	}
	suffix := agentSuffix(*label)
	pane := created.RootPane.PaneID
	for i, thread := range flags.Args() {
		if i > 0 {
			direction := "right"
			if i%2 == 0 {
				direction = "down"
			}
			split, err := herdrResult("pane", "split", pane, "--direction", direction, "--no-focus")
			if err != nil {
				return err
			}
			pane = split.Pane.PaneID
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(thread), "session-"), filepath.Ext(thread))
		// Pi appends to the session it resumes; resume a copy, keep the recording.
		raw, err := os.ReadFile(thread)
		if err != nil {
			return err
		}
		resumed := filepath.Join(cwd, ".witness", "replay", suffix, filepath.Base(thread))
		if err := os.MkdirAll(filepath.Dir(resumed), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(resumed, raw, 0600); err != nil {
			return err
		}
		if out, err := herdr("agent", "start", name+"-"+suffix, "--kind", "pi", "--pane", pane, "--", "--session", resumed); err != nil {
			// A Pi with no key or login may wait at a prompt: the pane still
			// holds the resumed agent, so report it and carry on.
			fmt.Fprintf(os.Stderr, "herdr agent start %s: %v %s\n", name, err, out)
		}
		assess := []string{"--reasons", audit}
		if *positive != "" {
			assess = append(assess, "--positive", *positive)
		}
		if *negative != "" {
			assess = append(assess, "--negative", *negative)
		}
		os.Setenv("HERDR_PANE_ID", pane)
		os.Setenv("XENO_RUN_DIR", filepath.Join(cwd, ".witness", "run", name))
		if err := assessPane(append(assess, thread)); err != nil {
			fmt.Fprintf(os.Stderr, "xeno assess %s: %v\n", name, err)
		}
		fmt.Println(pane, name)
	}
	return nil
}

// assessPane is assess, replaceable in tests.
var assessPane = assess

// agentSuffix makes agent names unique per replay: the label's last letters
// and digits, as Herdr names allow.
func agentSuffix(label string) string {
	keep := []rune{}
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			keep = append(keep, r)
		}
	}
	if len(keep) > 8 {
		keep = keep[len(keep)-8:]
	}
	if len(keep) == 0 {
		return "replay"
	}
	return string(keep)
}

type herdrIDs struct {
	RootPane struct {
		PaneID string `json:"pane_id"`
	} `json:"root_pane"`
	Pane struct {
		PaneID string `json:"pane_id"`
	} `json:"pane"`
}

func herdrResult(args ...string) (herdrIDs, error) {
	var response struct {
		Result herdrIDs `json:"result"`
	}
	out, err := herdr(args...)
	if err != nil {
		return response.Result, fmt.Errorf("herdr %s: %w", strings.Join(args[:2], " "), err)
	}
	if json.Unmarshal(out, &response) != nil || (response.Result.RootPane.PaneID == "" && response.Result.Pane.PaneID == "") {
		return response.Result, fmt.Errorf("herdr %s: no pane in response", strings.Join(args[:2], " "))
	}
	return response.Result, nil
}
