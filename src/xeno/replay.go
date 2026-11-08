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

// Variables replay hands each pane, as Pass references only: a pane resolves
// them with pass-cli itself, so no secret reaches a command line.
var replayEnv = []string{"TYPESAFE_API_KEY", "PROTON_PASS_AGENT_REASON"}

// xeno replay opens a Herdr workspace with one pane per recorded thread, each
// running `xeno assess` against the given reasons, so the sidebar shows the
// recorded session the way xeno sees a live one.
func replay(args []string) error {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	reasons := flags.String("reasons", "", "retained Pass audit JSON whose ItemRead reasons each thread is assessed against")
	label := flags.String("label", "xeno replay", "label for the new workspace")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: xeno replay --reasons FILE [--label TEXT] THREAD...")
		fmt.Fprintln(flags.Output(), "\nOpens a Herdr workspace with a pane per thread, each assessing it. Threads are Claude JSONL, trace sink sessions or agent session files.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *reasons == "" || flags.NArg() == 0 {
		flags.Usage()
		return errors.New("replay needs --reasons and at least one thread")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	audit, err := filepath.Abs(*reasons)
	if err != nil {
		return err
	}
	// Panes start here, and each writes its evidence to its own run directory.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	env, assess := []string{}, []string{}
	for _, name := range replayEnv {
		value := os.Getenv(name)
		if value == "" {
			continue
		}
		if name == "TYPESAFE_API_KEY" && !strings.HasPrefix(value, "pass://") {
			return errors.New("replay passes TYPESAFE_API_KEY to its panes as a pass:// reference, never as a resolved key")
		}
		env = append(env, "--env", name+"="+value)
	}
	created, err := herdrResult(append([]string{"workspace", "create", "--label", *label, "--cwd", cwd, "--no-focus"}, env...)...)
	if err != nil {
		return err
	}
	pane := created.RootPane.PaneID
	for i, thread := range flags.Args() {
		thread, err := filepath.Abs(thread)
		if err != nil {
			return err
		}
		if i > 0 {
			direction := "right"
			if i%2 == 0 {
				direction = "down"
			}
			split, err := herdrResult(append([]string{"pane", "split", pane, "--direction", direction, "--no-focus"}, env...)...)
			if err != nil {
				return err
			}
			pane = split.Pane.PaneID
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(thread), "session-"), filepath.Ext(thread))
		if _, err := herdr("pane", "rename", pane, name); err != nil {
			return fmt.Errorf("herdr pane rename: %w", err)
		}
		run := filepath.Join(cwd, ".witness", "run", name)
		assess = []string{"XENO_RUN_DIR=" + quote(run), "pass-cli", "run", "--", quote(self), "assess", "--reasons", quote(audit), quote(thread)}
		if _, err := herdr("pane", "run", pane, strings.Join(assess, " ")); err != nil {
			return fmt.Errorf("herdr pane run: %w", err)
		}
		fmt.Println(pane, name)
	}
	return nil
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

// quote makes a path one shell word for `herdr pane run`.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
