package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	code, err := command(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "xeno:", err)
	}
	os.Exit(code)
}

func command(args []string) (int, error) {
	if len(args) == 1 && args[0] == "label" {
		return label()
	}
	if len(args) != 1 || (args[0] != "init" && args[0] != "refresh") {
		return 2, errors.New("usage: xeno init | refresh | label")
	}
	startup := args[0] == "init"
	if startup && os.Getenv("PROTON_PASS_PERSONAL_ACCESS_TOKEN") == "" {
		return 0, nil
	}
	if err := refresh(); err != nil {
		if startup {
			// A Pass outage should not block the container from opening.
			return 0, err
		}
		return 1, err
	}
	return 0, nil
}

func stateDir() (string, error) {
	if dir := os.Getenv("XENO_STATE_DIR"); dir != "" {
		return dir, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".xeno"), nil
}

func refresh() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	lock, err := os.OpenFile(filepath.Join(dir, "pass.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	env := passEnv(dir)
	args := []string{"agent", "monitor", "--output", "json"}
	token := os.Getenv("PROTON_PASS_PERSONAL_ACCESS_TOKEN")
	if name := os.Getenv("XENO_PASS_AGENT"); token == "" && name != "" {
		args = append(args, "--", name)
	}
	out, err := passCommand(env, args...)
	if err != nil && token != "" {
		if _, err := passCommand(env, "login"); err != nil {
			return errors.New("Pass token login failed")
		}
		out, err = passCommand(env, args...)
	}
	if err != nil {
		return errors.New("Pass audit fetch failed")
	}

	var records []json.RawMessage
	if json.Unmarshal(out, &records) != nil || records == nil {
		return errors.New("Pass audit response is not a JSON array")
	}
	return atomicWrite(filepath.Join(dir, "reasons.json"), out)
}

func passEnv(dir string) []string {
	env := os.Environ()
	if token := os.Getenv("PROTON_PASS_PERSONAL_ACCESS_TOKEN"); token != "" {
		sum := sha256.Sum256([]byte(token))
		session := filepath.Join(dir, "pass-"+hex.EncodeToString(sum[:16]))
		env = append(env, "PROTON_PASS_SESSION_DIR="+session)
		if os.Getenv("PROTON_PASS_KEY_PROVIDER") == "" {
			env = append(env, "PROTON_PASS_KEY_PROVIDER=fs")
		}
	}
	return env
}

func passCommand(env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pass-cli", args...)
	cmd.Env = env
	return cmd.Output()
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".xeno-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

type event struct {
	Data struct {
		PaneID      string `json:"pane_id"`
		AgentStatus string `json:"agent_status"`
	} `json:"data"`
}

// label runs on Herdr's pane.agent_status_changed. When an agent finishes, it
// refreshes the audit records and labels the pane uncontrolled unless they hold
// both a positive and a negative control reason, or unverified if they cannot
// be fetched: missing evidence is not a clean result.
func label() (int, error) {
	var ev event
	if err := json.Unmarshal([]byte(os.Getenv("HERDR_PLUGIN_EVENT_JSON")), &ev); err != nil {
		return 1, fmt.Errorf("event: %w", err)
	}
	if ev.Data.AgentStatus != "done" || ev.Data.PaneID == "" {
		return 0, nil
	}

	state := "done"
	positive, negative, err := latestControls()
	if err != nil {
		fmt.Fprintln(os.Stderr, "xeno:", err)
		state = "done · unverified"
	} else if !(positive && negative) {
		state = "done · uncontrolled"
	}

	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	cmd := exec.Command(herdr, "pane", "report-metadata", ev.Data.PaneID,
		"--source", "xeno", "--state-label", "done="+state)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return 1, fmt.Errorf("report-metadata: %w", err)
	}
	return 0, nil
}

func latestControls() (positive, negative bool, err error) {
	if err := refresh(); err != nil {
		return false, false, err
	}
	dir, err := stateDir()
	if err != nil {
		return false, false, err
	}
	out, err := os.ReadFile(filepath.Join(dir, "reasons.json"))
	if err != nil {
		return false, false, err
	}
	return controls(out)
}

// controls reports whether the records hold a positive and a negative control
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
