package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hmain/cainban-core/src/systems/store"
	"github.com/hmain/cainban-core/src/systems/task"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// scopeLine is the one-line "where am I?" header prefixed to list-style tool
// output. cainban-core is single-user and local, so the board is always the
// current local board, id 1.
func scopeLine() string {
	return "Scope — board: default (id 1)"
}

// toolError returns an MCP tool RESULT flagged isError for an EXPECTED,
// model-recoverable domain outcome (a task that does not exist, an invalid
// status/priority). The model sees the message and can recover rather than the
// call surfacing as a protocol fault. Reserve a transport error (nil, nil, err)
// for true faults the model cannot fix by retrying with different args.
func toolError(format string, args ...any) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

// versionConflictResult formats the agent-facing tool error returned when an
// expected_version guard fails: re-read and retry.
func versionConflictResult(id int) *mcpsdk.CallToolResult {
	return toolError("task #%d changed since you read it (version conflict); re-read with get_task and retry", id)
}

// recordActivity appends a best-effort append-only audit event. A record error
// never fails the tool call.
func recordActivity(ts store.TaskStore, ev task.ActivityEvent) {
	_ = ts.RecordActivity(ev)
}

// Tool handler argument types.

type CreateTaskArgs struct {
	Title       string      `json:"title" jsonschema:"the title of the task"`
	Description string      `json:"description,omitempty" jsonschema:"the description of the task"`
	BoardID     int         `json:"board_id,omitempty" jsonschema:"the board ID (defaults to 1)"`
	Priority    interface{} `json:"priority,omitempty" jsonschema:"priority level (none, low, medium, high, critical or 0-4)"`
}

type ListTasksArgs struct {
	BoardID int    `json:"board_id,omitempty" jsonschema:"the board ID (defaults to 1)"`
	Status  string `json:"status,omitempty" jsonschema:"filter by status (todo, doing, done)"`
}

type UpdateTaskStatusArgs struct {
	ID              int    `json:"id" jsonschema:"the task ID"`
	Status          string `json:"status" jsonschema:"the new status (todo, doing, done)"`
	ExpectedVersion int    `json:"expected_version,omitempty" jsonschema:"optimistic-concurrency guard: the task version you last read; the write fails with a version-conflict error if the task changed since. Omit (or 0) to force-write."`
}

type GetTaskArgs struct {
	ID int `json:"id" jsonschema:"the task ID"`
}

type UpdateTaskPriorityArgs struct {
	ID              int         `json:"id" jsonschema:"task ID to update"`
	Priority        interface{} `json:"priority" jsonschema:"priority level (none, low, medium, high, critical or 0-4)"`
	ExpectedVersion int         `json:"expected_version,omitempty" jsonschema:"optimistic-concurrency guard: the task version you last read; the write fails with a version-conflict error if the task changed since. Omit (or 0) to force-write."`
}

type UpdateTaskArgs struct {
	ID              int    `json:"id" jsonschema:"the task ID"`
	Title           string `json:"title" jsonschema:"the new title"`
	Description     string `json:"description,omitempty" jsonschema:"the new description"`
	ExpectedVersion int    `json:"expected_version,omitempty" jsonschema:"optimistic-concurrency guard: the task version you last read; the write fails with a version-conflict error if the task changed since. Omit (or 0) to force-write."`
}

type ListBoardsArgs struct{}

type ChangeBoardArgs struct {
	BoardName string `json:"board_name" jsonschema:"the name of the board to switch to"`
}

type ListActivityArgs struct {
	TaskID int    `json:"task_id,omitempty" jsonschema:"optional: only show activity for this board task ID; omit for the whole board"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max events to return, newest first (default 50, max 200)"`
	Since  string `json:"since,omitempty" jsonschema:"optional RFC3339 timestamp; return only events strictly newer than this (for delta polling a live board)"`
}

type DeleteTaskArgs struct {
	ID   int  `json:"id" jsonschema:"the board task ID to delete"`
	Hard bool `json:"hard,omitempty" jsonschema:"permanently delete instead of soft-delete; a hard delete cannot be restored (default false)"`
}

type RestoreTaskArgs struct {
	ID int `json:"id" jsonschema:"the board task ID to restore"`
}

type SearchTasksArgs struct {
	Query   string `json:"query" jsonschema:"title substring to fuzzy-match against tasks in the board"`
	BoardID int    `json:"board_id,omitempty" jsonschema:"the board ID (defaults to 1)"`
}

type LinkTasksArgs struct {
	FromID   int    `json:"from_id" jsonschema:"board task ID of the source task"`
	ToID     int    `json:"to_id" jsonschema:"board task ID of the target task"`
	LinkType string `json:"type" jsonschema:"link type: blocks, blocked_by, related, depends_on"`
}

type UnlinkTasksArgs struct {
	FromID   int    `json:"from_id" jsonschema:"board task ID of the source task"`
	ToID     int    `json:"to_id" jsonschema:"board task ID of the target task"`
	LinkType string `json:"type" jsonschema:"link type: blocks, blocked_by, related, depends_on"`
}

type GetTaskLinksArgs struct {
	ID int `json:"id" jsonschema:"the board task ID to list links for"`
}

type ListLinksArgs struct {
	BoardID int `json:"board_id,omitempty" jsonschema:"the board ID (defaults to 1)"`
}

// validLinkTypes lists the recognized link types for error messages.
const validLinkTypes = "blocks, blocked_by, related, depends_on"

// Tool handlers. Each resolves the current board's SQLite store per request.

func (s *Server) handleCreateTask(ctx context.Context, _ *mcpsdk.CallToolRequest, args CreateTaskArgs) (*mcpsdk.CallToolResult, *task.Task, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := args.BoardID
	if boardID == 0 {
		boardID = 1
	}

	var created *task.Task
	if args.Priority != nil && task.IsValidPriority(args.Priority) {
		created, err = ts.CreateWithPriority(boardID, args.Title, args.Description, args.Priority)
	} else {
		created, err = ts.Create(boardID, args.Title, args.Description)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create task: %w", err)
	}

	recordActivity(ts, task.ActivityEvent{
		BoardID:     boardID,
		BoardTaskID: created.BoardTaskID,
		Action:      task.ActivityCreated,
		Detail:      created.Title,
	})

	priorityStr := ""
	if created.Priority > 0 {
		priorityStr = fmt.Sprintf(" [%s]", task.GetPriorityName(created.Priority))
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{
			Text: fmt.Sprintf("Created task #%d%s: %s", created.BoardTaskID, priorityStr, created.Title),
		}},
	}, created, nil
}

func (s *Server) handleListTasks(ctx context.Context, _ *mcpsdk.CallToolRequest, args ListTasksArgs) (*mcpsdk.CallToolResult, []*task.Task, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := args.BoardID
	if boardID == 0 {
		boardID = 1
	}

	var tasks []*task.Task
	if args.Status != "" {
		if !task.IsValidStatus(args.Status) {
			return toolError("invalid status %q; valid values are todo, doing, done", args.Status), nil, nil
		}
		tasks, err = ts.ListByStatus(boardID, task.Status(args.Status))
	} else {
		tasks, err = ts.List(boardID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list tasks: %w", err)
	}

	if len(tasks) == 0 {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: scopeLine()},
				&mcpsdk.TextContent{Text: "No tasks found in current board"},
			},
		}, tasks, nil
	}

	tasksByStatus := make(map[task.Status][]*task.Task)
	for _, t := range tasks {
		tasksByStatus[t.Status] = append(tasksByStatus[t.Status], t)
	}

	content := []mcpsdk.Content{&mcpsdk.TextContent{Text: scopeLine()}}
	for _, status := range []task.Status{task.StatusTodo, task.StatusDoing, task.StatusDone} {
		statusTasks, exists := tasksByStatus[status]
		if !exists || len(statusTasks) == 0 {
			continue
		}
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("\n%s:", string(status))})
		for _, t := range statusTasks {
			priorityStr := ""
			if t.Priority > 0 {
				priorityStr = fmt.Sprintf(" [%s]", task.GetPriorityName(t.Priority))
			}
			content = append(content, &mcpsdk.TextContent{
				Text: fmt.Sprintf("• #%d%s %s", t.BoardTaskID, priorityStr, t.Title),
			})
		}
	}

	return &mcpsdk.CallToolResult{Content: content}, tasks, nil
}

func (s *Server) handleUpdateTaskStatus(ctx context.Context, _ *mcpsdk.CallToolRequest, args UpdateTaskStatusArgs) (*mcpsdk.CallToolResult, any, error) {
	if !task.IsValidStatus(args.Status) {
		return toolError("invalid status %q; valid values are todo, doing, done", args.Status), nil, nil
	}

	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := 1
	t, err := ts.GetByBoardTaskID(boardID, args.ID)
	if err != nil {
		return toolError("task #%d not found; call list_tasks to see valid task ids", args.ID), nil, nil
	}

	if args.ExpectedVersion > 0 {
		if err := ts.UpdateStatusIfVersion(t.ID, task.Status(args.Status), args.ExpectedVersion); err != nil {
			if errors.Is(err, store.ErrVersionConflict) {
				return versionConflictResult(args.ID), nil, nil
			}
			return nil, nil, fmt.Errorf("failed to update task status: %w", err)
		}
	} else if err := ts.UpdateStatus(t.ID, task.Status(args.Status)); err != nil {
		return nil, nil, fmt.Errorf("failed to update task status: %w", err)
	}

	recordActivity(ts, task.ActivityEvent{
		BoardID:     boardID,
		BoardTaskID: args.ID,
		Action:      task.ActivityStatusChanged,
		Detail:      fmt.Sprintf("%s -> %s", t.Status, args.Status),
	})

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Updated task #%d status to %s", args.ID, args.Status)}},
	}, nil, nil
}

func (s *Server) handleGetTask(ctx context.Context, _ *mcpsdk.CallToolRequest, args GetTaskArgs) (*mcpsdk.CallToolResult, *task.Task, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	t, err := ts.GetByBoardTaskID(1, args.ID)
	if err != nil {
		return toolError("task #%d not found; call list_tasks to see valid task ids", args.ID), nil, nil
	}

	text := fmt.Sprintf("#%d [%s] %s\n%s", t.BoardTaskID, t.Status, t.Title, t.Description)
	if links, lerr := ts.GetTaskLinks(t.ID); lerr == nil && len(links) > 0 {
		text += "\nLinks:"
		for _, l := range links {
			text += fmt.Sprintf("\n  • #%d %s #%d", l.FromTaskID, l.LinkType, l.ToTaskID)
		}
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
	}, t, nil
}

func (s *Server) handleUpdateTaskPriority(ctx context.Context, _ *mcpsdk.CallToolRequest, args UpdateTaskPriorityArgs) (*mcpsdk.CallToolResult, any, error) {
	if !task.IsValidPriority(args.Priority) {
		return toolError("invalid priority; valid values are none, low, medium, high, critical (or 0-4)"), nil, nil
	}

	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := 1
	t, err := ts.GetByBoardTaskID(boardID, args.ID)
	if err != nil {
		return toolError("task #%d not found; call list_tasks to see valid task ids", args.ID), nil, nil
	}

	if args.ExpectedVersion > 0 {
		if err := ts.UpdatePriorityIfVersion(t.ID, args.Priority, args.ExpectedVersion); err != nil {
			if errors.Is(err, store.ErrVersionConflict) {
				return versionConflictResult(args.ID), nil, nil
			}
			return nil, nil, fmt.Errorf("failed to update task priority: %w", err)
		}
	} else if err := ts.UpdatePriority(t.ID, args.Priority); err != nil {
		return nil, nil, fmt.Errorf("failed to update task priority: %w", err)
	}

	priorityLevel, _ := task.ParsePriority(args.Priority)
	priorityName := task.GetPriorityName(priorityLevel)

	recordActivity(ts, task.ActivityEvent{
		BoardID:     boardID,
		BoardTaskID: args.ID,
		Action:      task.ActivityPriorityChanged,
		Detail:      fmt.Sprintf("%s -> %s", task.GetPriorityName(t.Priority), priorityName),
	})

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Task #%d priority updated to %s (%d)", args.ID, priorityName, priorityLevel)}},
	}, nil, nil
}

func (s *Server) handleUpdateTask(ctx context.Context, _ *mcpsdk.CallToolRequest, args UpdateTaskArgs) (*mcpsdk.CallToolResult, any, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := 1
	t, err := ts.GetByBoardTaskID(boardID, args.ID)
	if err != nil {
		return toolError("task #%d not found; call list_tasks to see valid task ids", args.ID), nil, nil
	}

	if args.ExpectedVersion > 0 {
		if err := ts.UpdateIfVersion(t.ID, args.Title, args.Description, args.ExpectedVersion); err != nil {
			if errors.Is(err, store.ErrVersionConflict) {
				return versionConflictResult(args.ID), nil, nil
			}
			return nil, nil, fmt.Errorf("failed to update task: %w", err)
		}
	} else if err := ts.Update(t.ID, args.Title, args.Description); err != nil {
		return nil, nil, fmt.Errorf("failed to update task: %w", err)
	}

	recordActivity(ts, task.ActivityEvent{
		BoardID:     boardID,
		BoardTaskID: args.ID,
		Action:      task.ActivityUpdated,
		Detail:      args.Title,
	})

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Updated task #%d: %s", args.ID, args.Title)}},
	}, nil, nil
}

func (s *Server) handleListBoards(ctx context.Context, _ *mcpsdk.CallToolRequest, _ ListBoardsArgs) (*mcpsdk.CallToolResult, []task.BoardSummary, error) {
	bs, closeFn, err := s.resolveBoardStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boards, err := bs.ListBoards()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list boards: %w", err)
	}

	if len(boards) == 0 {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{
				&mcpsdk.TextContent{Text: scopeLine()},
				&mcpsdk.TextContent{Text: "No boards found"},
			},
		}, boards, nil
	}

	content := []mcpsdk.Content{
		&mcpsdk.TextContent{Text: scopeLine()},
		&mcpsdk.TextContent{Text: "Available boards:"},
	}
	for _, b := range boards {
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("• %s", b.Name)})
	}

	return &mcpsdk.CallToolResult{Content: content}, boards, nil
}

func (s *Server) handleChangeBoard(ctx context.Context, _ *mcpsdk.CallToolRequest, args ChangeBoardArgs) (*mcpsdk.CallToolResult, *task.BoardSummary, error) {
	bs, closeFn, err := s.resolveBoardStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	b, err := bs.ResolveBoard(args.BoardName)
	if err != nil {
		if errors.Is(err, store.ErrBoardNotFound) {
			return toolError("board %q not found; call list_boards to see valid boards", args.BoardName), nil, nil
		}
		return nil, nil, fmt.Errorf("failed to resolve board %q: %w", args.BoardName, err)
	}

	if sbs, ok := bs.(interface{ SetCurrent(string) error }); ok {
		_ = sbs.SetCurrent(b.Name)
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Switched to board %q (id %d)", b.Name, b.ID)}},
	}, &b, nil
}

func (s *Server) handleListActivity(ctx context.Context, _ *mcpsdk.CallToolRequest, args ListActivityArgs) (*mcpsdk.CallToolResult, []task.ActivityEvent, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	var since time.Time
	if args.Since != "" {
		parsed, perr := time.Parse(time.RFC3339, args.Since)
		if perr != nil {
			return toolError("invalid since %q; pass an RFC3339 timestamp like 2026-01-02T15:04:05Z", args.Since), nil, nil
		}
		since = parsed
	}

	events, err := ts.ListActivity(1, args.TaskID, args.Limit, since)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list activity: %w", err)
	}

	if len(events) == 0 {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "No activity recorded"}},
		}, events, nil
	}

	content := make([]mcpsdk.Content, 0, len(events))
	for _, ev := range events {
		content = append(content, &mcpsdk.TextContent{
			Text: fmt.Sprintf("%s  #%d %s  %s", ev.Timestamp.Format(time.RFC3339), ev.BoardTaskID, ev.Action, ev.Detail),
		})
	}

	return &mcpsdk.CallToolResult{Content: content}, events, nil
}

func (s *Server) handleDeleteTask(ctx context.Context, _ *mcpsdk.CallToolRequest, args DeleteTaskArgs) (*mcpsdk.CallToolResult, any, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := 1
	t, err := ts.GetByBoardTaskID(boardID, args.ID)
	if err != nil {
		return toolError("task #%d not found; call list_tasks to see valid task ids", args.ID), nil, nil
	}

	if args.Hard {
		if err := ts.HardDelete(t.ID); err != nil {
			return nil, nil, fmt.Errorf("failed to hard-delete task: %w", err)
		}
	} else if err := ts.SoftDelete(t.ID); err != nil {
		return nil, nil, fmt.Errorf("failed to delete task: %w", err)
	}

	kind, detail := "deleted", "soft delete"
	if args.Hard {
		kind, detail = "permanently deleted", "hard delete"
	}

	recordActivity(ts, task.ActivityEvent{
		BoardID:     boardID,
		BoardTaskID: args.ID,
		Action:      task.ActivityDeleted,
		Detail:      detail,
	})

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Task #%d %s (%q)", args.ID, kind, t.Title)}},
	}, nil, nil
}

func (s *Server) handleRestoreTask(ctx context.Context, _ *mcpsdk.CallToolRequest, args RestoreTaskArgs) (*mcpsdk.CallToolResult, any, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	if err := ts.RestoreTask(args.ID); err != nil {
		return toolError("task #%d could not be restored; it may have been hard-deleted or never existed", args.ID), nil, nil
	}

	recordActivity(ts, task.ActivityEvent{
		BoardID:     1,
		BoardTaskID: args.ID,
		Action:      task.ActivityRestored,
		Detail:      "restored",
	})

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Task #%d restored", args.ID)}},
	}, nil, nil
}

func (s *Server) handleSearchTasks(ctx context.Context, _ *mcpsdk.CallToolRequest, args SearchTasksArgs) (*mcpsdk.CallToolResult, []*task.Task, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := args.BoardID
	if boardID == 0 {
		boardID = 1
	}

	results, err := ts.SearchTasks(boardID, args.Query)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to search tasks: %w", err)
	}

	content := []mcpsdk.Content{&mcpsdk.TextContent{Text: scopeLine()}}
	if len(results) == 0 {
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("No tasks matching %q", args.Query)})
		return &mcpsdk.CallToolResult{Content: content}, results, nil
	}
	for _, t := range results {
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("• #%d [%s] %s", t.BoardTaskID, t.Status, t.Title)})
	}

	return &mcpsdk.CallToolResult{Content: content}, results, nil
}

func (s *Server) handleLinkTasks(ctx context.Context, _ *mcpsdk.CallToolRequest, args LinkTasksArgs) (*mcpsdk.CallToolResult, any, error) {
	if !task.IsValidLinkType(args.LinkType) {
		return toolError("invalid link type %q; valid values are %s", args.LinkType, validLinkTypes), nil, nil
	}

	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := 1
	from, err := ts.GetByBoardTaskID(boardID, args.FromID)
	if err != nil {
		return toolError("from task #%d not found; call list_tasks to see valid task ids", args.FromID), nil, nil
	}
	to, err := ts.GetByBoardTaskID(boardID, args.ToID)
	if err != nil {
		return toolError("to task #%d not found; call list_tasks to see valid task ids", args.ToID), nil, nil
	}

	if err := ts.LinkTasks(from.ID, to.ID, task.LinkType(args.LinkType)); err != nil {
		return toolError("failed to link: %s", err), nil, nil
	}

	for _, id := range []int{args.FromID, args.ToID} {
		recordActivity(ts, task.ActivityEvent{
			BoardID:     boardID,
			BoardTaskID: id,
			Action:      task.ActivityLinked,
			Detail:      fmt.Sprintf("#%d %s #%d", args.FromID, args.LinkType, args.ToID),
		})
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Linked: #%d %s #%d", args.FromID, args.LinkType, args.ToID)}},
	}, nil, nil
}

func (s *Server) handleUnlinkTasks(ctx context.Context, _ *mcpsdk.CallToolRequest, args UnlinkTasksArgs) (*mcpsdk.CallToolResult, any, error) {
	if !task.IsValidLinkType(args.LinkType) {
		return toolError("invalid link type %q; valid values are %s", args.LinkType, validLinkTypes), nil, nil
	}

	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := 1
	from, err := ts.GetByBoardTaskID(boardID, args.FromID)
	if err != nil {
		return toolError("from task #%d not found; call list_tasks to see valid task ids", args.FromID), nil, nil
	}
	to, err := ts.GetByBoardTaskID(boardID, args.ToID)
	if err != nil {
		return toolError("to task #%d not found; call list_tasks to see valid task ids", args.ToID), nil, nil
	}

	if err := ts.UnlinkTasks(from.ID, to.ID, task.LinkType(args.LinkType)); err != nil {
		return toolError("no %s link found between #%d and #%d", args.LinkType, args.FromID, args.ToID), nil, nil
	}

	for _, id := range []int{args.FromID, args.ToID} {
		recordActivity(ts, task.ActivityEvent{
			BoardID:     boardID,
			BoardTaskID: id,
			Action:      task.ActivityUnlinked,
			Detail:      fmt.Sprintf("#%d %s #%d", args.FromID, args.LinkType, args.ToID),
		})
	}

	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("Unlinked: #%d %s #%d", args.FromID, args.LinkType, args.ToID)}},
	}, nil, nil
}

func (s *Server) handleGetTaskLinks(ctx context.Context, _ *mcpsdk.CallToolRequest, args GetTaskLinksArgs) (*mcpsdk.CallToolResult, []task.TaskLink, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	t, err := ts.GetByBoardTaskID(1, args.ID)
	if err != nil {
		return toolError("task #%d not found; call list_tasks to see valid task ids", args.ID), nil, nil
	}

	links, err := ts.GetTaskLinks(t.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get task links: %w", err)
	}

	content := []mcpsdk.Content{&mcpsdk.TextContent{Text: scopeLine()}}
	if len(links) == 0 {
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("Task #%d has no links", args.ID)})
		return &mcpsdk.CallToolResult{Content: content}, links, nil
	}
	for _, l := range links {
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("• #%d %s #%d", l.FromTaskID, l.LinkType, l.ToTaskID)})
	}

	return &mcpsdk.CallToolResult{Content: content}, links, nil
}

func (s *Server) handleListLinks(ctx context.Context, _ *mcpsdk.CallToolRequest, args ListLinksArgs) (*mcpsdk.CallToolResult, []task.TaskLinkView, error) {
	ts, closeFn, err := s.resolveTaskSystem(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer closeFn()

	boardID := args.BoardID
	if boardID == 0 {
		boardID = 1
	}

	links, err := ts.ListLinks(boardID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list links: %w", err)
	}

	tasks, err := ts.List(boardID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	boardIDByInternal := make(map[int]int, len(tasks))
	for _, t := range tasks {
		boardIDByInternal[t.ID] = t.BoardTaskID
	}

	views := make([]task.TaskLinkView, 0, len(links))
	for _, l := range links {
		from, okFrom := boardIDByInternal[l.FromTaskID]
		to, okTo := boardIDByInternal[l.ToTaskID]
		if !okFrom || !okTo {
			continue
		}
		views = append(views, task.TaskLinkView{
			FromBoardTaskID: from,
			ToBoardTaskID:   to,
			LinkType:        l.LinkType,
			CreatedAt:       l.CreatedAt,
		})
	}

	content := []mcpsdk.Content{&mcpsdk.TextContent{Text: scopeLine()}}
	if len(views) == 0 {
		content = append(content, &mcpsdk.TextContent{Text: "No links on this board"})
		return &mcpsdk.CallToolResult{Content: content}, views, nil
	}
	for _, v := range views {
		content = append(content, &mcpsdk.TextContent{Text: fmt.Sprintf("• #%d %s #%d", v.FromBoardTaskID, v.LinkType, v.ToBoardTaskID)})
	}

	return &mcpsdk.CallToolResult{Content: content}, views, nil
}
