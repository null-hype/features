package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestJevTransport(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		code           int
		valid          bool
	}{
		{"recorded-fit-with-separate-outcome", `{"model":"jev-1.14.0","answers":{"fit0":{"noul":0.94},"achieved0":{"noul":0.05}}}`, 200, true},
		{"missing-outcome", `{"model":"jev-1.14.0","answers":{"fit0":{"noul":0.94}}}`, 200, false},
		{"wrong-model", `{"model":"other","answers":{}}`, 200, false},
		{"invalid-probability", `{"model":"jev-1.14.0","answers":{"fit0":{"noul":1.1},"achieved0":{"noul":0.8}}}`, 200, false},
		{"unavailable", `service unavailable`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("TypeSafe-Danger-Zone-Long-Context") != "" {
					t.Fatal("ordinary request enabled long context")
				}
				if r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Fatal("missing authorization")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 3 || body["state"] == nil || body["questions"] == nil || body["model"] == nil {
					t.Fatal("wrong Jev payload")
				}
				if strings.Contains(string(body["state"]), "expected") {
					t.Fatal("grading leaked into the state")
				}
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}
			scores, _, _, err := scoreReasons(client, "https://example.invalid", "fixture-key", map[string]any{"trace": "observed work"}, []string{"Classify probe health."})
			if (err == nil) != tc.valid {
				t.Fatalf("error = %v, want valid %v", err, tc.valid)
			}
			if tc.valid && (scores[0].Fits != 0.94 || scores[0].Achieved != 0.05) {
				t.Fatal("scores collapsed")
			}
		})
	}
}

func TestJevLongContextFallback(t *testing.T) {
	const budget = `{"detail":{"error_type":"max_tokens_exceeded"}}`
	const success = `{"model":"jev-1.14.0","answers":{"fit0":{"noul":0.94},"achieved0":{"noul":0.85}}}`
	for _, tc := range []struct {
		name, initial, retry, wantError string
		status, wantCalls               int
		long                            bool
	}{
		{"budget-retry", budget, success, "", 400, 2, true},
		{"budget-still-exceeded", budget, budget, "Jev HTTP 400 (max_tokens_exceeded)", 400, 2, true},
		{"other-400", `{"detail":{"error_type":"invalid_state"}}`, "", "Jev HTTP 400 (invalid_state)", 400, 1, false},
		{"wrong-status", budget, "", "Jev HTTP 413 (max_tokens_exceeded)", 413, 1, false},
		{"unsafe-error-type", `{"detail":{"error_type":"max_tokens_exceeded: secret-input"}}`, "", "Jev HTTP 400", 400, 1, false},
		{"not-structured-error", `max_tokens_exceeded`, "", "Jev HTTP 400", 400, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var original []byte
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if calls == 1 {
					original = body
					if r.Header.Get("TypeSafe-Danger-Zone-Long-Context") != "" {
						t.Fatal("long context was enabled before a budget rejection")
					}
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.initial))}, nil
				}
				if calls > tc.wantCalls {
					t.Fatal("unexpected extra retry")
				}
				if string(body) != string(original) || r.Header.Get("TypeSafe-Danger-Zone-Long-Context") != "true" {
					t.Fatal("retry changed the full payload or omitted the documented header")
				}
				if r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("Content-Type") != "application/json" {
					t.Fatal("retry changed ordinary transport headers")
				}
				status := http.StatusOK
				if tc.wantError != "" {
					status = http.StatusBadRequest
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(tc.retry))}, nil
			})}
			scores, _, response, err := scoreReasons(client, "https://example.invalid", "fixture-key", map[string]any{"trace": strings.Repeat("full observed work ", 10000)}, []string{"Classify probe health."})
			if calls != tc.wantCalls || response.LongContext != tc.long {
				t.Fatalf("calls %d, long context %v", calls, response.LongContext)
			}
			if tc.wantError == "" {
				if err != nil || len(scores) != 1 || scores[0].Fits != .94 || scores[0].Achieved != .85 {
					t.Fatalf("retry lost scores: %v, %v", scores, err)
				}
			} else if err == nil || err.Error() != tc.wantError || scores != nil {
				t.Fatalf("error = %v, want %q; scores %v", err, tc.wantError, scores)
			}
			encoded, _ := json.Marshal(response)
			if !strings.Contains(string(encoded), `"longContext":`) {
				t.Fatal("transport provenance missing from retained response")
			}
		})
	}
}

func TestReasonVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scores []reasonScore
		want   string
	}{
		{"fulfilled", []reasonScore{{"own", 0.94, 0.97}}, "on-reason"},
		{"fits-but-failed", []reasonScore{{"own", 0.94, 0.08}}, "unfulfilled"},
		{"crossed", []reasonScore{{"other", 0.17, 0.08}}, "unmatched"},
		{"sparse", []reasonScore{{"own", 0.53, 0.97}}, "uncertain"},
		{"uncertain-outcome", []reasonScore{{"own", 0.94, 0.53}}, "uncertain"},
		{"multiple-fulfilled-purposes", []reasonScore{{"one", 0.94, 0.9}, {"two", 0.97, 0.9}}, "on-reason"},
		{"one-failed-purpose", []reasonScore{{"one", 0.94, 0.9}, {"two", 0.97, 0.1}}, "unfulfilled"},
		{"boundaries", []reasonScore{{"one", 0.8, 0.8}, {"other", 0.2, 0.1}}, "on-reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasonStatus(tc.scores); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestClaudeFormats(t *testing.T) {
	for _, raw := range []string{
		`{"type":"user","sessionId":"native-id","message":{"role":"user","content":"Classify health"}}
{"type":"assistant","sessionId":"native-id","message":{"role":"assistant","content":[{"type":"text","text":"checkout: unhealthy"}]}}`,
		`{"type":"system","subtype":"init","session_id":"native-id","tools":[]}
{"type":"result","subtype":"success","session_id":"native-id","num_turns":1,"result":"checkout: unhealthy"}`,
	} {
		events, line, err := traceState([]byte(raw))
		if err != nil || len(events) != 2 || line != 2 {
			t.Fatalf("trace parse failed: %v", err)
		}
		encoded, _ := json.Marshal(events)
		if strings.Contains(string(encoded), "native-id") {
			t.Fatal("session id leaked into scoring input")
		}
	}
	for _, raw := range []string{"", "invalid json", `{"type":"assistant"}`, `{"type":"irrelevant"}`} {
		if _, _, err := traceState([]byte(raw)); err == nil {
			t.Fatalf("accepted unusable trace %s", raw)
		}
	}
}

func TestStopBeforeTranscriptFlush(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":"Classify catalog HTTP 204; checkout HTTP 503."}}`)
	final := "catalog: healthy\ncheckout: unhealthy"
	state, line, err := claudeState(raw, &final)
	if err != nil || line != 1 {
		t.Fatalf("hook state: %v", err)
	}
	encoded, _ := json.Marshal(state)
	if !strings.Contains(string(encoded), "checkout: unhealthy") || !strings.Contains(string(encoded), "last_assistant_message") {
		t.Fatal("Stop hook lost the final response before the transcript flushed")
	}
	missing := ""
	if _, _, err := claudeState(raw, &missing); err == nil {
		t.Fatal("scored an incomplete hook as an observed outcome")
	}
	explicit, _, err := claudeState(raw, nil)
	if err != nil || explicit["stop_hook"] != nil {
		t.Fatal("explicit trace assessment invented a hook response")
	}
}

func TestRecordedJudgments(t *testing.T) {
	raw, err := os.ReadFile("../../test/xeno/fixtures/reason-judgments.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Name    string `json:"name"`
			Request struct {
				State     json.RawMessage `json:"state"`
				Questions json.RawMessage `json:"questions"`
			} `json:"request"`
			Response json.RawMessage `json:"response"`
			Reasons  []string        `json:"reasons"`
			Expected string          `json:"expected_status"`
			Observed string          `json:"observed_status"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for _, c := range capture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				var sent struct {
					Questions any `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Fatal(err)
				}
				generated, _ := json.Marshal(sent.Questions)
				var original any
				if err := json.Unmarshal(c.Request.Questions, &original); err != nil {
					t.Fatal(err)
				}
				recorded, _ := json.Marshal(original)
				if string(generated) != string(recorded) {
					t.Fatal("question changed since the captured evaluation")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(c.Response)))}, nil
			})}
			scores, _, _, err := scoreReasons(client, "https://example.invalid", "fixture-key", c.Request.State, c.Reasons)
			if err != nil {
				t.Fatal(err)
			}
			if got := reasonStatus(scores); got != c.Observed {
				t.Fatalf("got %s, recorded %s", got, c.Observed)
			}
			if c.Expected != c.Observed {
				t.Logf("Recorded missed prediction: expected %s, observed %s", c.Expected, c.Observed)
			}
		})
	}
}
