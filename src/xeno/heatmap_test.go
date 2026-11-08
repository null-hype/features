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
	if _, err := os.Stat(filepath.Join(out, "reason-heatmap.html")); !os.IsNotExist(err) {
		t.Fatal("xeno wrote its own HTML heat map; the report renders the evidence")
	}
	for _, name := range []string{"reason-heatmap.json"} {
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

func writeSinkThread(t *testing.T, dir, name string, sessions ...string) string {
	t.Helper()
	span := func(start, session, prompt string) string {
		attrs := fmt.Sprintf(`{"key":"session.id","value":{"stringValue":%q}},{"key":"gen_ai.request.model","value":{"stringValue":"m"}}`, session)
		if prompt != "" {
			attrs += fmt.Sprintf(`,{"key":"gen_ai.prompt","value":{"stringValue":%q}},{"key":"gen_ai.completion","value":{"stringValue":"reply to %s"}}`, prompt, prompt)
		}
		return fmt.Sprintf(`{"startTimeUnixNano":%q,"attributes":[%s]}`, start, attrs)
	}
	lines := []string{
		fmt.Sprintf(`{"resourceSpans":[{"scopeSpans":[{"spans":[%s,%s]}]}]}`, span("20", sessions[0], "second call"), span("15", sessions[0], "")),
		fmt.Sprintf(`{"resourceSpans":[{"scopeSpans":[{"spans":[%s]}]}]}`, span("10", sessions[len(sessions)-1], "first call")),
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An OpenRouter agent's thread is its session file in the toolbox's trace
// sink: its model calls in start order, with spans that carry no call left out.
func TestHeatmapReadsTraceSinkSessions(t *testing.T) {
	audit, _ := heatmapInputs(t)
	dir := t.TempDir()
	path := writeSinkThread(t, dir, "sess.ndjson", "sess-1")
	raw, _ := os.ReadFile(path)
	if !isSinkThread(raw) {
		t.Fatal("sink file not recognised")
	}
	state, session, err := sinkThread(raw)
	if err != nil || session != "sess-1" {
		t.Fatalf("session %q, err %v", session, err)
	}
	trace := state["trace"].([]any)
	if len(trace) != 2 || trace[0].(map[string]any)["prompt"] != "first call" || trace[1].(map[string]any)["prompt"] != "second call" {
		t.Fatalf("calls not in start order, or a span without a call kept: %+v", trace)
	}
	if _, _, err := sinkThread(func() []byte {
		b, _ := os.ReadFile(writeSinkThread(t, dir, "mixed.ndjson", "sess-1", "sess-2"))
		return b
	}()); err == nil {
		t.Fatal("a file holding two sessions was accepted as one thread")
	}

	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body.State), "sess-1") || !strings.Contains(string(body.State), "first call") {
			t.Fatalf("sink state: %s", body.State)
		}
		answers := map[string]any{}
		for q := range body.Questions {
			answers[q] = map[string]float64{"noul": .5}
		}
		raw, _ := json.Marshal(map[string]any{"model": jevModel, "answers": answers})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	evidence, err := scoreHeatmap(client, "https://example.invalid", "fixture", audit, []string{path}, "Classify health", "Deploy a payment gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Threads) != 1 || evidence.Threads[0].Session != "sess-1" || len(evidence.Threads[0].Scores) != 3 {
		t.Fatalf("sink thread: %+v", evidence.Threads)
	}
}

func writeAgentSession(t *testing.T, dir, name string, ids ...string) string {
	t.Helper()
	lines := []string{`{"type":"title","title":"planner","v":1}`}
	for _, id := range ids {
		lines = append(lines, fmt.Sprintf(`{"type":"session","version":3,"id":%q,"cwd":"/work/app"}`, id))
	}
	lines = append(lines,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"Build the pipeline"}]}}`,
		`{"type":"custom","customType":"yoars2-cursor","data":{"seq":1}}`,
		`{"type":"custom_message","customType":"yoars2-norms","content":"[YOARS norms] No module reaches the summary unreviewed"}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"glob","arguments":{"path":"/work/app"}}]}}`,
		`{"type":"message","message":{"role":"toolResult","toolName":"glob","content":[{"type":"text","text":"SPEC.md"}]}}`,
		`{"type":"compaction","summary":"Coordinated parse, normalize and report"}`,
	)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A yoars agent's thread is its version 3 session file: its messages, the room
// messages it was shown and its compaction summaries, without relay bookkeeping.
func TestHeatmapReadsAgentSessions(t *testing.T) {
	audit, _ := heatmapInputs(t)
	dir := t.TempDir()
	path := writeAgentSession(t, dir, "session-planner.jsonl", "01a0a7bf")
	raw, _ := os.ReadFile(path)
	if !isAgentSession(raw) || isSinkThread(raw) {
		t.Fatal("agent session not recognised as one")
	}
	state, session, err := agentSession(raw)
	if err != nil || session != "01a0a7bf" {
		t.Fatalf("session %q, err %v", session, err)
	}
	kinds := []string{}
	for _, e := range state["trace"].([]any) {
		kinds = append(kinds, e.(map[string]any)["type"].(string))
	}
	if strings.Join(kinds, " ") != "message custom_message message message compaction" {
		t.Fatalf("thread events: %v", kinds)
	}
	if _, _, err := agentSession(func() []byte {
		b, _ := os.ReadFile(writeAgentSession(t, dir, "mixed.jsonl", "one", "two"))
		return b
	}()); err == nil {
		t.Fatal("a file holding two sessions was accepted as one thread")
	}

	client := &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		var body struct {
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body.State), "yoars2-cursor") || !strings.Contains(string(body.State), "No module reaches the summary") {
			t.Fatalf("agent session state: %s", body.State)
		}
		answers := map[string]any{}
		for q := range body.Questions {
			answers[q] = map[string]float64{"noul": .5}
		}
		raw, _ := json.Marshal(map[string]any{"model": jevModel, "answers": answers})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	evidence, err := scoreHeatmap(client, "https://example.invalid", "fixture", audit, []string{path}, "Classify health", "Deploy a payment gateway")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Threads) != 1 || evidence.Threads[0].Session != "01a0a7bf" || len(evidence.Threads[0].Scores) != 3 {
		t.Fatalf("agent session thread: %+v", evidence.Threads)
	}
}
