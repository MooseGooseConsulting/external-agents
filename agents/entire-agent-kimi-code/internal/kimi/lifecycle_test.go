package kimi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lifecyclePayload loads a captured hook payload and re-points it at the test
// repository, since the recorded cwd belongs to the capture machine.
func lifecyclePayload(t *testing.T, event, repo string) []byte {
	t.Helper()
	b, e := os.ReadFile("../../testdata/hooks/lifecycle-" + event + ".json")
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	m["cwd"] = repo
	out, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func lifecycleNativeFixture(t *testing.T, repo, home, transcript string) []byte {
	t.Helper()
	dir := filepath.Join(home, "sessions", "wd_test", "session_lifecycle")
	path := filepath.Join(dir, "agents", "main", "wire.jsonl")
	b, e := os.ReadFile("../../testdata/transcripts/" + transcript)
	if e != nil {
		t.Fatal(e)
	}
	put(t, path, b)
	idx, e := json.Marshal(map[string]string{"sessionId": "session_lifecycle", "sessionDir": dir, "workDir": repo})
	if e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(home, "session_index.jsonl"), append(idx, '\n'))
	return b
}

func lifecycleTranscript(t *testing.T, name string) (string, []byte) {
	t.Helper()
	path := filepath.Join("../../testdata/transcripts", name)
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return path, b
}

func TestLifecycleHookEventsMapWithOutcomeMetadata(t *testing.T) {
	repo, home := setup(t)
	native := lifecycleNativeFixture(t, repo, home, "lifecycle-compaction.jsonl")
	cases := []struct {
		event, hook string
		kind        int
		metadata    map[string]string
	}{
		// Failure and interrupt are Stop-class turn boundaries that carry an
		// outcome discriminator instead of a clean stop. The real captured
		// StopFailure payload also reports error_type and error_message,
		// which must survive into the event metadata.
		{"StopFailure", "stop-failure", 3, map[string]string{"hook": "stop-failure", "outcome": "stop-failure", "error_type": "APITimeoutError", "error_message": "Request timed out."}},
		{"Interrupt", "interrupt", 3, map[string]string{"hook": "interrupt", "outcome": "interrupt", "reason": "cancelled"}},
		{"SessionEnd", "session-end", 5, map[string]string{"hook": "session-end", "reason": "exit"}},
	}
	for _, c := range cases {
		ev, e := New().ParseHook(c.hook, lifecyclePayload(t, c.event, repo))
		if e != nil || ev == nil {
			t.Fatalf("%s: event %v error %v", c.event, ev, e)
		}
		if ev.Type != c.kind || ev.SessionID != "session_lifecycle" {
			t.Fatalf("%s mapped to type %d session %q", c.event, ev.Type, ev.SessionID)
		}
		if len(ev.Metadata) != len(c.metadata) {
			t.Fatalf("%s metadata %v, want %v", c.event, ev.Metadata, c.metadata)
		}
		for k, want := range c.metadata {
			if ev.Metadata[k] != want {
				t.Fatalf("%s metadata[%q] = %q, want %q", c.event, k, ev.Metadata[k], want)
			}
		}
		snapshot, e := os.ReadFile(ev.SessionRef)
		if e != nil {
			t.Fatalf("%s snapshot unreadable: %v", c.event, e)
		}
		if !bytes.Equal(snapshot, native) {
			t.Fatalf("%s snapshot altered the native wire log", c.event)
		}
	}
}

func TestLifecyclePostCompactIsNoOp(t *testing.T) {
	repo, home := setup(t)
	lifecycleNativeFixture(t, repo, home, "lifecycle-compaction.jsonl")
	ev, e := New().ParseHook("post-compact", lifecyclePayload(t, "PostCompact", repo))
	if ev != nil || e != nil {
		t.Fatalf("PostCompact produced an event: %v %v", ev, e)
	}
}

type compactLine struct {
	Type    string           `json:"type"`
	Content []map[string]any `json:"content"`
}

func compactLines(t *testing.T, transcript string) []compactLine {
	t.Helper()
	raw, e := base64.StdEncoding.DecodeString(transcript)
	if e != nil {
		t.Fatal(e)
	}
	var lines []compactLine
	rest := raw
	for len(rest) > 0 {
		n := bytes.IndexByte(rest, '\n')
		if n < 0 {
			n = len(rest)
		}
		var l compactLine
		if e = json.Unmarshal(rest[:n], &l); e != nil {
			t.Fatal(e)
		}
		lines = append(lines, l)
		rest = rest[min(n+1, len(rest)):]
	}
	return lines
}

func TestLifecycleCompactionTranscriptOffsetsAndContent(t *testing.T) {
	path, b := lifecycleTranscript(t, "lifecycle-compaction.jsonl")
	pos, e := New().GetTranscriptPosition(path)
	if e != nil || pos != len(b) {
		t.Fatalf("cursor %d want %d: %v", pos, len(b), e)
	}
	prompts, e := New().ExtractPrompts(path, 0)
	if e != nil || len(prompts) != 1 || prompts[0] != "Reply with exactly LIFECYCLE_ACP_READY and nothing else. Do not use tools." {
		t.Fatalf("prompts: %q %v", prompts, e)
	}
	// Resuming past the original prompt must not replay the compaction
	// summary as a new prompt or conversation record.
	n := bytes.IndexByte(b, '\n')
	if n < 0 {
		t.Fatal("fixture has no complete line")
	}
	after, e := New().ExtractPrompts(path, n+1)
	if e != nil || len(after) != 0 {
		t.Fatalf("compaction summary replayed as a prompt: %q %v", after, e)
	}
	files, end, e := New().ExtractModifiedFiles(path, 0)
	if e != nil || len(files) != 0 || end != len(b) {
		t.Fatalf("compaction invented files: %v %d %v", files, end, e)
	}
	// Thinking parts and the compaction summary are excluded from the
	// summary, which holds only rendered assistant text.
	s, ok, e := New().ExtractSummary(path)
	if e != nil || !ok || s != "LIFECYCLE_ACP_READY" {
		t.Fatalf("summary: %q %v %v", s, ok, e)
	}
	resp, e := New().CompactTranscript(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := base64.StdEncoding.DecodeString(resp.Transcript)
	if e != nil {
		t.Fatal(e)
	}
	lines := compactLines(t, resp.Transcript)
	if len(lines) != 2 {
		t.Fatalf("compact transcript emitted %d lines, want 2", len(lines))
	}
	if lines[0].Type != "user" || len(lines[0].Content) != 1 || lines[0].Content[0]["text"] != prompts[0] {
		t.Fatalf("user line mismatched: %+v", lines[0])
	}
	if lines[1].Type != "assistant" || len(lines[1].Content) != 1 ||
		lines[1].Content[0]["type"] != "text" || lines[1].Content[0]["text"] != "LIFECYCLE_ACP_READY" {
		t.Fatalf("assistant line mismatched: %+v", lines[1])
	}
	if strings.Contains(string(raw), "engineering task") {
		t.Fatal("compaction summary leaked into the compact transcript")
	}
}

func TestLifecycleFailureAndInterruptTranscripts(t *testing.T) {
	for _, c := range []struct {
		file, token string
	}{
		{"lifecycle-failure.jsonl", "LIFECYCLE_FAIL_OK"},
		{"lifecycle-interrupt.jsonl", "water cooling loops"},
	} {
		path, b := lifecycleTranscript(t, c.file)
		if pos, e := New().GetTranscriptPosition(path); e != nil || pos != len(b) {
			t.Fatalf("%s cursor %d want %d: %v", c.file, pos, len(b), e)
		}
		prompts, e := New().ExtractPrompts(path, 0)
		if e != nil || len(prompts) != 1 || !strings.Contains(prompts[0], c.token) {
			t.Fatalf("%s prompts: %q %v", c.file, prompts, e)
		}
		// Neither a failed nor an interrupted turn may contribute rendered
		// assistant text or modified files.
		if s, ok, e := New().ExtractSummary(path); e != nil || ok || s != "" {
			t.Fatalf("%s summary: %q %v %v", c.file, s, ok, e)
		}
		files, end, e := New().ExtractModifiedFiles(path, 0)
		if e != nil || len(files) != 0 || end != len(b) {
			t.Fatalf("%s files: %v %d %v", c.file, files, end, e)
		}
		// token_counting bookkeeping is not an API call: usage must stay
		// empty for turns that never completed a request.
		if u, e := tokenUsage(b, 0); e != nil || u != (tokens{}) {
			t.Fatalf("%s usage: %+v %v", c.file, u, e)
		}
		// The compact transcript keeps the user prompt but invents no
		// assistant output for the aborted turn.
		resp, e := New().CompactTranscript(path)
		if e != nil {
			t.Fatalf("%s compact: %v", c.file, e)
		}
		lines := compactLines(t, resp.Transcript)
		var text string
		if len(lines) == 1 && len(lines[0].Content) == 1 {
			text, _ = lines[0].Content[0]["text"].(string)
		}
		if len(lines) != 1 || lines[0].Type != "user" || len(lines[0].Content) != 1 ||
			!strings.Contains(text, c.token) {
			t.Fatalf("%s compact lines: %+v", c.file, lines)
		}
	}
}

func TestCapturedLifecycleEdgesPreserveNativeLog(t *testing.T) {
	for _, tc := range []struct {
		event, hook, transcript, metadata, value string
		kind                                     int
	}{
		{"Interrupt", "interrupt", "interrupt", "reason", "cancelled", 3},
		{"StopFailure", "stop-failure", "failure", "error_type", "APITimeoutError", 3},
		{"SessionEnd", "session-end", "interrupt", "reason", "exit", 5},
		{"PreCompact", "pre-compact", "compaction", "", "", 0},
		{"PostCompact", "post-compact", "compaction", "", "", 0},
	} {
		t.Run(tc.event, func(t *testing.T) {
			repo, home := setup(t)
			path := nativeFixture(t, repo, home)
			wire, err := os.ReadFile("../../testdata/transcripts/lifecycle-" + tc.transcript + ".jsonl")
			if err != nil {
				t.Fatal(err)
			}
			put(t, path, wire)
			b, err := os.ReadFile("../../testdata/hooks/lifecycle-" + tc.event + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err = json.Unmarshal(b, &payload); err != nil {
				t.Fatal(err)
			}
			payload["session_id"], payload["cwd"] = "session_test", repo
			b, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			ev, err := New().ParseHook(tc.hook, b)
			if err != nil {
				t.Fatal(err)
			}
			if tc.kind == 0 {
				if ev != nil {
					t.Fatalf("append-only compaction reset offsets: %+v", ev)
				}
			} else {
				if ev == nil || ev.Type != tc.kind || ev.Metadata[tc.metadata] != tc.value {
					t.Fatalf("lost captured lifecycle outcome: %+v", ev)
				}
				stored, err := os.ReadFile(ev.SessionRef)
				if err != nil || !bytes.Equal(stored, wire) {
					t.Fatalf("snapshot changed native bytes: %v", err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, wire) {
				t.Fatalf("hook modified Kimi's log: %v", err)
			}
		})
	}
}
