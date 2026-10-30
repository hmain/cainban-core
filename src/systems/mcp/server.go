// Package mcp is cainban-core's local, single-user MCP server. It exposes the
// board/task tools over the Model Context Protocol so an AI agent (Kiro, Claude
// Desktop, Cursor, …) can drive a local kanban board.
//
// This is the CORE server: it talks to the local SQLite board under ~/.cainban
// with NO authentication, NO multi-tenant scoping, and NO cloud dependency. The
// transport defaults to stdio (the agent spawns `cainban mcp` as a subprocess);
// a loopback-only HTTP transport is also available for local testing.
//
// (The upstream multi-user cainban layered OAuth, GitHub-App authorization,
// per-request tenant scoping, a DynamoDB backend, WebSocket fan-out and an
// embeddable widget on top of this same tool surface. None of that lives here.)
package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hmain/cainban-core/src/systems/board"
	"github.com/hmain/cainban-core/src/systems/store"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName / serverVersion identify this MCP server to clients in the
// initialize handshake.
const (
	serverName    = "cainban"
	serverVersion = "core"
)

// Server is the local cainban MCP server. It is stateless with respect to the
// board: every tool call resolves and opens the current board's SQLite database
// for the life of that call, so concurrent `cainban` processes (CLI + MCP) see
// each other's writes.
type Server struct{}

// NewStateless constructs the local MCP server. The name mirrors the upstream
// constructor so the CLI entrypoint reads the same; here there is only one mode.
func NewStateless() *Server { return &Server{} }

// newMCPServer builds a configured SDK server with all tools registered.
func (s *Server) newMCPServer() *mcpsdk.Server {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)
	s.registerTools(srv)
	return srv
}

// Start runs the MCP server over stdio and blocks until the client disconnects.
// This is the default transport: an agent launches `cainban mcp` and speaks
// JSON-RPC over the subprocess's stdin/stdout.
func (s *Server) Start() error {
	srv := s.newMCPServer()
	return srv.Run(context.Background(), &mcpsdk.StdioTransport{})
}

// ServeHTTP serves the Streamable HTTP transport on addr, bound to loopback
// only. It is a convenience for local testing (e.g. curl, a local inspector);
// the primary transport is stdio. A non-loopback address is rejected so the
// unauthenticated local server can never be exposed on the network.
func (s *Server) ServeHTTP(addr string) error {
	bound, err := loopbackOnly(addr)
	if err != nil {
		return err
	}
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return s.newMCPServer()
	}, nil)
	fmt.Fprintf(os.Stderr, "cainban MCP server listening on http://%s (loopback only)\n", bound)
	return http.ListenAndServe(bound, handler) //nolint:gosec // loopback-only local dev server
}

// loopbackOnly validates that addr binds to a loopback interface and returns a
// concrete host:port to listen on. A bare ":PORT" is rewritten to
// "127.0.0.1:PORT"; an explicit non-loopback host is rejected.
func loopbackOnly(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Allow a bare ":PORT".
		if strings.HasPrefix(addr, ":") {
			return "127.0.0.1" + addr, nil
		}
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if host == "" {
		return "127.0.0.1:" + port, nil
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return net.JoinHostPort(host, port), nil
	}
	return "", fmt.Errorf("refusing to bind MCP server to non-loopback host %q; use 127.0.0.1 or localhost", host)
}

// currentBoardDBPath resolves the SQLite database path for the current local
// board (the one selected by `cainban board switch`, defaulting to "default").
func currentBoardDBPath() (string, error) {
	bs := board.New()
	name, err := bs.GetCurrentBoard()
	if err != nil {
		return "", fmt.Errorf("failed to resolve current board: %w", err)
	}
	return bs.GetBoardPath(name), nil
}

// resolveTaskSystem opens the current board's SQLite TaskStore for one request,
// returning a close func the caller MUST call.
func (s *Server) resolveTaskSystem(ctx context.Context) (store.TaskStore, func(), error) {
	dbPath, err := currentBoardDBPath()
	if err != nil {
		return nil, nil, err
	}
	ts, closer, err := store.OpenTask(ctx, dbPath)
	if err != nil {
		return nil, nil, err
	}
	return ts, func() { _ = closer() }, nil
}

// resolveBoardStore opens a BoardStore over the local board tree for one
// request, returning a close func the caller MUST call.
func (s *Server) resolveBoardStore(ctx context.Context) (store.BoardStore, func(), error) {
	dbPath, err := currentBoardDBPath()
	if err != nil {
		return nil, nil, err
	}
	// The board tree is rooted at the config dir (parent of the db path).
	_ = filepath.Dir(dbPath)
	bs, closer, err := store.OpenBoard(ctx, dbPath)
	if err != nil {
		return nil, nil, err
	}
	return bs, func() { _ = closer() }, nil
}

// registerTools registers every local tool on the SDK server.
func (s *Server) registerTools(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "create_task",
		Description: "Create a new task in the kanban board",
		Annotations: writer("Create task", false, false),
	}, s.handleCreateTask)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_tasks",
		Description: "List tasks from the kanban board",
		Annotations: readOnly("List tasks"),
	}, s.handleListTasks)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_task_status",
		Description: "Update the status of a task",
		Annotations: writer("Update task status", true, true),
	}, s.handleUpdateTaskStatus)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_task",
		Description: "Get a specific task by ID",
		Annotations: readOnly("Get task"),
	}, s.handleGetTask)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_task_priority",
		Description: "Update the priority of a task",
		Annotations: writer("Update task priority", true, true),
	}, s.handleUpdateTaskPriority)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "update_task",
		Description: "Update a task's title and description",
		Annotations: writer("Update task", true, true),
	}, s.handleUpdateTask)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_boards",
		Description: "List all available kanban boards",
		Annotations: readOnly("List boards"),
	}, s.handleListBoards)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "change_board",
		Description: "Change the active kanban board",
		Annotations: writer("Change board", true, false),
	}, s.handleChangeBoard)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_activity",
		Description: "List recent task activity (who changed what), newest first",
		Annotations: readOnly("List activity"),
	}, s.handleListActivity)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "delete_task",
		Description: "Delete a task. Soft-delete by default (recoverable with restore_task); pass hard=true to permanently remove it.",
		Annotations: writer("Delete task", true, true),
	}, s.handleDeleteTask)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "restore_task",
		Description: "Restore a soft-deleted task so it is visible again. Has no effect on a task that was never deleted or was hard-deleted.",
		Annotations: writer("Restore task", true, false),
	}, s.handleRestoreTask)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "search_tasks",
		Description: "Fuzzy-search tasks in the current board by title substring. Returns matching tasks newest-first.",
		Annotations: readOnly("Search tasks"),
	}, s.handleSearchTasks)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "link_tasks",
		Description: "Create a directional link between two tasks (blocks, blocked_by, related, depends_on).",
		Annotations: writer("Link tasks", false, false),
	}, s.handleLinkTasks)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "unlink_tasks",
		Description: "Remove a link between two tasks.",
		Annotations: writer("Unlink tasks", true, true),
	}, s.handleUnlinkTasks)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "get_task_links",
		Description: "List all links referencing a task (both directions).",
		Annotations: readOnly("Get task links"),
	}, s.handleGetTaskLinks)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "list_links",
		Description: "List every task link on the board (both directions), in board-scoped task ids. Use to render the dependency graph.",
		Annotations: readOnly("List links"),
	}, s.handleListLinks)
}

// ptrBool returns a pointer to b for the SDK's *bool annotation fields.
func ptrBool(b bool) *bool { return &b }

// readOnly builds annotations for a tool that does not modify its environment.
func readOnly(title string) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{
		Title:         title,
		ReadOnlyHint:  true,
		OpenWorldHint: ptrBool(false),
	}
}

// writer builds annotations for a tool that modifies its environment.
func writer(title string, idempotent, destructive bool) *mcpsdk.ToolAnnotations {
	return &mcpsdk.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		IdempotentHint:  idempotent,
		DestructiveHint: ptrBool(destructive),
		OpenWorldHint:   ptrBool(false),
	}
}
