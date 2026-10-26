# Changelog

All notable changes to `entire-agent-kimi-code` are documented here. This
project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
adopts semantic versioning under the `agents/entire-agent-kimi-code/v*` tag
convention.

## [0.1.1] - 2026-10-26

### Fixed

- Archive verification hardening: refuse to extract archives with unsafe or
  non-regular members (tar-slip), derive the platform label from the archive
  filename instead of hardcoding linux, require the exec bit on unix binaries
  rather than chmodding them into place, and validate member paths for
  directory entries too.
- Subagent correlation: a new SubagentStart can never bind to an already
  completed child while a pending or active sibling matches; a delayed start
  parsed after its child finished binds only to a uniquely matching completed
  child, and several matching completed children stay ambiguous and are
  refused.
- Token accounting across resume: per-child byte cursors persist beside the
  child transcripts, keyed to the parent offset and the parent transcript
  endpoint. Retried invocations replay exactly their recorded range;
  recalculations with a grown parent extend the recorded range from its
  start; `--offset 0` performs a full recalculation with children counted in
  full; a cursor write failure fails the command instead of letting a later
  pass recount the same usage.
- Session reads: derived sidecar paths are resolved through symlinks and must
  remain inside the Entire-owned store before reading.
- Lifecycle: a parent-level Interrupt or StopFailure ends the parent turn even
  while a foreground child runs; child-attributed stop-class hooks remain
  attributed to the child.
- Release workflow: an existing release missing assets heals on rerun by
  uploading only the missing ones.
- Docs and e2e: README checksum verification greps the downloaded archive's
  own line; the version-stamp comment matches the goreleaser ldflags; e2e
  StartSession waits for the initial Kimi prompt before sending input.

## [0.1.0] - 2026-10-26

### Added

- Standalone `entire-agent-kimi-code` binary implementing the Entire external
  agent protocol for the Kimi Code CLI, tested against Kimi 2.3.2 and Entire
  0.12.1 on Windows.
- Nine managed hook rules: SessionStart, TurnStarted, Stop, StopFailure,
  Interrupt, PreCompact, SessionEnd, SubagentStart and SubagentStop.
  TurnStarted is the only managed turn boundary and covers user, task and
  system origins; UserPromptSubmit remains an unmanaged diagnostic no-op.
- Native wire capture: read-only, byte-identical raw snapshots of
  `$KIMI_CODE_HOME/sessions/.../wire.jsonl` and `state.json` under ignored
  `.entire/metadata/kimi-code/`, deferring partially appended last records
  without losing cursor position.
- Checkpoint compaction into Entire Transcript Format with token accounting
  that counts `usage.record` once per call and ignores duplicate `step.end`
  usage.
- Concurrent subagent correlation: foreground and background children are
  matched through native spawn/tool prompt and completion records; ambiguous
  identical child payloads fail explicitly instead of guessing identities.
- Entire 0.12.1 runtime compatibility mode: repository-local translation of
  the runtime's line-count cursors to raw byte boundaries, overridable with
  `ENTIRE_KIMI_OFFSET_UNIT=bytes|lines`.
- PreCompact as a tested explicit no-op so append-only native compaction
  records never reset byte cursors or replay historical token usage.
- Config safety: comment-delimited managed block, timestamped backups,
  atomic writes, current-user-only Windows backup DACL, concurrent-edit
  rejection, and `uninstall-hooks` that removes only the managed block.

### Packaging

- Release workflow (`.github/workflows/kimi-code-release.yml`) building
  archives and `checksums.txt` from `agents/entire-agent-kimi-code/v*` tags
  with SHA-pinned GoReleaser, publishing them to the nested tag with GitHub
  CLI, and running post-release checksum plus host executable verification.
- Exact-version installation via
  `go install github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/cmd/entire-agent-kimi-code@v0.1.0`.
- No Kimi plugin is installed, no daemon runs, and there is no auto-update.

[0.1.1]: https://github.com/MooseGooseConsulting/external-agents/releases/tag/agents%2Fentire-agent-kimi-code%2Fv0.1.1
[0.1.0]: https://github.com/MooseGooseConsulting/external-agents/releases/tag/agents%2Fentire-agent-kimi-code%2Fv0.1.0
