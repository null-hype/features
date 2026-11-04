package main

// The Jev request is the same Noul transport used by the reason-label QA
// harness. The existing lights reporter owns Herdr metadata and findings.json.
import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const jevModel = "jev-1.14.0"

type reasonScore struct {
	Reason   string  `json:"reason"`
	Fits     float64 `json:"fits"`
	Achieved float64 `json:"achieved"`
}

type jevResponse struct {
	Model       string `json:"model"`
	LongContext bool   `json:"longContext"`
	Answers     map[string]struct {
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

func scoreReasons(client *http.Client, endpoint, key string, state any, reasons []string) ([]reasonScore, any, jevResponse, error) {
	questions := map[string]any{}
	for i, reason := range reasons {
		questions[fmt.Sprintf("fit%d", i)] = map[string]any{
			"type":         "noul",
			"instructions": "Does the observable activity in this Claude Code thread support this stated reason for launching it? Reason: " + reason,
			"criteria": map[string]string{
				"true":  "The requested task or observed work supports this specific purpose.",
				"false": "The thread supports a different purpose, contradicts this purpose, or offers no relevant evidence.",
			},
		}
		questions[fmt.Sprintf("achieved%d", i)] = map[string]any{
			"type":         "noul",
			"instructions": "Does the observed result demonstrate that the agent achieved this stated reason? Judge the work and outcomes, not just the request, intent, or a success exit code. Reason: " + reason,
			"criteria": map[string]string{
				"true":  "Observed work or results demonstrate the stated outcome.",
				"false": "The outcome is absent, contradicted, failed, or only asserted without supporting work.",
			},
		}
	}
	request := map[string]any{"model": jevModel, "state": state, "questions": questions}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, request, jevResponse{}, err
	}
	var answer jevResponse
	longContext := false
	for {
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, request, jevResponse{LongContext: longContext}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		if longContext {
			req.Header.Set("TypeSafe-Danger-Zone-Long-Context", "true")
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, request, jevResponse{LongContext: longContext}, errors.New("Jev request failed")
		}
		if resp.StatusCode != http.StatusOK {
			errorType := jevErrorType(resp.Body)
			resp.Body.Close()
			// Retry the identical full request only when TypeSafe identifies
			// the default context budget as the reason it could not run.
			if resp.StatusCode == http.StatusBadRequest && errorType == "max_tokens_exceeded" && !longContext {
				longContext = true
				continue
			}
			message := fmt.Sprintf("Jev HTTP %d", resp.StatusCode)
			if errorType != "" {
				message += " (" + errorType + ")"
			}
			return nil, request, jevResponse{LongContext: longContext}, errors.New(message)
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&answer)
		resp.Body.Close()
		// Local transport provenance, never trusted from a response field.
		answer.LongContext = longContext
		if decodeErr != nil || answer.Model != jevModel {
			return nil, request, answer, errors.New("invalid Jev response or model")
		}
		break
	}
	values := map[string]float64{}
	for name := range questions {
		p := answer.Answers[name].Noul
		if p == nil || !(*p >= 0 && *p <= 1) {
			return nil, request, answer, errors.New("missing or invalid Jev probability")
		}
		values[name] = *p
	}
	scores := []reasonScore{}
	for i, reason := range reasons {
		scores = append(scores, reasonScore{reason, values[fmt.Sprintf("fit%d", i)], values[fmt.Sprintf("achieved%d", i)]})
	}
	return scores, request, answer, nil
}

// Keep service error types useful without copying free-form response text,
// which may echo input. Only a short machine identifier is safe to retain.
func jevErrorType(body io.Reader) string {
	var response struct {
		Detail struct {
			ErrorType string `json:"error_type"`
		} `json:"detail"`
	}
	if json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&response) != nil {
		return ""
	}
	kind := response.Detail.ErrorType
	if len(kind) == 0 || len(kind) > 64 {
		return ""
	}
	for _, c := range kind {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	return kind
}

func reasonStatus(scores []reasonScore) string {
	matched, uncertain, failed := false, false, false
	for _, s := range scores {
		if s.Fits >= 0.8 {
			matched = true
			failed = failed || s.Achieved <= 0.2
			uncertain = uncertain || s.Achieved < 0.8
		} else if s.Fits > 0.2 {
			uncertain = true
		}
	}
	if failed {
		return "unfulfilled"
	}
	if uncertain {
		return "uncertain"
	}
	if matched {
		return "on-reason"
	}
	return "unmatched"
}

// Native Claude JSONL has user/assistant messages. The QA stream has init and
// result events; retain those too. Metadata and native ids are not sent to Jev.
func traceState(raw []byte) ([]any, int, error) {
	var events []any
	line, last := 0, 1
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 16<<20)
	for scan.Scan() {
		line++
		var e map[string]json.RawMessage
		if json.Unmarshal(scan.Bytes(), &e) != nil {
			return nil, line, fmt.Errorf("invalid Claude JSONL at line %d", line)
		}
		var kind string
		json.Unmarshal(e["type"], &kind)
		projection := map[string]any{"type": kind}
		switch kind {
		case "user", "assistant":
			if !json.Valid(e["message"]) {
				return nil, line, errors.New("Claude message is missing")
			}
			projection["message"] = e["message"]
		case "system":
			var subtype string
			json.Unmarshal(e["subtype"], &subtype)
			if subtype != "init" {
				continue
			}
			projection["enabled_tools"] = e["tools"]
		case "result":
			projection["status"], projection["reply"] = e["subtype"], e["result"]
			projection["turns"] = e["num_turns"]
		default:
			continue
		}
		events, last = append(events, projection), line
	}
	if err := scan.Err(); err != nil {
		return nil, last, errors.New("Claude trace line exceeds limit or cannot be read")
	}
	if len(events) == 0 {
		return nil, last, errors.New("Claude trace contains no usable messages")
	}
	return events, last, nil
}

func claudeState(raw []byte, final *string) (map[string]any, int, error) {
	trace, line, err := traceState(raw)
	if err != nil {
		return nil, line, err
	}
	state := map[string]any{"trace": trace}
	if final != nil {
		// Claude may flush the final assistant event only after Stop hooks finish.
		if strings.TrimSpace(*final) == "" {
			return nil, line, errors.New("Claude Stop hook requires last_assistant_message; assess TRACE after Claude exits instead")
		}
		state["stop_hook"] = map[string]string{"last_assistant_message": *final}
	}
	return state, line, nil
}

func assess(args []string) error {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return errors.New("assess runs inside a Herdr pane")
	}
	var hook struct {
		Transcript string `json:"transcript_path"`
		Cwd        string `json:"cwd"`
		Final      string `json:"last_assistant_message"`
	}
	isHook := len(args) == 1 && args[0] == "--hook"
	path := os.Getenv("XENO_CLAUDE_TRACE")
	if isHook {
		if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&hook) != nil || hook.Transcript == "" {
			return errors.New("Claude hook requires transcript_path")
		}
		path = hook.Transcript
	} else if len(args) == 1 {
		path = args[0]
	} else if len(args) > 1 {
		return errors.New("usage: xeno assess [--hook | TRACE]")
	}
	dir := os.Getenv("XENO_STATE_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, ".xeno")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	paneKey := sha256.Sum256([]byte(pane))
	tracked := filepath.Join(dir, fmt.Sprintf("trace-%x.json", paneKey[:8]))
	var previous struct {
		Trace  string `json:"trace"`
		Run    string `json:"run"`
		Prompt string `json:"prompt"`
	}
	if path == "" {
		last, _ := os.ReadFile(tracked)
		json.Unmarshal(last, &previous)
		path = previous.Trace
	}
	if path == "" {
		return errors.New("run xeno assess TRACE first (Claude Stop hooks can use --hook)")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	run := os.Getenv("XENO_RUN_DIR")
	if run == "" {
		run = previous.Run
		if run == "" {
			run = filepath.Join(hook.Cwd, ".witness/run")
		}
	}
	run, err = filepath.Abs(run)
	if err != nil {
		return err
	}
	promptFile := os.Getenv("XENO_PROMPT_FILE")
	if promptFile == "" {
		promptFile = previous.Prompt
	}
	if promptFile != "" {
		promptFile, err = filepath.Abs(promptFile)
		if err != nil {
			return err
		}
	}
	binding, _ := json.Marshal(map[string]string{"trace": path, "run": run, "prompt": promptFile})
	if err := atomicWrite(tracked, binding); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(run, "evidence"), 0700); err != nil {
		return err
	}

	assessment := map[string]any{"model": jevModel, "trace": path}
	line, status, message, sourceHash := 1, "unverified", "", ""
	judgeErr := func() error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return errors.New("cannot read Claude trace")
		}
		sum := sha256.Sum256(raw)
		sourceHash = "sha256:" + hex.EncodeToString(sum[:])
		assessment["transcriptHash"] = sourceHash
		var final *string
		if isHook {
			final = &hook.Final
			// The native log continues growing after this hook. Keep the hash
			// as evidence, rather than treating later appends as stale findings.
			sourceHash = ""
		}
		state, resultLine, err := claudeState(raw, final)
		line = resultLine
		if err != nil {
			return err
		}
		if err := refresh(); err != nil {
			return err
		}
		records, err := os.ReadFile(filepath.Join(dir, "reasons.json"))
		if err != nil {
			return errors.New("cannot read reasons.json; run xeno refresh first")
		}
		reasons, err := auditReasons(records)
		if err != nil {
			return err
		}
		assessment["audit"] = json.RawMessage(records)
		key := os.Getenv("TYPESAFE_API_KEY")
		if key == "" || strings.HasPrefix(key, "pass://") {
			return errors.New("TYPESAFE_API_KEY must be resolved before assessment")
		}
		if promptFile != "" {
			raw, err := os.ReadFile(promptFile)
			if err != nil {
				return errors.New("cannot read XENO_PROMPT_FILE")
			}
			state["prompt"] = string(raw)
		}
		scores, request, response, err := scoreReasons(&http.Client{Timeout: 30 * time.Second}, "https://api.typesafe.ai/v1/systemone", key, state, reasons)
		assessment["request"], assessment["response"] = request, response
		if err != nil {
			return err
		}
		assessment["scores"] = scores
		status = reasonStatus(scores)
		parts := []string{}
		for _, s := range scores {
			parts = append(parts, fmt.Sprintf("%q (fit %.2f, achieved %.2f)", s.Reason, s.Fits, s.Achieved))
		}
		message = status + ": " + strings.Join(parts, "; ") + ". Jev judgments over the retained trace; no causal attribution or release verdict is implied."
		return nil
	}()
	if judgeErr != nil {
		message = "unverified: " + judgeErr.Error()
	}
	severity := "warning"
	if status == "on-reason" {
		severity = "info"
	}
	assessment["status"] = status
	finding := map[string]any{
		"id": "xeno.reason", "location": map[string]any{"file": path, "line": line, "column": 1},
		"severity": severity, "message": message,
		"evidence": []string{"evidence/reason-assessment.json"},
	}
	if sourceHash != "" {
		finding["sourceHash"] = sourceHash
	}
	assessment["run"] = map[string]any{"version": 1, "findings": []any{finding}}
	out, err := json.MarshalIndent(assessment, "", "  ")
	if err != nil {
		return err
	}
	evidence := filepath.Join(run, "evidence", "reason-assessment.json")
	if err := atomicWrite(evidence, append(out, '\n')); err != nil {
		return err
	}
	if err := report("reason", evidence, run); err != nil {
		return err
	}
	return judgeErr
}
