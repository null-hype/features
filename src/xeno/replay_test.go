package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Replay opens one workspace, splits a pane per further thread, names each
// pane for its agent and runs xeno assess in it; the key travels only as a
// Pass reference.
func TestReplayOpensAPanePerThread(t *testing.T) {
	dir := t.TempDir()
	audit := filepath.Join(dir, "reasons.json")
	os.WriteFile(audit, []byte(`[{"action":"ItemRead","payload":{"reason":"parse"}}]`), 0600)
	threads := []string{writeAgentSession(t, dir, "session-auth.jsonl", "a"), writeAgentSession(t, dir, "session-ui.jsonl", "b")}
	t.Setenv("TYPESAFE_API_KEY", "pass://infra/typesafe.ai/TYPESAFE_API_KEY")
	cwd, _ := os.Getwd()
	var calls []string
	split := 0
	saved := herdr
	defer func() { herdr = saved }()
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
	if err := replay(append([]string{"--reasons", audit, "--label", "yoars run"}, threads...)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"workspace create --label yoars run --cwd " + cwd + " --no-focus --env TYPESAFE_API_KEY=pass://infra/typesafe.ai/TYPESAFE_API_KEY",
		"pane rename w9:p1 auth",
		"pane split w9:p1 --direction right --no-focus --env TYPESAFE_API_KEY=pass://infra/typesafe.ai/TYPESAFE_API_KEY",
		"pane rename w9:p2 ui",
	}
	got := []string{}
	for _, c := range calls {
		if !strings.HasPrefix(c, "pane run") {
			got = append(got, c)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("herdr calls:\n%s", strings.Join(calls, "\n"))
	}
	runs := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "pane run") {
			runs++
			if !strings.Contains(c, "XENO_RUN_DIR='"+filepath.Join(cwd, ".witness", "run")) || !strings.Contains(c, "pass-cli run -- ") || !strings.Contains(c, " assess --reasons '"+audit+"' ") {
				t.Fatalf("pane command: %s", c)
			}
		}
	}
	if runs != 2 {
		t.Fatalf("%d panes ran assess", runs)
	}
}

func TestReplayRefusesAResolvedKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "sk-live-secret")
	saved := herdr
	defer func() { herdr = saved }()
	herdr = func(args ...string) ([]byte, error) {
		t.Fatal("called herdr with a resolved key in the environment")
		return nil, nil
	}
	if err := replay([]string{"--reasons", "r.json", "t.jsonl"}); err == nil {
		t.Fatal("replay accepted a resolved key")
	}
}
