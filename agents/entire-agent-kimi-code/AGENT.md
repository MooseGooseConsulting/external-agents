# Kimi Code — local Entire integration

Owner: MooseGooseConsulting/external-agents. Delivery targets local Windows; no upstream acceptance gate.

## Research

Verified 2026-10-25 with Kimi 2.3.2 and Entire 0.12.1. Protocol source: entireio/cli tag v0.12.1, docs/architecture/external-agent-protocol.md and cmd/entire/cli/agent/external/{types,external}.go.
Native hooks: global KIMI_CODE_HOME/config.toml [[hooks]], JSON stdin, session_id and cwd. Prompt mode: kimi -p PROMPT (cannot combine --auto). Resume: kimi --session ID.
Captured real SessionStart, UserPromptSubmit, TurnStarted and Stop in a disposable repository, using existing authentication without copying credentials. Original config restored byte-for-byte.
UserPromptSubmit.prompt is a content-parts array; TurnStarted.prompt is text. Live failure testing found overlapping hooks can let a provider error overtake completion of TurnStart. TurnStarted is the only managed turn boundary and covers user, task and system origins; subagent-origin TurnStarted is suppressed. Nine hooks call the short-lived dispatch-hook command, which serializes the complete Entire hook process per session.
SessionEnd did not fire in prompt mode. Interactive /exit was verified to fire SessionEnd; do not infer it from headless process exit.

## Native data

session_index.jsonl has sessionId/sessionDir/workDir. Main transcript: sessions/<workDirKey>/<sessionId>/agents/main/wire.jsonl; agent-* siblings contain subagents. Never modify native records.
Verified actual prompt, assistant output and Write tool execution in wire.jsonl. turn.prompt.input holds text parts. context.append_loop_event.event has tool.call (name,args,toolCallId), tool.result and content.part. usage.record has per-call usage inputOther/output/inputCacheRead/inputCacheCreation; ignore duplicated step.end usage.

The installed Entire 0.12.1 executable uses line counts in `strategy.countTranscriptItems`, not the protocol's documented byte metric. Installation writes repository-local runtime.json compatibility metadata. Public standalone parsing uses bytes; live configured 0.12.1 reads translate line cursors to raw byte boundaries. The smoke test checks that the resumed checkpoint's api_call_count equals only the newly appended native usage records.

## Mapping

SessionStart→1; TurnStarted→2; Stop/StopFailure/Interrupt→3; SessionEnd→5; SubagentStart→6; SubagentStop→7. TurnStarted is the only turn boundary for user, task and system origins; subagent-origin TurnStarted is suppressed, and PostCompact is no-op. UserPromptSubmit is an unmanaged diagnostic no-op. PreCompact is a documented no-op: real compaction appends to wire.jsonl without truncation, so emitting Entire's offset-resetting Compaction event would replay historical records and double-count tokens.
Session helpers resolve native paths from the index with repository/containment validation. Raw reads/chunks preserve bytes. write-session stores restoration data only in Entire-owned storage; it does not rewrite Kimi sessions. Compact-transcript converts wire events to Entire format for checkpoint readability. Transcript analysis extracts structured Write/Edit paths and user prompts; arbitrary shell writes are not inferred.

## Test prerequisites and limits

Real tests require kimi and entire on PATH, existing authenticated Kimi config and a disposable Git repository. Global hook installation affects all repositories, while Entire dispatch remains repository scoped. Pure protocol tests use isolated KIMI_CODE_HOME. The inherited lifecycle harness assumes Unix/tmux; scripts/smoke-kimi.py, smoke-failure.py and smoke-subagents.py provide native Windows verification. capture-kimi-lifecycle.py captures ACP lifecycle fixtures with bounded reads and restores temporary diagnostic hooks safely.
Real compaction, Escape interruption, interactive session end and foreground subagent capture were exercised. StopFailure was captured with an isolated loopback-only HTTP 400 provider and no real credentials. See EVIDENCE.md for outcomes and remaining limits. No AI summary provider, background daemon or historical import.
