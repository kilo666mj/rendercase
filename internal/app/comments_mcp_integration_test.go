package app

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.michaelspost.com/mcpkit/mcpkittest"
)

func TestCommentMCPToolsIntegration(t *testing.T) {
	f := newCommentFixture(t)
	ctx := t.Context()
	created, err := f.s.createCommentThread(ctx, f.viewer, f.artifactID, 0, json.RawMessage(`{"selector":"p:nth-of-type(1)","quote":{"exact":"Hello"}}`), "Typo here", "")
	if err != nil {
		t.Fatal(err)
	}
	call := func(session *mcp.ClientSession, name string, args map[string]any) (bool, map[string]any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.IsError {
			return false, nil
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return true, out
	}
	keys := func(m map[string]any) []string {
		var got []string
		for k := range m {
			got = append(got, k)
		}
		slices.Sort(got)
		return got
	}
	owner := mcpkittest.Connect(t, f.s.newMCPServer(f.owner))

	ok, out := call(owner, "rendercase_list_comments", map[string]any{"artifact_id": f.artifactID})
	if !ok || len(out["threads"].([]any)) != 1 {
		t.Fatalf("list = %v", out)
	}
	thread := out["threads"].([]any)[0].(map[string]any)
	if got, want := keys(thread), []string{"anchor", "can_resolve", "comments", "created_at", "created_by", "id", "version"}; !slices.Equal(got, want) {
		t.Fatalf("thread keys = %v, want %v", got, want)
	}
	if thread["anchor"].(map[string]any)["selector"] != "p:nth-of-type(1)" {
		t.Fatalf("anchor = %v", thread["anchor"])
	}

	ok, out = call(owner, "rendercase_reply_comment", map[string]any{"thread_id": created.ID, "body": "Fixed in the next version"})
	if !ok || out["body"] != "Fixed in the next version" {
		t.Fatalf("reply = %v", out)
	}
	ok, out = call(owner, "rendercase_resolve_comment_thread", map[string]any{"thread_id": created.ID, "resolved": true})
	if !ok || out["resolved_at"] == nil {
		t.Fatalf("resolve = %v", out)
	}
	if ok, out = call(owner, "rendercase_list_comments", map[string]any{"artifact_id": f.artifactID}); !ok || len(out["threads"].([]any)) != 0 {
		t.Fatalf("open threads after resolve = %v", out)
	}
	ok, out = call(owner, "rendercase_list_comments", map[string]any{"artifact_id": f.artifactID, "include_resolved": true})
	if !ok || len(out["threads"].([]any)) != 1 || len(out["threads"].([]any)[0].(map[string]any)["comments"].([]any)) != 2 {
		t.Fatalf("resolved threads = %v", out)
	}

	stranger := mcpkittest.Connect(t, f.s.newMCPServer(f.stranger))
	if ok, _ := call(stranger, "rendercase_list_comments", map[string]any{"artifact_id": f.artifactID}); ok {
		t.Fatal("stranger listed comments")
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO artifact_grants(artifact_id,user_id,role) VALUES($1,$2,'viewer')`, f.artifactID, f.stranger.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := call(stranger, "rendercase_resolve_comment_thread", map[string]any{"thread_id": created.ID, "resolved": false}); ok {
		t.Fatal("non-author viewer reopened a thread")
	}
	if ok, _ := call(stranger, "rendercase_reply_comment", map[string]any{"thread_id": created.ID, "body": " "}); ok {
		t.Fatal("blank reply accepted")
	}

	var mcpReplies int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='comment.create' AND details->>'interface'='mcp'`).Scan(&mcpReplies); err != nil {
		t.Fatal(err)
	}
	if mcpReplies != 1 {
		t.Fatalf("MCP reply audit events = %d", mcpReplies)
	}
}
