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
	if len(args) > 0 {
		switch args[0] {
		case "dispatch":
			if err := dispatch(args[1:]); err != nil {
				return 1, err
			}
			return 0, nil
		case "heatmap":
			if err := heatmap(args[1:]); err != nil {
				return 1, err
			}
			return 0, nil
		case "assess":
			if err := assess(args[1:]); err != nil {
				return 1, err
			}
			return 0, nil
		case "claim", "verifier", "falsifier", "jev", "clear":
			if err := report(args...); err != nil {
				return 1, err
			}
			return 0, nil
		}
	}
	if len(args) != 1 || (args[0] != "init" && args[0] != "refresh") {
		return 2, errors.New("usage: xeno init | refresh | dispatch --project PROJECT [--env-file FILE] [--output DIR] [--dry-run] | assess [--hook | TRACE] | heatmap --positive REASON --negative REASON [--reasons FILE] [--output DIR] TRACE... | claim | verifier | falsifier | jev | clear")
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

func report(args ...string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	bin, err = filepath.EvalSymlinks(bin)
	if err != nil {
		return err
	}
	cmd := exec.Command("bash", append([]string{filepath.Join(filepath.Dir(bin), "report.sh")}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func refresh() error {
	dir := os.Getenv("XENO_STATE_DIR")
	if dir == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(userHome, ".xeno")
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
