package kimi

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestConcurrentSameNameChildrenUsePayloadIdentity(t *testing.T) {
	repo, home := setup(t)
	path := nativeFixture(t, repo, home)
	data, e := os.ReadFile("../../testdata/transcripts/background-main.jsonl")
	if e != nil {
		t.Fatal(e)
	}
	put(t, path, data)
	var spawns []struct{ ID, Name, Tool, Prompt, Response string }
	prompts := map[string]string{}
	var rows []map[string]any
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var r map[string]any
		if json.Unmarshal(line, &r) == nil {
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		if r["type"] == "context.append_loop_event" {
			ev, _ := r["event"].(map[string]any)
			if ev["type"] == "tool.call" && ev["name"] == "Agent" {
				args, _ := ev["args"].(map[string]any)
				id, _ := ev["toolCallId"].(string)
				prompt, _ := args["prompt"].(string)
				prompts[id] = prompt
			}
		}
		if r["type"] == "subagent.spawned" {
			id, _ := r["subagentId"].(string)
			name, _ := r["subagentName"].(string)
			tool, _ := r["parentToolCallId"].(string)
			spawns = append(spawns, struct{ ID, Name, Tool, Prompt, Response string }{ID: id, Name: name, Tool: tool, Prompt: prompts[tool]})
		}
		if r["type"] == "subagent.completed" {
			for i := range spawns {
				if spawns[i].ID == r["subagentId"] {
					spawns[i].Response, _ = r["resultSummary"].(string)
				}
			}
		}
	}
	if len(spawns) != 2 {
		t.Fatal("fixture must contain two children", len(spawns))
	}
	for _, s := range spawns {
		p, _ := json.Marshal(map[string]any{"session_id": "session_test", "cwd": repo, "agent_name": s.Name, "response": s.Response})
		ev, e := New().ParseHook("subagent-stop", p)
		if e != nil || ev == nil || ev.SubagentID != s.ID || ev.ToolUseID != s.Tool {
			t.Fatalf("child %s was miscorrelated: %v %v", s.ID, ev, e)
		}
	}
	// A child Stop after the parent already ended must not emit another TurnEnd.
	p, _ := json.Marshal(map[string]any{"session_id": "session_test", "cwd": repo})
	ev, e := New().ParseHook("stop", p)
	if e != nil || ev != nil {
		t.Fatal("late child Stop duplicated parent TurnEnd", ev, e)
	}
}

func TestAmbiguousChildResponseDoesNotStealIdentity(t *testing.T) {
	repo, home := setup(t)
	path := nativeFixture(t, repo, home)
	data, e := os.ReadFile("../../testdata/transcripts/background-main.jsonl")
	if e != nil {
		t.Fatal(e)
	}
	var ambiguous bytes.Buffer
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var r map[string]any
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r["type"] == "subagent.completed" {
			r["resultSummary"] = "Done"
		}
		b, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		ambiguous.Write(b)
		ambiguous.WriteByte('\n')
	}
	put(t, path, ambiguous.Bytes())
	p, _ := json.Marshal(map[string]string{"session_id": "session_test", "cwd": repo, "agent_name": "coder", "response": "Done"})
	ev, e := New().ParseHook("subagent-stop", p)
	if ev != nil || e == nil || !strings.Contains(e.Error(), "uniquely correlate") {
		t.Fatal("ambiguous hook invented a child identity", ev, e)
	}
}
