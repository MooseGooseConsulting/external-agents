package kimi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

const wireLoopEvent = "context.append_loop_event"

type record struct {
	Type    string          `json:"type"`
	AgentID string          `json:"agentId"`
	Time    int64           `json:"time"`
	Input   json.RawMessage `json:"input"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Origin  struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	} `json:"message"`
	Event struct {
		Type         string          `json:"type"`
		FinishReason string          `json:"finishReason"`
		UUID         string          `json:"uuid"`
		ToolID       string          `json:"toolCallId"`
		Name         string          `json:"name"`
		Args         json.RawMessage `json:"args"`
		Part         struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Think string `json:"think"`
		} `json:"part"`
		Result struct {
			Output json.RawMessage `json:"output"`
			Error  json.RawMessage `json:"error"`
		} `json:"result"`
	} `json:"event"`
	Usage struct {
		Input  int `json:"inputOther"`
		Output int `json:"output"`
		Read   int `json:"inputCacheRead"`
		Create int `json:"inputCacheCreation"`
	} `json:"usage"`
}

func contentText(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(b, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type == "text" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// Only complete newline-terminated records advance the cursor. An incomplete
// append remains available on the next call; offsets are always raw bytes.
func records(b []byte, offset int) ([]record, int, error) {
	if offset < 0 {
		return nil, offset, errors.New("transcript offset out of range")
	}
	if offset >= len(b) {
		return []record{}, len(b), nil
	}
	if offset > 0 && !bytes.HasSuffix(b[:offset], []byte{'\n'}) {
		n := bytes.IndexByte(b[offset:], '\n')
		if n < 0 {
			return []record{}, offset, nil
		}
		offset += n + 1
	}
	out := []record{}
	pos := offset
	for pos < len(b) {
		n := bytes.IndexByte(b[pos:], '\n')
		if n < 0 {
			break
		}
		line := bytes.TrimSpace(b[pos : pos+n])
		pos += n + 1
		if len(line) == 0 {
			continue
		}
		var r record
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, pos, nil
}
func extractFiles(b []byte, offset int) ([]string, int, error) {
	rs, pos, e := records(b, offset)
	if e != nil {
		return nil, pos, e
	}
	set := map[string]bool{}
	for _, r := range rs {
		if r.Type != wireLoopEvent || r.Event.Type != "tool.call" {
			continue
		}
		switch strings.ToLower(r.Event.Name) {
		case "write", "edit", "multiedit", "writefile", "strreplacefile":
			var args struct {
				Path string `json:"path"`
				File string `json:"file_path"`
			}
			if json.Unmarshal(r.Event.Args, &args) == nil {
				p := args.Path
				if p == "" {
					p = args.File
				}
				if p != "" {
					set[p] = true
				}
			}
		}
	}
	out := []string{}
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, pos, nil
}
func (a *Agent) GetTranscriptPosition(path string) (int, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return 0, nil
	}
	if e != nil {
		return 0, e
	}
	_, pos, e := records(b, 0)
	return outputOffset(b, pos), e
}
func (a *Agent) ExtractModifiedFiles(path string, offset int) ([]string, int, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, offset, e
	}
	files, pos, e := extractFiles(b, inputByteOffset(b, offset))
	if e != nil {
		return nil, pos, e
	}
	pos = outputOffset(b, pos)
	if filepath.Ext(path) != ".jsonl" {
		return files, pos, nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	children, e := childTranscripts(filepath.Join(filepath.Dir(path), id, "subagents"))
	if e != nil {
		return nil, pos, e
	}
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	for _, b := range children {
		f, _, e := extractFiles(b, 0)
		if e != nil {
			return nil, pos, e
		}
		for _, p := range f {
			set[p] = true
		}
	}
	files = []string{}
	for p := range set {
		files = append(files, p)
	}
	sort.Strings(files)
	return files, pos, nil
}
func (a *Agent) ExtractPrompts(path string, offset int) ([]string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	rs, _, e := records(b, inputByteOffset(b, offset))
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, r := range rs {
		if r.Type == "turn.prompt" {
			if s := contentText(r.Input); s != "" {
				out = append(out, s)
			}
		}
	}
	return out, nil
}
func (a *Agent) ExtractSummary(path string) (string, bool, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", false, e
	}
	rs, _, e := records(b, 0)
	if e != nil {
		return "", false, e
	}
	var s strings.Builder
	for _, r := range rs {
		if r.Type == wireLoopEvent && r.Event.Type == "content.part" && r.Event.Part.Type == "text" {
			s.WriteString(r.Event.Part.Text)
		}
	}
	return s.String(), s.Len() > 0, nil
}

type tokens struct {
	Input  int     `json:"input_tokens"`
	Output int     `json:"output_tokens"`
	Read   int     `json:"cache_read_tokens"`
	Create int     `json:"cache_creation_tokens"`
	Calls  int     `json:"api_call_count"`
	Sub    *tokens `json:"subagent_tokens,omitempty"`
}

func tokenUsage(b []byte, offset int) (tokens, error) {
	rs, _, e := records(b, offset)
	if e != nil {
		return tokens{}, e
	}
	var t tokens
	for _, r := range rs {
		if r.Type != "usage.record" {
			continue
		}
		t.Input += r.Usage.Input
		t.Output += r.Usage.Output
		t.Read += r.Usage.Read
		t.Create += r.Usage.Create
		t.Calls++
	}
	return t, nil
}
func (a *Agent) HandleAggregate(command string, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offset := fs.Int("offset", 0, "")
	dir := fs.String("subagents-dir", "", "")
	if e := fs.Parse(args); e != nil {
		return e
	}
	b, e := io.ReadAll(in)
	if e != nil {
		return e
	}
	byteOffset := inputByteOffset(b, *offset)
	files, _, e := extractFiles(b, byteOffset)
	if e != nil {
		return e
	}
	t, e := tokenUsage(b, byteOffset)
	if e != nil {
		return e
	}
	if command != "calculate-tokens" && *dir != "" {
		children, e := childTranscripts(*dir)
		if e != nil {
			return e
		}
		sub := tokens{}
		for _, b := range children {
			f, _, e := extractFiles(b, 0)
			if e != nil {
				return e
			}
			files = append(files, f...)
			u, e := tokenUsage(b, 0)
			if e != nil {
				return e
			}
			sub.Input += u.Input
			sub.Output += u.Output
			sub.Read += u.Read
			sub.Create += u.Create
			sub.Calls += u.Calls
		}
		if sub.Calls > 0 {
			t.Sub = &sub
		}

		t.Input += sub.Input
		t.Output += sub.Output
		t.Read += sub.Read
		t.Create += sub.Create
		t.Calls += sub.Calls
	}
	if command == "extract-all-modified-files" {
		set := map[string]bool{}
		for _, f := range files {
			set[f] = true
		}
		files = []string{}
		for f := range set {
			files = append(files, f)
		}
		sort.Strings(files)
		return protocol.WriteJSON(out, protocol.ExtractFilesResponse{Files: files})
	}
	return protocol.WriteJSON(out, t)
}

func (a *Agent) CompactTranscript(path string) (protocol.CompactTranscriptResponse, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return protocol.CompactTranscriptResponse{}, e
	}
	rs, _, e := records(b, 0)
	if e != nil {
		return protocol.CompactTranscriptResponse{}, e
	}
	results := map[string]any{}
	for _, r := range rs {
		if r.Type == wireLoopEvent && r.Event.Type == "tool.result" {
			var value any
			_ = json.Unmarshal(r.Event.Result.Output, &value)
			status := "success"
			if len(r.Event.Result.Error) > 0 && string(r.Event.Result.Error) != "null" {
				status = "error"
			}
			results[r.Event.ToolID] = map[string]any{"output": value, "status": status}
		}
	}
	var out bytes.Buffer
	var encodeErr error
	emit := func(r record, kind string, content any) {
		line := map[string]any{"v": 1, "agent": "kimi-code", "cli_version": "2.3.2", "type": kind, "content": content}
		if r.Time > 0 {
			line["ts"] = time.UnixMilli(r.Time).UTC().Format(time.RFC3339Nano)
		}
		if encodeErr == nil {
			encodeErr = json.NewEncoder(&out).Encode(line)
		}
	}
	for _, r := range rs {
		if r.Type == "turn.prompt" {
			if s := contentText(r.Input); s != "" {
				emit(r, "user", []any{map[string]any{"text": s}})
			}
		}
		if r.Type != wireLoopEvent {
			continue
		}
		switch r.Event.Type {
		case "content.part":
			p := r.Event.Part
			if p.Type == "text" && p.Text != "" {
				emit(r, "assistant", []any{map[string]any{"type": "text", "text": p.Text}})
			}
		case "tool.call":
			var input any
			_ = json.Unmarshal(r.Event.Args, &input)
			tool := map[string]any{"type": "tool_use", "id": r.Event.ToolID, "name": r.Event.Name, "input": input}
			if result, ok := results[r.Event.ToolID]; ok {
				tool["result"] = result
			}
			emit(r, "assistant", []any{tool})
		}
	}
	if encodeErr != nil {
		return protocol.CompactTranscriptResponse{}, encodeErr
	}
	if out.Len() == 0 {
		return protocol.CompactTranscriptResponse{}, errors.New("no conversation content in wire transcript")
	}
	return protocol.CompactTranscriptResponse{Transcript: base64.StdEncoding.EncodeToString(out.Bytes())}, nil
}
