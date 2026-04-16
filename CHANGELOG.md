# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-04-17

### Added

- Initial release.
- `mcpbench run` — load test with stdio or HTTP/SSE transport.
- `mcpbench compare` — regression gating with relative, absolute-ms, and
  percentage-point thresholds.
- `mcpbench scenario-validate` — standalone scenario YAML validator.
- Scenario YAML DSL with weighted tool selection, arg templating
  (`{{.Iter}}`, `{{.Env.X}}`, `{{.Rand}}`), warmup/cooldown/think-time.
- Per-tool latency reservoir (p50/p90/p95/p99, mean, stddev).
- Error categorization: `ok` / `timeout` / `jsonrpc` / `transport` /
  `template` / `other`.
- Output formats: human table, JSON (v1 on-disk format), CSV, Prometheus
  text exposition.
- Subprocess lifecycle (`SIGTERM` with 5-second grace, then `SIGKILL`).
- 216 tests across transport, scenario, metrics, compare, CLI, and e2e flows.
