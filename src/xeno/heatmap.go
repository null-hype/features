package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type heatmapRow struct {
	Reason string `json:"reason"`
	Source string `json:"source"`
}

type heatmapThread struct {
	Trace    string        `json:"trace"`
	Hash     string        `json:"transcriptHash"`
	Session  string        `json:"claudeSession,omitempty"`
	Scores   []reasonScore `json:"scores,omitempty"`
	Request  any           `json:"request,omitempty"`
	Response jevResponse   `json:"response"`
	Error    string        `json:"error,omitempty"`
}

type heatmapEvidence struct {
	Version         int             `json:"version"`
	Model           string          `json:"model"`
	ObservedAt      string          `json:"observedAt"`
	Audit           string          `json:"audit"`
	AuditHash       string          `json:"auditHash"`
	Rows            []heatmapRow    `json:"rows"`
	Threads         []heatmapThread `json:"threads"`
	PositiveControl string          `json:"positiveControl"`
	NegativeControl string          `json:"negativeControl"`
}

func auditReasons(raw []byte) ([]string, error) {
	var records []struct {
		Action  string `json:"action"`
		Payload struct {
			Reason string `json:"reason"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &records) != nil || records == nil {
		return nil, errors.New("invalid reasons.json")
	}
	reasons, seen := []string{}, map[string]bool{}
	for _, record := range records {
		reason := record.Payload.Reason
		if record.Action == "ItemRead" && strings.TrimSpace(reason) != "" && !seen[reason] {
			reasons, seen[reason] = append(reasons, reason), true
		}
	}
	if len(reasons) == 0 {
		return nil, errors.New("no ItemRead reasons to assess")
	}
	return reasons, nil
}

func heatmap(args []string) error {
	flags := flag.NewFlagSet("heatmap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	positive := flags.String("positive", "", "reason expected to fit at least one selected thread")
	negative := flags.String("negative", "", "reason expected to fit none of the selected threads")
	audit := flags.String("reasons", "", "retained Pass monitor JSON; otherwise refresh the signed-in agent")
	output := flags.String("output", ".witness/run/evidence", "private output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*positive) == "" || strings.TrimSpace(*negative) == "" || *positive == *negative || flags.NArg() == 0 {
		return errors.New("usage: xeno heatmap --positive REASON --negative REASON [--reasons FILE] [--output DIR] TRACE...")
	}
	if *audit == "" {
		if err := refresh(); err != nil {
			return err
		}
		dir := os.Getenv("XENO_STATE_DIR")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir = filepath.Join(home, ".xeno")
		}
		*audit = filepath.Join(dir, "reasons.json")
	}
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" || strings.HasPrefix(key, "pass://") {
		return errors.New("TYPESAFE_API_KEY must be resolved before assessment")
	}
	evidence, err := scoreHeatmap(&http.Client{Timeout: 30 * time.Second}, "https://api.typesafe.ai/v1/systemone", key, *audit, flags.Args(), *positive, *negative)
	if err != nil {
		return err
	}
	if err := writeHeatmap(*output, evidence); err != nil {
		return err
	}
	fmt.Println(filepath.Join(*output, "reason-heatmap.html"))
	for _, thread := range evidence.Threads {
		if thread.Error != "" {
			return errors.New("heat map includes unverified threads; inspect reason-heatmap.json")
		}
	}
	return nil
}

// Explicit traces select the session's threads. No matching or ownership is
// inferred from a filename, workspace, reason score, or invented run identity.
func scoreHeatmap(client *http.Client, endpoint, key, auditPath string, paths []string, positive, negative string) (heatmapEvidence, error) {
	evidence := heatmapEvidence{Version: 1, Model: jevModel, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	if strings.TrimSpace(positive) == "" || strings.TrimSpace(negative) == "" || positive == negative || len(paths) == 0 {
		return evidence, errors.New("heat map requires distinct positive/negative controls and at least one trace")
	}
	auditPath, err := filepath.Abs(auditPath)
	if err != nil {
		return evidence, err
	}
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		return evidence, errors.New("cannot read Pass audit")
	}
	reasons, err := auditReasons(raw)
	if err != nil {
		return evidence, err
	}
	evidence.Audit, evidence.AuditHash = auditPath, contentHash(raw)
	for _, reason := range reasons {
		evidence.Rows = append(evidence.Rows, heatmapRow{reason, "audit"})
	}
	evidence.Rows = append(evidence.Rows, heatmapRow{positive, "positive control"}, heatmapRow{negative, "negative control"})
	// A control may also be a real audit reason; ask the existing scorer only once.
	seenReasons := map[string]bool{}
	for _, reason := range reasons {
		seenReasons[reason] = true
	}
	for _, reason := range []string{positive, negative} {
		if !seenReasons[reason] {
			reasons = append(reasons, reason)
			seenReasons[reason] = true
		}
	}
	states := []any{}
	seenPaths, seenSessions := map[string]bool{}, map[string]bool{}
	for _, path := range paths {
		path, err = filepath.Abs(path)
		if err != nil {
			return evidence, err
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return evidence, errors.New("cannot resolve Claude trace")
		}
		if seenPaths[path] {
			return evidence, errors.New("duplicate Claude trace")
		}
		seenPaths[path] = true
		raw, err := os.ReadFile(path)
		if err != nil {
			return evidence, errors.New("cannot read Claude trace")
		}
		state, _, err := claudeState(raw, nil)
		if err != nil {
			return evidence, err
		}
		session, err := traceSession(raw)
		if err != nil {
			return evidence, err
		}
		if session != "" && seenSessions[session] {
			return evidence, errors.New("multiple traces represent the same Claude thread")
		}
		if session != "" {
			seenSessions[session] = true
		}
		evidence.Threads = append(evidence.Threads, heatmapThread{Trace: path, Hash: contentHash(raw), Session: session})
		states = append(states, state)
	}
	for i, state := range states {
		thread := &evidence.Threads[i]
		thread.Scores, thread.Request, thread.Response, err = scoreReasons(client, endpoint, key, state, reasons)
		if err != nil {
			thread.Error = err.Error()
		}
	}
	evidence.PositiveControl = heatmapControl(evidence.Threads, positive, true)
	evidence.NegativeControl = heatmapControl(evidence.Threads, negative, false)
	return evidence, nil
}

func contentHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func traceSession(raw []byte) (string, error) {
	var session string
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var event struct {
			Session       string `json:"sessionId"`
			StreamSession string `json:"session_id"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return "", errors.New("invalid Claude trace")
		}
		for _, id := range []string{event.Session, event.StreamSession} {
			if id == "" {
				continue
			}
			if session != "" && id != session {
				return "", errors.New("Claude trace contains more than one thread")
			}
			session = id
		}
	}
	return session, nil
}

func heatmapControl(threads []heatmapThread, reason string, positive bool) string {
	unknown, matched := false, false
	for _, thread := range threads {
		found := false
		if thread.Error == "" {
			for _, score := range thread.Scores {
				if score.Reason != reason {
					continue
				}
				found = true
				if positive {
					matched = matched || score.Fits >= .8
				} else if score.Fits > .2 {
					return "missed"
				}
			}
		}
		unknown = unknown || !found
	}
	if positive && matched {
		return "held"
	}
	if unknown || len(threads) == 0 {
		return "unverified"
	}
	if positive {
		return "missed"
	}
	return "held"
}

func writeHeatmap(dir string, evidence heatmapEvidence) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(dir, "reason-heatmap.json"), append(data, '\n')); err != nil {
		return err
	}
	tmpl, err := template.New("heatmap").Funcs(template.FuncMap{
		"name": func(t heatmapThread) string {
			if t.Session != "" {
				return t.Session
			}
			return filepath.Base(t.Trace)
		},
		"cell": func(t heatmapThread, reason string) *reasonScore {
			if t.Error != "" {
				return nil
			}
			for _, s := range t.Scores {
				if s.Reason == reason {
					return &s
				}
			}
			return nil
		},
		"shade": func(p float64) string {
			if p >= .8 {
				return "high"
			}
			if p <= .2 {
				return "low"
			}
			return "middle"
		},
	}).Parse(heatmapHTML)
	if err != nil {
		return err
	}
	var html bytes.Buffer
	if err := tmpl.Execute(&html, evidence); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "reason-heatmap.html"), html.Bytes())
}

const heatmapHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><title>Xeno — reasons × threads</title>
<style>body{font:16px/1.5 system-ui,sans-serif;color:#17212b;margin:2rem;max-width:100rem}h1{font-size:1.6rem}a{color:#164fa0}table{border-collapse:collapse;min-width:100%}th,td{border:1px solid #aeb9c3;padding:.8rem;vertical-align:top}thead th{max-width:18rem;overflow-wrap:anywhere}tbody th{text-align:left;min-width:18rem;max-width:35rem;font-weight:500}td{text-align:center;min-width:9rem}.high{background:#b2d9c8}.middle{background:#f9dfa1}.low{background:#e9edf1}.unknown{background:#fff;color:#6b3232}small{display:block;font-size:.8rem}code{overflow-wrap:anywhere}.scroll{overflow:auto}details{margin-top:1rem}summary{cursor:pointer}li{margin:.6rem 0}</style>
<h1>Xeno — reasons × threads</h1>
<p>Jev reason-fit scores for the explicitly selected Claude threads. Color shows fit: high ≥ 0.80, intermediate &gt; 0.20, low ≤ 0.20. Each cell also retains the separate achievement judgment.</p>
<p>Positive control (fits at least one thread): <strong>{{.PositiveControl}}</strong>. Negative control (fits no thread): <strong>{{.NegativeControl}}</strong>.</p>
<div class="scroll"><table><caption>Observed {{.ObservedAt}} · {{.Model}}</caption><thead><tr><th scope="col">Reason</th>{{range .Threads}}<th scope="col" title="{{.Trace}}">{{name .}}</th>{{end}}</tr></thead><tbody>
{{range .Rows}}{{$row := .}}<tr><th scope="row">{{.Reason}}<small>{{.Source}}</small></th>{{range $.Threads}}{{with cell . $row.Reason}}<td class="{{shade .Fits}}"><strong>{{printf "%.2f" .Fits}}</strong><small>achieved {{printf "%.2f" .Achieved}}</small></td>{{else}}<td class="unknown">unverified<small>{{.Error}}</small></td>{{end}}{{end}}</tr>{{end}}
</tbody></table></div>
<p><a href="reason-heatmap.json">Retained scoring evidence</a> includes the exact requests, responses, trace paths and content hashes. Scores are model judgments; they do not establish causal attribution or project completion.</p>
<details><summary>Inputs</summary><p>Audit: <code>{{.Audit}}</code><br><code>{{.AuditHash}}</code></p><ul>{{range .Threads}}<li><code>{{.Trace}}</code><br><code>{{.Hash}}</code></li>{{end}}</ul></details>
</html>`
