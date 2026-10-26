package kimi

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A session that launches a second child with the same agent name and prompt
// as an already-completed child must bind the new SubagentStart to the
// incomplete record; a completed child is never a start candidate.
func TestFindSubagentStartPrefersIncompleteChildOverCompletedDuplicate(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "session_x.jsonl")
	put(t, ref, []byte(strings.Join([]string{
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"t1","name":"Agent","args":{"prompt":"same prompt"}}}`,
		`{"type":"subagent.spawned","subagentId":"agent-0","subagentName":"coder","parentToolCallId":"t1","parentAgentId":"main"}`,
		`{"type":"subagent.completed","subagentId":"agent-0","resultSummary":"first done"}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"t2","name":"Agent","args":{"prompt":"same prompt"}}}`,
		`{"type":"subagent.spawned","subagentId":"agent-1","subagentName":"coder","parentToolCallId":"t2","parentAgentId":"main"}`,
		"",
	}, "\n")))
	subs := subagentRecords(ref)
	if len(subs) != 2 {
		t.Fatalf("parsed %d subagent records", len(subs))
	}
	sub, matches := findSubagent(subs, subagentQuery{Name: "coder", Prompt: "same prompt", Done: false})
	if matches != 1 || sub.ID != "agent-1" {
		t.Fatalf("start bound to completed child: matches=%d sub=%+v", matches, sub)
	}
	// A delayed start parsed after the ONLY matching child finished still
	// correlates through the unambiguous completed fallback.
	sub, matches = findSubagent(subs[:1], subagentQuery{Name: "coder", Prompt: "same prompt", Done: false})
	if matches != 1 || sub.ID != "agent-0" {
		t.Fatalf("delayed start lost: matches=%d sub=%+v", matches, sub)
	}
	// Two finished children with the same name and prompt stay ambiguous: a
	// start must never rebind one of them.
	twice := append(append([]subagent{}, subs[:1]...), subagent{ID: "agent-2", Name: "coder", Prompt: "same prompt", Response: "second done", Done: true})
	if _, matches = findSubagent(twice, subagentQuery{Name: "coder", Prompt: "same prompt", Done: false}); matches <= 1 {
		t.Fatalf("ambiguous completed children resolved: matches=%d", matches)
	}
	// A stop keeps correlating with the finished child.
	sub, matches = findSubagent(subs, subagentQuery{Name: "coder", Response: "first done", Done: true})
	if matches != 1 || sub.ID != "agent-0" {
		t.Fatalf("stop correlation lost: matches=%d sub=%+v", matches, sub)
	}
}

// The same scenario through the hook surface: the second SubagentStart must
// produce a correlated event rather than being dropped as ambiguous or bound
// to the completed sibling.
func TestSecondSubagentStartWithRepeatedPromptBindsIncompleteChild(t *testing.T) {
	repo, home := setup(t)
	dir := filepath.Join(home, "sessions", "wd_test", "session_test")
	path := filepath.Join(dir, "agents", "main", "wire.jsonl")
	put(t, path, []byte(strings.Join([]string{
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"t1","name":"Agent","args":{"prompt":"same prompt"}}}`,
		`{"type":"subagent.spawned","subagentId":"agent-0","subagentName":"coder","parentToolCallId":"t1","parentAgentId":"main"}`,
		`{"type":"subagent.completed","subagentId":"agent-0","resultSummary":"first done"}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"t2","name":"Agent","args":{"prompt":"same prompt"}}}`,
		`{"type":"subagent.spawned","subagentId":"agent-1","subagentName":"coder","parentToolCallId":"t2","parentAgentId":"main"}`,
		"",
	}, "\n")))
	idx, e := json.Marshal(map[string]string{"sessionId": "session_test", "sessionDir": dir, "workDir": repo})
	if e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(home, "session_index.jsonl"), append(idx, '\n'))
	p, _ := json.Marshal(map[string]any{"session_id": "session_test", "cwd": repo, "agent_name": "coder", "prompt": "same prompt", "tool_use_id": "t2"})
	ev, e := New().ParseHook("subagent-start", p)
	if e != nil || ev == nil {
		t.Fatalf("second start dropped or failed: %v %v", ev, e)
	}
	if ev.SubagentID != "agent-1" || ev.ToolUseID != "t2" {
		t.Fatalf("start misattributed: %+v", ev)
	}
}
