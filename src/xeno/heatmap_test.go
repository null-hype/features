package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func heatmapInputs(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	audit := filepath.Join(dir, "reasons.json")
	if err := os.WriteFile(audit, []byte(`[{"action":"ItemRead","payload":{"reason":"Classify health"}},{"action":"ItemRead","payload":{"reason":"Classify health"}},{"action":"ItemRead","payload":{"reason":"Redact <secret>"}},{"action":"Login","payload":{"reason":"not a read"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for i, result := range []string{"checkout unhealthy", "redacted credential"} {
		path := filepath.Join(dir, fmt.Sprintf("thread-%d.jsonl", i))
		raw := fmt.Sprintf("{\"type\":\"assistant\",\"sessionId\":\"claude-%d\",\"message\":{\"role\":\"assistant\",\"content\":%q}}", i, result)
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return audit, paths
}

func TestHeatmapScoresEveryThreadWithExistingQuestions(t *testing.T) {
	audit, paths := heatmapInputs(t)
	calls := 0
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			Model     string                     `json:"model"`
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != jevModel || len(body.Questions) != 6 {
			t.Fatalf("unexpected scorer request: %+v", body)
		}
		if strings.Contains(string(body.State), "positive") || strings.Contains(string(body.State), "negative") || strings.Contains(string(body.State), "claude-") {
			t.Fatal("control expectation or native identity leaked into model state")
		}
		answers := map[string]any{}
		for i, p := range [][]float64{{.94, .10, .04}, {.12, .97, .03}}[calls] {
			answers[fmt.Sprintf("fit%d", i)] = map[string]float64{"noul": p}
			answers[fmt.Sprintf("achieved%d", i)] = map[string]float64{"noul": p - .01}
		}
		calls++
		raw, _ := json.Marshal(map[string]any{"model": jevModel, "answers": answers})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	evidence, err := scoreHeatmap(client, "https://example.invalid", "fixture", audit, paths, "Classify health", "Deploy a payment gateway")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(evidence.Rows) != 4 || len(evidence.Threads) != 2 {
		t.Fatalf("wrong dimensions: %+v", evidence)
	}
	if evidence.PositiveControl != "held" || evidence.NegativeControl != "held" {
		t.Fatalf("controls: %+v", evidence)
	}
	if evidence.Threads[0].Scores[0].Fits != .94 || evidence.Threads[1].Scores[1].Fits != .97 {
		t.Fatal("matrix columns lost their scores")
	}
	out := t.TempDir()
	if err := writeHeatmap(out, evidence); err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(out, "reason-heatmap.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "Redact <secret>") || !strings.Contains(string(html), "Redact &lt;secret&gt;") {
		t.Fatal("unescaped reason in HTML")
	}
	if !strings.Contains(string(html), "0.94") || !strings.Contains(string(html), "positive control") {
		t.Fatal("scores or control source missing")
	}
	for _, name := range []string{"reason-heatmap.html", "reason-heatmap.json"} {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("private evidence mode = %o", info.Mode().Perm())
		}
	}
}

func TestHeatmapRejectsCombinedAndDuplicateThreadsBeforeScoring(t *testing.T) {
	audit, paths := heatmapInputs(t)
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) { t.Fatal("scored invalid inputs"); return nil, nil })}
	for _, selected := range [][]string{{paths[0], paths[0]}, {paths[0], filepath.Join(t.TempDir(), "absent")}} {
		if _, err := scoreHeatmap(client, "https://example.invalid", "fixture", audit, selected, "positive", "negative"); err == nil {
			t.Fatal("accepted duplicate/missing trace")
		}
	}
	raw, _ := os.ReadFile(paths[0])
	if err := os.WriteFile(paths[1], raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := scoreHeatmap(client, "https://example.invalid", "fixture", audit, paths, "positive", "negative"); err == nil {
		t.Fatal("same Claude thread counted twice")
	}
	if _, err := traceSession([]byte(`{"sessionId":"one"}
{"session_id":"two"}`)); err == nil {
		t.Fatal("accepted a combined trace")
	}
}

func TestHeatmapUnavailableCellsAndMissedControls(t *testing.T) {
	audit, paths := heatmapInputs(t)
	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})}
	evidence, err := scoreHeatmap(client, "https://example.invalid", "fixture", audit, paths, "positive", "negative")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.PositiveControl != "unverified" || evidence.NegativeControl != "unverified" || evidence.Threads[0].Error == "" {
		t.Fatal("instrument failure hidden as control success")
	}
	if err := writeHeatmap(t.TempDir(), evidence); err != nil {
		t.Fatal(err)
	}
	threads := []heatmapThread{{Scores: []reasonScore{{Reason: "positive", Fits: .7}, {Reason: "negative", Fits: .3}}}}
	if heatmapControl(threads, "positive", true) != "missed" || heatmapControl(threads, "negative", false) != "missed" {
		t.Fatal("control miss was hidden")
	}
}

func TestAuditReasonsRejectsAbsentReads(t *testing.T) {
	for _, raw := range []string{"null", `{}`, `[]`, `[{"action":"ItemRead","payload":{"reason":" "}}]`, `[{"action":"Login","payload":{"reason":"Read"}}]`} {
		if _, err := auditReasons([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
