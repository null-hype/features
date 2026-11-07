package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
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

	if _, err := fetchProject(server.Client(), server.URL, "k", "P-NOPE-1"); err == nil || err.Error() != "Linear (HTTP 200): Entity not found: Project" {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionPassesReasonAndPromptAsData(t *testing.T) {
	i := issue("CIT-1", "backlog")
	i.Title = "The split is sealed"
	i.Description = "--help `rm -rf /`; echo \"$HOME\"\nsecond line"
	pane := session(i, "/repo/claude.env", "/repo")

	want := []string{"pass-cli", "run", "--env-file", "/repo/claude.env", "--",
		"claude", "--model", "sonnet", "--advisor", "opus", "--effort", "medium",
		"--", i.Description}
	if !reflect.DeepEqual(pane.Command, want) {
		t.Fatalf("command = %q", pane.Command)
	}
	if pane.Env["PROTON_PASS_AGENT_REASON"] != "The split is sealed" || pane.Label != "CIT-1" || pane.Cwd != "/repo" {
		t.Fatalf("pane = %+v", pane)
	}
}

func TestStackGivesEveryPaneAnEqualShare(t *testing.T) {
	var panes []sessionPane
	for _, id := range []string{"A", "B", "C"} {
		panes = append(panes, session(issue(id, "backlog"), "e", "/"))
	}
	root := stack(panes).(layoutSplit)
	inner := root.Second.(layoutSplit)
	if root.Ratio != 1.0/3 || inner.Ratio != 0.5 || root.First.(sessionPane).Label != "A" ||
		inner.First.(sessionPane).Label != "B" || inner.Second.(sessionPane).Label != "C" {
		t.Fatalf("stack = %+v", root)
	}
	if stack(panes[:1]).(sessionPane).Label != "A" {
		t.Fatal("a single pane is not wrapped in a split")
	}
}

func TestApplyLayoutSendsOneRequest(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "herdr.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan herdrRequest, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req herdrRequest
		json.NewDecoder(conn).Decode(&req)
		got <- req
		var resp herdrResponse
		resp.Error = &struct {
			Message string `json:"message"`
		}{"no such workspace"}
		json.NewEncoder(conn).Encode(resp)
	}()

	err = applyLayout(socket, layoutParams{WorkspaceID: "w9", TabLabel: "P-CIT-10", Root: session(issue("A", "backlog"), "e", "/")})
	if err == nil || err.Error() != "Herdr: no such workspace" {
		t.Fatalf("err = %v", err)
	}
	if req := <-got; req.Method != "layout.apply" {
		t.Fatalf("method = %q", req.Method)
	}
}

func TestFetchProjectAuthorization(t *testing.T) {
	for key, want := range map[string]string{
		"lin_api_abc\n": "lin_api_abc",
		"lin_oauth_abc": "Bearer lin_oauth_abc",
	} {
		var got string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusUnauthorized)
			var resp linearResponse
			resp.Errors = append(resp.Errors, struct {
				Message string `json:"message"`
			}{"Authentication required, not authenticated"})
			json.NewEncoder(w).Encode(resp)
		}))
		_, err := fetchProject(server.Client(), server.URL, key, "P-CIT-10")
		server.Close()
		if got != want {
			t.Errorf("key %q sent as %q, want %q", key, got, want)
		}
		if err == nil || err.Error() != "Linear (HTTP 401): Authentication required, not authenticated" {
			t.Errorf("err = %v", err)
		}
	}
}

func TestFetchProjectRefusesAPartialProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp linearResponse
		resp.Data.Project = &linearProject{}
		resp.Data.Project.Issues.Nodes = preregistrationProject()
		resp.Data.Project.Issues.PageInfo.HasNextPage = true
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	if _, err := fetchProject(server.Client(), server.URL, "k", "P-CIT-10"); err == nil {
		t.Fatal("a partial project was accepted")
	}
}
