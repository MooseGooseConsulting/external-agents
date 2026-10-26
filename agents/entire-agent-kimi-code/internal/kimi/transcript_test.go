package kimi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

func TestGenericJSONFixtureDoesNotBecomeChildDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-transcript.json")
	if e := os.WriteFile(path, []byte("{}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	files, pos, e := New().ExtractModifiedFiles(path, 0)
	if e != nil || len(files) != 0 || pos != 3 {
		t.Fatalf("generic JSON: files=%v position=%d err=%v", files, pos, e)
	}
}

func TestRecordsOffsetBeyondLengthClampsToEOF(t *testing.T) {
	b := []byte("{\"type\":\"turn.prompt\"}\n")
	for _, offset := range []int{len(b), len(b) + 5, len(b) * 10} {
		rs, pos, e := records(b, offset)
		if e != nil || len(rs) != 0 || pos != len(b) {
			t.Fatalf("offset %d: pos %d err %v records %d", offset, pos, e, len(rs))
		}
	}
	// Same clamping must hold for a caller that passes a stale cursor past EOF.
	rs, pos, e := records([]byte{}, 3)
	if e != nil || len(rs) != 0 || pos != 0 {
		t.Fatalf("empty: pos %d err %v records %d", pos, e, len(rs))
	}
}

func TestRecordsParseCRLFAndSkipMalformedLines(t *testing.T) {
	prompt := []byte("{\"type\":\"turn.prompt\",\"input\":\"second prompt\"}\r\n")
	think := []byte("{\"type\":\"context.append_loop_event\",\"event\":{\"type\":\"content.part\",\"part\":{\"type\":\"think\",\"think\":\"hidden\"}}}\r\n")
	malformed := []byte("not json at all\r\n")
	text := []byte("{\"type\":\"context.append_loop_event\",\"event\":{\"type\":\"content.part\",\"part\":{\"type\":\"text\",\"text\":\"visible\"}}}\n")
	b := bytes.Join([][]byte{prompt, think, malformed, text}, nil)

	rs, pos, e := records(b, 0)
	if e != nil || pos != len(b) {
		t.Fatalf("CRLF cursor: pos %d err %v", pos, e)
	}
	if len(rs) != 3 {
		t.Fatalf("malformed CRLF line not skipped: %d records", len(rs))
	}
	if s := contentText(rs[0].Input); s != "second prompt" {
		t.Fatalf("string input prompt lost: %q", s)
	}
	// A caller resuming from the malformed line still parses the tail
	// (the malformed line itself contributes no record).
	rs, pos, e = records(b, len(prompt)+len(think))
	if e != nil || len(rs) != 1 || pos != len(b) {
		t.Fatalf("resume after malformed: %d %d %v", len(rs), pos, e)
	}
}

func TestExtractFilesEditMultiEditSchemas(t *testing.T) {
	b := []byte(strings.Join([]string{
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"e1","name":"Edit","args":{"path":"src/edited.go"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"e2","name":"MULTIEDIT","args":{"file_path":"src/multi.go"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"e3","name":"StrReplaceFile","args":{"path":"src/replace.txt"}}}`,
		// Same file touched twice must collapse to one entry.
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"e4","name":"Write","args":{"path":"src/edited.go"}}}`,
		// A read-only tool and a Write without args must contribute nothing.
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"e5","name":"Read","args":{"path":"src/edited.go"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"e6","name":"Write"}}`,
		"",
	}, "\n"))
	files, pos, e := extractFiles(b, 0)
	if e != nil || pos != len(b) {
		t.Fatalf("cursor: pos %d err %v", pos, e)
	}
	want := []string{"src/edited.go", "src/multi.go", "src/replace.txt"}
	if len(files) != len(want) {
		t.Fatalf("got %v want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("got %v want %v", files, want)
		}
	}
}

func TestExtractSummaryIgnoresThinkingAndEmptyParts(t *testing.T) {
	b := []byte(strings.Join([]string{
		`{"type":"turn.prompt","input":[{"type":"text","text":"ignored prompt"}]}`,
		`{"type":"context.append_loop_event","event":{"type":"content.part","part":{"type":"think","think":"reasoning"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"content.part","part":{"type":"text","text":"alpha"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"content.part","part":{"type":"text","text":"beta"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"content.part","part":{"type":"text","text":""}}}`,
		"",
	}, "\n"))
	path := filepath.Join(t.TempDir(), "summary.jsonl")
	put(t, path, b)
	s, ok, e := New().ExtractSummary(path)
	if e != nil || !ok || s != "alphabeta" {
		t.Fatalf("summary: %q %v %v", s, ok, e)
	}
	put(t, path, []byte{})
	if s, ok, e = New().ExtractSummary(path); e != nil || ok || s != "" {
		t.Fatalf("empty transcript: %q %v %v", s, ok, e)
	}
}

func TestCompactTranscriptToolResultStatus(t *testing.T) {
	b := []byte(strings.Join([]string{
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"ok1","name":"Read","args":{"path":"a.txt"}},"time":1792952238358}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.result","toolCallId":"ok1","result":{"output":"file body","error":null}}}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"bad1","name":"Read","args":{"path":"missing.txt"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.result","toolCallId":"bad1","result":{"output":"x","error":"no such file"}}}`,
		// A tool call whose result never arrived must not invent one.
		`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"hung1","name":"Bash","args":{"command":"sleep"}}}`,
		"",
	}, "\n"))
	path := filepath.Join(t.TempDir(), "statuses.jsonl")
	put(t, path, b)
	resp, e := New().CompactTranscript(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := base64.StdEncoding.DecodeString(resp.Transcript)
	if e != nil {
		t.Fatal(e)
	}
	type line struct {
		Type    string `json:"type"`
		Content []struct {
			Result *struct {
				Status string `json:"status"`
				Output any    `json:"output"`
			} `json:"result"`
		} `json:"content"`
	}
	var lines []line
	rest := raw
	for len(rest) > 0 {
		n := bytes.IndexByte(rest, '\n')
		if n < 0 {
			n = len(rest)
		}
		var l line
		if e = json.Unmarshal(rest[:n], &l); e != nil {
			t.Fatal(e)
		}
		lines = append(lines, l)
		rest = rest[min(n+1, len(rest)):]
	}
	var statuses []string
	for _, l := range lines {
		for _, c := range l.Content {
			if c.Result != nil {
				statuses = append(statuses, c.Result.Status)
			}
		}
	}
	if len(statuses) != 2 || statuses[0] != "success" || statuses[1] != "error" {
		t.Fatalf("statuses %v", statuses)
	}
	// A transcript with only thinking parts yields no compact content.
	put(t, path, []byte(`{"type":"context.append_loop_event","event":{"type":"content.part","part":{"type":"think","think":"only"}}}`+"\n"))
	if _, e = New().CompactTranscript(path); e == nil || !strings.Contains(e.Error(), "no conversation content") {
		t.Fatalf("empty-content guard: %v", e)
	}
}

func TestChildTranscriptsConfinedAndSkipsNonJSONL(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "agent-0.jsonl"), []byte("{\"type\":\"usage.record\"}\n"))
	put(t, filepath.Join(dir, "notes.txt"), []byte("skip me"))
	put(t, filepath.Join(dir, "sub", "agent-1.jsonl"), []byte("{\"type\":\"usage.record\"}\n"))
	outside := filepath.Join(t.TempDir(), "leak.jsonl")
	put(t, outside, []byte("{\"type\":\"usage.record\"}\n"))
	if e := os.Symlink(outside, filepath.Join(dir, "escape.jsonl")); e != nil {
		t.Log("symlink creation unavailable:", e)
	} else {
		defer os.Remove(filepath.Join(dir, "escape.jsonl"))
	}
	children, e := childTranscripts(dir)
	if e != nil {
		t.Fatal(e)
	}
	if len(children) != 1 {
		t.Fatalf("confinement or filter failed: %d children", len(children))
	}
	if none, e := childTranscripts(filepath.Join(dir, "missing")); e != nil || none != nil {
		t.Fatalf("missing dir: %v %v", none, e)
	}
}

// The upstream analyzer suite passes a transcript named test-transcript.json;
// deriving <basename>/subagents from it lands under a file, which must read as
// "no subagent transcripts" rather than a directory error (ENOTDIR on Unix).
func TestChildTranscriptsToleratesPathUnderFile(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "test-transcript.json"), []byte(`{}`))
	if none, e := childTranscripts(filepath.Join(root, "test-transcript.json", "subagents")); e != nil || none != nil {
		t.Fatalf("path under file: %v %v", none, e)
	}
	files, _, e := New().ExtractModifiedFiles(filepath.Join(root, "test-transcript.json"), 0)
	if e != nil || len(files) != 0 {
		t.Fatalf("non-jsonl transcript: %v %v", files, e)
	}
}

func TestExtractModifiedFilesDedupesMainAndChild(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "sess.jsonl")
	put(t, main, []byte(`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"w1","name":"Write","args":{"path":"shared.txt"}}}`+"\n"))
	put(t, filepath.Join(root, "sess", "subagents", "agent-0.jsonl"),
		[]byte(strings.Join([]string{
			`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"w2","name":"Edit","args":{"path":"shared.txt"}}}`,
			`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"w3","name":"Write","args":{"path":"child-only.txt"}}}`,
			"",
		}, "\n")))
	files, pos, e := New().ExtractModifiedFiles(main, 0)
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"child-only.txt", "shared.txt"}
	if len(files) != len(want) {
		t.Fatalf("got %v want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("got %v want %v", files, want)
		}
	}
	if pos != len([]byte(`{"type":"context.append_loop_event","event":{"type":"tool.call","toolCallId":"w1","name":"Write","args":{"path":"shared.txt"}}}`+"\n")) {
		t.Fatalf("cursor moved by child: %d", pos)
	}
}

func TestTokenAggregationEdgeCases(t *testing.T) {
	b := []byte(strings.Join([]string{
		// Embedded step-end usage must not be counted in addition to usage.record.
		`{"type":"context.append_loop_event","event":{"type":"step.end","usage":{"inputOther":999,"output":999,"inputCacheRead":999}}}`,
		`{"type":"usage.record","usage":{"inputOther":10,"output":5,"inputCacheRead":20,"inputCacheCreation":3}}`,
		// A usage record without the usage object still counts as one API call.
		`{"type":"usage.record","agentId":"main"}`,
		"",
	}, "\n"))
	u, e := tokenUsage(b, 0)
	if e != nil {
		t.Fatal(e)
	}
	if u.Input != 10 || u.Output != 5 || u.Read != 20 || u.Create != 3 || u.Calls != 2 {
		t.Fatalf("usage: %+v", u)
	}
	// Offset past every usable record must yield zero usage.
	u, e = tokenUsage(b, len(b))
	if e != nil || u.Calls != 0 {
		t.Fatalf("offset tail: %+v %v", u, e)
	}
	// calculate-tokens deliberately ignores the subagents directory.
	var out bytes.Buffer
	if e = New().HandleAggregate("calculate-tokens", []string{"--subagents-dir", t.TempDir()}, bytes.NewReader(b), &out); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), "subagent_tokens") {
		t.Fatal("calculate-tokens aggregated subagents")
	}
	var resp protocol.ExtractFilesResponse
	if e = json.Unmarshal(out.Bytes(), &resp); e != nil || len(resp.Files) != 0 {
		t.Fatalf("token response shape: %v", out.String())
	}
	// Unknown flag must be an error, not silent success.
	if e = New().HandleAggregate("calculate-tokens", []string{"--bogus"}, bytes.NewReader(b), &out); e == nil {
		t.Fatal("accepted unknown flag")
	}
}
