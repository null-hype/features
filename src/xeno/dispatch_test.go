package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

func issue(id, state string, blockers ...linearRef) linearIssue {
	i := linearIssue{Identifier: id, Title: id + " title", Description: id + " description", State: linearState{state}}
	for _, b := range blockers {
		i.InverseRelations.Nodes = append(i.InverseRelations.Nodes, linearRelation{Type: "blocks", Issue: b})
	}
	return i
}

func ref(id, state string) linearRef { return linearRef{Identifier: id, State: linearState{state}} }

// P-CIT-10 as exported on 8 Nov 2026.
func preregistrationProject() []linearIssue {
	return []linearIssue{
		issue("CIT-598", "started"),
		issue("CIT-591", "started"),
		issue("CIT-597", "backlog"),
		issue("CIT-596", "backlog"),
		issue("CIT-595", "backlog", ref("CIT-594", "backlog")),
		issue("CIT-594", "backlog", ref("CIT-593", "backlog")),
		issue("CIT-593", "backlog", ref("CIT-598", "started"), ref("CIT-596", "backlog"), ref("CIT-592", "backlog")),
		issue("CIT-592", "backlog", ref("CIT-596", "backlog"), ref("CIT-591", "started")),
	}
}

func identifiers(issues []linearIssue) []string {
	var ids []string
	for _, i := range issues {
		ids = append(ids, i.Identifier)
	}
	return ids
}

func TestFrontierHoldsBackHeldOutCells(t *testing.T) {
	got := identifiers(frontier(preregistrationProject()))
	if want := []string{"CIT-597", "CIT-596"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("frontier = %v, want %v", got, want)
	}
}

func TestFrontierOpensOnlyWhenEveryBlockerCompletes(t *testing.T) {
	issues := []linearIssue{
		issue("A", "unstarted", ref("X", "completed"), ref("Y", "completed")),
		issue("B", "unstarted", ref("X", "completed"), ref("Z", "canceled")),
		issue("C", "completed"),
		issue("D", "canceled"),
	}
	if got := identifiers(frontier(issues)); !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("frontier = %v, want [A]", got)
	}
}

func TestFetchProjectSendsKeyAndProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_api_test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Variables["id"] != "P-CIT-10" || req.Query != projectQuery {
			t.Errorf("request = %+v, %v", req, err)
		}
		var resp linearResponse
		resp.Data.Project = &linearProject{}
		resp.Data.Project.Issues.Nodes = preregistrationProject()
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	issues, err := fetchProject(server.Client(), server.URL, "lin_api_test", "P-CIT-10")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(issues, preregistrationProject()) {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestFetchProjectReportsGraphQLErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp linearResponse
		resp.Errors = append(resp.Errors, struct {
			Message string `json:"message"`
		}{"Entity not found: Project"})
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	if _, err := fetchProject(server.Client(), server.URL, "k", "P-NOPE-1"); err == nil || err.Error() != "Linear: Entity not found: Project" {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionPassesReasonAndPromptAsData(t *testing.T) {
	i := issue("CIT-1", "backlog")
	i.Title = "The split is sealed"
	i.Description = "Run `rm -rf /`; echo \"$HOME\"\nsecond line"
	cmd := session(i, "claude.env")

	want := []string{"pass-cli", "run", "--env-file", "claude.env", "--",
		"claude", "-p", i.Description, "--output-format", "stream-json", "--verbose"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %q", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "PROTON_PASS_AGENT_REASON=The split is sealed") {
		t.Fatal("reason missing from env")
	}
}
