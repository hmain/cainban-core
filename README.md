# cainban-core

A terminal kanban board for one person and their AI agents. No accounts, no server, no cloud. Your tasks live in a local SQLite file, and any MCP-capable agent (Kiro, Claude Desktop, Cursor) can read and drive the same board you do.

This is the local core of [cainban](https://github.com/Awiant-AB/cainban): the CLI, the TUI, and the MCP server over SQLite. The multi-user, hosted product (GitHub-App authorization, a web board, real-time sync) is not here by design. If you want a private kanban on your own machine that your agent can touch, this is it.

## Install

You need Go 1.26+ and a C toolchain (cainban stores data in SQLite via cgo).

```sh
git clone https://github.com/hmain/cainban-core
cd cainban-core
./install.sh          # builds and installs to ~/.local/bin/cainban
```

Or straight from source:

```sh
CGO_ENABLED=1 go install github.com/hmain/cainban-core/cmd/cainban@latest
```

Pre-built binaries for Linux, macOS, and Windows are attached to each [release](https://github.com/hmain/cainban-core/releases).

## Use it

```sh
cainban init                        # create a board (named after the current dir)
cainban add "Write the README" --priority high
cainban list                        # or: cainban list doing
cainban move 1 doing
cainban tui                         # the interactive board
```

The full command set: `init`, `add`, `list`, `move`, `get`, `update`, `search`, `priority`, `board`, `link`, `unlink`, `links`, `delete`, `restore`, `tui`, `mcp`, `version`. Statuses are `todo` / `doing` / `done`; priorities are `none` / `low` / `medium` / `high` / `critical`.

## Give it to your agent

cainban speaks MCP over stdio, so an agent launches `cainban mcp` as a subprocess and gets the board as tools. Point your client at the binary:

```json
{
  "mcpServers": {
    "cainban": {
      "command": "cainban",
      "args": ["mcp"]
    }
  }
}
```

The agent then has sixteen tools: `create_task`, `list_tasks`, `get_task`, `update_task`, `update_task_status`, `update_task_priority`, `search_tasks`, `list_boards`, `change_board`, `list_activity`, `delete_task`, `restore_task`, `link_tasks`, `unlink_tasks`, `get_task_links`, `list_links`. They act on the same local board your CLI and TUI use, so you and the agent are never looking at different state.

There is no authentication because there is nothing to authenticate against: the server reads and writes one local SQLite file as you. For the same reason, the optional HTTP transport (`cainban mcp --http :8080`) refuses to bind anywhere but loopback.

## How it works

Everything lives under `~/.cainban/boards/<name>.db`. Each board is its own SQLite database with tasks, links, and an append-only activity log. The storage layer sits behind a small `TaskStore` / `BoardStore` interface, so the CLI, the TUI, and the MCP server all go through the same code path and see the same writes. Running `cainban tui` in one terminal while an agent drives `cainban mcp` in another is the normal case, not a corner one.

## Develop

```sh
make build        # -> bin/cainban (CGO on)
make quality      # gofmt check + go vet + golangci-lint + go test -race
make setup-hooks  # install the pre-commit hooks
```

CI runs the same gate plus `govulncheck` and a secret scan on every PR. Keep the tree gofmt-clean and `go mod tidy`.

## License

MIT. See [LICENSE](LICENSE).
