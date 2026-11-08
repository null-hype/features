package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Replay opens one workspace, splits a pane per further session, resumes each
// session's Pi agent there from a copy, and assesses the thread with the
// controls for that pane.
func TestReplayResumesAnAgentPerSession(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	audit := filepath.Join(dir, "reasons.json")
	os.WriteFile(audit, []byte(`[{"action":"ItemRead","payload":{"reason":"parse"}}]`), 0600)
	threads := []string{writeAgentSession(t, dir, "session-auth.jsonl", "a"), writeAgentSession(t, dir, "session-ui.jsonl", "b")}
	var calls []string
	split := 0
	savedHerdr, savedAssess := herdr, assessPane
	defer func() { herdr, assessPane = savedHerdr, savedAssess }()
	herdr = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] + " " + args[1] {
		case "workspace create":
			return []byte(`{"result":{"workspace":{"workspace_id":"w9"},"root_pane":{"pane_id":"w9:p1"}}}`), nil
		case "pane split":
			split++
			return []byte(fmt.Sprintf(`{"result":{"pane":{"pane_id":"w9:p%d"}}}`, split+1)), nil
		}
		return []byte(`{"result":{}}`), nil
	}
	var assessed [][]string
	assessPane = func(args []string) error {
		assessed = append(assessed, append([]string{os.Getenv("HERDR_PANE_ID")}, args...))
		return nil
	}
	if err := replay(append([]string{"--reasons", audit, "--label", "yoars run 225035", "--positive", "Build it.", "--negative", "Deploy k8s."}, threads...)); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	copies := filepath.Join(cwd, ".witness", "replay", "un225035")
	want := []string{
		"workspace create --label yoars run 225035 --cwd " + cwd + " --no-focus",
		"agent start auth-un225035 --kind pi --pane w9:p1 -- --session " + filepath.Join(copies, "session-auth.jsonl"),
		"pane split w9:p1 --direction right --no-focus",
		"agent start ui-un225035 --kind pi --pane w9:p2 -- --session " + filepath.Join(copies, "session-ui.jsonl"),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("herdr calls:\n%s", strings.Join(calls, "\n"))
	}
	for i, pane := range []string{"w9:p1", "w9:p2"} {
		got := assessed[i]
		if got[0] != pane || strings.Join(got[1:7], " ") != "--reasons "+audit+" --positive Build it. --negative Deploy k8s." || got[7] != threads[i] {
			t.Fatalf("assess %d: %v", i, got)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(copies, "session-auth.jsonl")); len(raw) == 0 {
		t.Fatal("the resumed copy is missing")
	}
}

func TestAgentSuffix(t *testing.T) {
	for label, want := range map[string]string{"yoars qwen3.6-35b-010514": "5b010514", "x": "x", "--": "replay"} {
		if got := agentSuffix(label); got != want {
			t.Errorf("agentSuffix(%q) = %q, want %q", label, got, want)
		}
	}
}
