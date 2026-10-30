package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// expectedTools is the exact local tool surface cainban-core exposes. If a tool
// is added or removed, update this list deliberately — the test guards against
// an accidental drift (e.g. a multi-user tool like change_repo / whoami /
// create_widget_token leaking back in).
var expectedTools = []string{
	"change_board",
	"create_task",
	"delete_task",
	"get_task",
	"get_task_links",
	"link_tasks",
	"list_activity",
	"list_boards",
	"list_links",
	"list_tasks",
	"restore_task",
	"search_tasks",
	"unlink_tasks",
	"update_task",
	"update_task_priority",
	"update_task_status",
}

// newConnectedClient wires an in-memory client <-> server pair for the core MCP
// server and returns a connected client session plus a cleanup func.
func newConnectedClient(t *testing.T) (*mcpsdk.ClientSession, func()) {
	t.Helper()
	srv := NewStateless().newMCPServer()
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()

	ctx := context.Background()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}
}

func TestToolSurfaceIsLocalCoreOnly(t *testing.T) {
	cs, cleanup := newConnectedClient(t)
	defer cleanup()

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)

	if len(got) != len(expectedTools) {
		t.Fatalf("tool count = %d, want %d\n got:  %v\n want: %v", len(got), len(expectedTools), got, expectedTools)
	}
	for i := range expectedTools {
		if got[i] != expectedTools[i] {
			t.Fatalf("tool[%d] = %q, want %q\n got:  %v\n want: %v", i, got[i], expectedTools[i], got, expectedTools)
		}
	}

	// Guard: no tool carries a `repo` argument (that was the multi-user seam).
	for _, tool := range res.Tools {
		if tool.InputSchema == nil {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema for %q: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal input schema for %q: %v", tool.Name, err)
		}
		if _, ok := schema.Properties["repo"]; ok {
			t.Errorf("tool %q exposes a multi-user `repo` argument; core is single-user/local", tool.Name)
		}
	}
}

func TestCreateListUpdateRoundtrip(t *testing.T) {
	// Point HOME at a temp dir so the SQLite board lands under a throwaway
	// ~/.cainban tree, isolated from the developer's real board.
	t.Setenv("HOME", t.TempDir())

	cs, cleanup := newConnectedClient(t)
	defer cleanup()
	ctx := context.Background()

	// create_task
	createRes, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{"title": "roundtrip task", "priority": "high"},
	})
	if err != nil {
		t.Fatalf("create_task: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create_task returned error result: %+v", createRes.Content)
	}

	// list_tasks should show it
	listRes, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("list_tasks: %v", err)
	}
	if !containsText(listRes, "roundtrip task") {
		t.Fatalf("list_tasks did not contain the created task: %+v", listRes.Content)
	}

	// update_task_status -> doing
	upRes, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "update_task_status",
		Arguments: map[string]any{"id": 1, "status": "doing"},
	})
	if err != nil {
		t.Fatalf("update_task_status: %v", err)
	}
	if upRes.IsError {
		t.Fatalf("update_task_status returned error result: %+v", upRes.Content)
	}

	// Unknown task id is an EXPECTED tool error, not a transport error.
	missRes, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "get_task",
		Arguments: map[string]any{"id": 999},
	})
	if err != nil {
		t.Fatalf("get_task transport error: %v", err)
	}
	if !missRes.IsError {
		t.Fatalf("get_task on a missing id should be an isError tool result")
	}
}

// containsText reports whether any TextContent block in the result contains sub.
func containsText(res *mcpsdk.CallToolResult, sub string) bool {
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			if contains(tc.Text, sub) {
				return true
			}
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
