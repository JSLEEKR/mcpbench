# Round Log — mcpbench

- **Round**: 84
- **Project**: mcpbench
- **Category**: MCP Tooling / Load Testing & Benchmarking
- **Language**: Go 1.22
- **Date**: 2026-04-17
- **Build status**: new
- **Quality gate**: TBD (eval phase fills in)
- **Tests**: 216 (transport ~54, scenario ~40, metrics ~22, compare ~18, report ~14, orchestrator ~18, cli ~24, errors ~15, jsonrpc ~11)
- **Test flags**: `go test -race ./...` passes clean
- **go vet**: clean

## Summary

A protocol-aware load tester and benchmarker for MCP servers. Fires concurrent
JSON-RPC 2.0 requests at any MCP server (stdio subprocess or HTTP/SSE endpoint),
measures per-tool latency distributions and error rates, and emits JSON / CSV
/ Prometheus output. `compare` subcommand diffs two runs and exits non-zero on
regression for CI gating.

## Features shipped

- stdio transport with subprocess lifecycle (SIGTERM + 5s grace + SIGKILL)
- HTTP transport with optional SSE (`text/event-stream`) parsing
- Scenario YAML with weighted tools, arg templating, warmup/cooldown
- Per-tool + overall metrics reservoir, nearest-rank percentiles
- Error categorization (timeout / jsonrpc / transport / template / other)
- `compare` with relative / ms / percentage-point thresholds
- 4 output formats (human table, JSON, CSV, Prometheus exposition)

## Out of scope (documented)

- WebSocket transport
- Distributed / multi-node coordinator
- UI / dashboard
- Historical trend storage

## Files created

See `README.md` and package tree under `cmd/` + `internal/`.
