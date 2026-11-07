package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const linearEndpoint = "https://api.linear.app/graphql"

// Blockers are the issues on the other end of "blocks" relations that point
// at this issue, wherever those issues live.
// Linear prices a query by its page sizes multiplied together; one page of 50
// issues with 10 relations each fits one project and stays under the limit.
const projectQuery = `query($id: String!) {
  project(id: $id) {
    issues(first: 50) {
      pageInfo { hasNextPage endCursor }
      nodes {
        identifier title description
        state { type }
        inverseRelations(first: 10) {
          nodes { type issue { identifier state { type } } }
        }
      }
    }
  }
}`

type linearState struct {
	Type string `json:"type"`
}

type linearRef struct {
	Identifier string      `json:"identifier"`
	State      linearState `json:"state"`
}

type linearRelation struct {
	Type  string    `json:"type"`
	Issue linearRef `json:"issue"`
}

type linearIssue struct {
	Identifier       string      `json:"identifier"`
	Title            string      `json:"title"`
	Description      string      `json:"description"`
	State            linearState `json:"state"`
	InverseRelations struct {
		Nodes []linearRelation `json:"nodes"`
	} `json:"inverseRelations"`
}

type linearPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type linearProject struct {
	Issues struct {
		PageInfo linearPageInfo `json:"pageInfo"`
		Nodes    []linearIssue  `json:"nodes"`
	} `json:"issues"`
}

type linearResponse struct {
	Data struct {
		Project *linearProject `json:"project"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func fetchProject(client *http.Client, endpoint, key, project string) ([]linearIssue, error) {
	body, err := json.Marshal(map[string]any{"query": projectQuery, "variables": map[string]string{"id": project}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Personal API keys go bare; OAuth access tokens need a Bearer prefix.
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "lin_oauth_") {
		key = "Bearer " + key
	}
	req.Header.Set("Authorization", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out linearResponse
	parseErr := json.Unmarshal(raw, &out)
	if parseErr == nil && len(out.Errors) > 0 {
		return nil, fmt.Errorf("Linear (HTTP %d): %s", resp.StatusCode, out.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Linear returned HTTP %d", resp.StatusCode)
	}
	if parseErr != nil {
		return nil, errors.New("Linear response is not GraphQL JSON")
	}
	if out.Data.Project == nil {
		return nil, fmt.Errorf("Linear project %q not found", project)
	}
	if out.Data.Project.Issues.PageInfo.HasNextPage {
		return nil, fmt.Errorf("Linear project %q has more than 50 issues", project)
	}
	return out.Data.Project.Issues.Nodes, nil
}

// The frontier is every issue nobody has started whose blockers are all
// completed. A canceled blocker still blocks: nothing it promised exists.
func frontier(issues []linearIssue) []linearIssue {
	var ready []linearIssue
	for _, issue := range issues {
		switch issue.State.Type {
		case "triage", "backlog", "unstarted":
		default:
			continue
		}
		blocked := false
		for _, rel := range issue.InverseRelations.Nodes {
			if rel.Type == "blocks" && rel.Issue.State.Type != "completed" {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, issue)
		}
	}
	return ready
}

// One Claude per issue: its title is the Pass reason, its description the
// opening prompt. Both travel as data, never through a shell.
type sessionPane struct {
	Type    string            `json:"type"`
	Label   string            `json:"label"`
	Cwd     string            `json:"cwd"`
	Command []string          `json:"command"`
	Env     map[string]string `json:"env"`
}

func session(issue linearIssue, envFile, cwd string) sessionPane {
	return sessionPane{
		Type:  "pane",
		Label: issue.Identifier,
		Cwd:   cwd,
		Command: []string{"pass-cli", "run", "--env-file", envFile, "--",
			"claude", "--model", "sonnet", "--advisor", "opus", "--effort", "medium",
			"--", issue.Description},
		Env: map[string]string{"PROTON_PASS_AGENT_REASON": issue.Title},
	}
}

type layoutSplit struct {
	Type      string  `json:"type"`
	Direction string  `json:"direction"`
	Ratio     float64 `json:"ratio"`
	First     any     `json:"first"`
	Second    any     `json:"second"`
}

// Herdr splits in two, so n panes nest: each split gives its first pane
// 1/n of what remains, which leaves every pane the same height.
func stack(panes []sessionPane) any {
	if len(panes) == 1 {
		return panes[0]
	}
	return layoutSplit{Type: "split", Direction: "down", Ratio: 1 / float64(len(panes)),
		First: panes[0], Second: stack(panes[1:])}
}

type herdrRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type layoutParams struct {
	WorkspaceID string `json:"workspace_id"`
	TabLabel    string `json:"tab_label"`
	Focus       bool   `json:"focus"`
	Root        any    `json:"root"`
}

type herdrResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Opens one tab with a pane per session through Herdr's socket.
func applyLayout(socket string, params layoutParams) error {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(herdrRequest{ID: "xeno_dispatch", Method: "layout.apply", Params: params}); err != nil {
		return err
	}
	var resp herdrResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("Herdr response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("Herdr: %s", resp.Error.Message)
	}
	return nil
}

func dispatch(args []string) error {
	flags := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	project := flags.String("project", os.Getenv("XENO_LINEAR_PROJECT"), "Linear project ID, identifier or slug")
	envFile := flags.String("env-file", "claude.env", "env file for pass-cli run")
	dryRun := flags.Bool("dry-run", false, "list the frontier without starting Claude")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *project == "" || flags.NArg() != 0 {
		return errors.New("usage: xeno dispatch --project PROJECT [--env-file FILE] [--dry-run]")
	}
	key := os.Getenv("LINEAR_API_KEY")
	if key == "" {
		return errors.New("LINEAR_API_KEY is not set")
	}
	issues, err := fetchProject(&http.Client{Timeout: 30 * time.Second}, linearEndpoint, key, *project)
	if err != nil {
		return err
	}
	ready := frontier(issues)
	if *dryRun || len(ready) == 0 {
		for _, issue := range ready {
			fmt.Printf("%s\t%s\n", issue.Identifier, issue.Title)
		}
		return nil
	}

	socket, workspace := os.Getenv("HERDR_SOCKET_PATH"), os.Getenv("HERDR_WORKSPACE_ID")
	if socket == "" || workspace == "" {
		return errors.New("dispatch opens its sessions in Herdr; run it from a Herdr pane")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	env, err := filepath.Abs(*envFile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(env); err != nil {
		return err
	}
	panes := make([]sessionPane, len(ready))
	for i, issue := range ready {
		panes[i] = session(issue, env, cwd)
	}
	// Claude keeps each session's transcript in ~/.claude as usual.
	return applyLayout(socket, layoutParams{WorkspaceID: workspace, TabLabel: *project, Focus: true, Root: stack(panes)})
}
