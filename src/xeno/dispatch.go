package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const linearEndpoint = "https://api.linear.app/graphql"

// Blockers are the issues on the other end of "blocks" relations that point
// at this issue, wherever those issues live.
// Linear prices a query by its page sizes multiplied together, so issues come
// in pages small enough to stay under its complexity limit.
const projectQuery = `query($id: String!, $after: String) {
  project(id: $id) {
    issues(first: 50, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        identifier title description
        state { type }
        inverseRelations(first: 20) {
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
	var issues []linearIssue
	after := ""
	for {
		page, err := fetchPage(client, endpoint, key, project, after)
		if err != nil {
			return nil, err
		}
		issues = append(issues, page.Issues.Nodes...)
		if !page.Issues.PageInfo.HasNextPage {
			return issues, nil
		}
		after = page.Issues.PageInfo.EndCursor
	}
}

func fetchPage(client *http.Client, endpoint, key, project, after string) (*linearProject, error) {
	variables := map[string]any{"id": project}
	if after != "" {
		variables["after"] = after
	}
	body, err := json.Marshal(map[string]any{"query": projectQuery, "variables": variables})
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
	return out.Data.Project, nil
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
// prompt. Both travel as data, never through a shell.
func session(issue linearIssue, envFile string) *exec.Cmd {
	cmd := exec.Command("pass-cli", "run", "--env-file", envFile, "--",
		"claude", "--model", "sonnet", "--advisor", "opus", "--effort", "medium",
		"-p", issue.Description, "--output-format", "stream-json", "--verbose")
	cmd.Env = append(os.Environ(), "PROTON_PASS_AGENT_REASON="+issue.Title)
	return cmd
}

func dispatch(args []string) error {
	flags := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	project := flags.String("project", os.Getenv("XENO_LINEAR_PROJECT"), "Linear project ID, identifier or slug")
	envFile := flags.String("env-file", "claude.env", "env file for pass-cli run")
	output := flags.String("output", ".xeno/dispatch", "directory for each issue's Claude stream")
	dryRun := flags.Bool("dry-run", false, "list the frontier without starting Claude")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *project == "" || flags.NArg() != 0 {
		return errors.New("usage: xeno dispatch --project PROJECT [--env-file FILE] [--output DIR] [--dry-run]")
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
	if *dryRun {
		for _, issue := range ready {
			fmt.Printf("%s\t%s\n", issue.Identifier, issue.Title)
		}
		return nil
	}
	if err := os.MkdirAll(*output, 0700); err != nil {
		return err
	}

	var wg sync.WaitGroup
	errs := make([]error, len(ready))
	for i, issue := range ready {
		trace := filepath.Join(*output, issue.Identifier+".jsonl")
		stdout, err := os.Create(trace)
		if err != nil {
			return err
		}
		stderr, err := os.Create(filepath.Join(*output, issue.Identifier+".log"))
		if err != nil {
			stdout.Close()
			return err
		}
		cmd := session(issue, *envFile)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			stdout.Close()
			stderr.Close()
			errs[i] = fmt.Errorf("%s: %w", issue.Identifier, err)
			continue
		}
		fmt.Printf("%s\t%s\n", issue.Identifier, trace)
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			defer stdout.Close()
			defer stderr.Close()
			if err := cmd.Wait(); err != nil {
				errs[i] = fmt.Errorf("%s: %w", id, err)
			}
		}(i, issue.Identifier)
	}
	wg.Wait()
	return errors.Join(errs...)
}
