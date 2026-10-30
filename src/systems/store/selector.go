// Package store's selector wires cainban-core's backend-neutral TaskStore /
// BoardStore interfaces to the single, local SQLite backend.
//
// The upstream (multi-user) cainban had a second DynamoDB backend selected by
// CAINBAN_BACKEND for the serverless/Lambda path. cainban-core is the local,
// single-user product: there is exactly one backend (SQLite over the local
// ~/.cainban tree), so there is no backend selector and no AWS dependency.
package store

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hmain/cainban-core/src/systems/board"
	"github.com/hmain/cainban-core/src/systems/storage"
	"github.com/hmain/cainban-core/src/systems/task"
)

// OpenTask opens the local SQLite database at sqlitePath and returns a
// TaskStore plus a close func the caller MUST call when done. CGO (go-sqlite3)
// is required.
func OpenTask(_ context.Context, sqlitePath string) (TaskStore, func() error, error) {
	db, err := storage.New(sqlitePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open sqlite backend: %w", err)
	}
	return task.New(db.Conn()), db.Close, nil
}

// OpenBoard returns a BoardStore over the local board.System rooted at the
// parent directory of sqlitePath (the ~/.cainban config dir), plus a no-op
// close func (board.System holds no open handle).
func OpenBoard(_ context.Context, sqlitePath string) (BoardStore, func() error, error) {
	configDir := filepath.Dir(sqlitePath)
	bs := newSQLiteBoardStore(board.NewWithConfigDir(configDir))
	return bs, func() error { return nil }, nil
}
