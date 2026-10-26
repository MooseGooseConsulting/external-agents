package kimi

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type subagent struct {
	ID          string `json:"subagentId"`
	Name        string `json:"subagentName"`
	ToolID      string `json:"parentToolCallId"`
	Parent      string `json:"parentAgentId"`
	Description string `json:"description"`
	Background  bool   `json:"runInBackground"`
	Done        bool
	Type        string `json:"type"`
	Prompt      string
	Response    string `json:"resultSummary"`
}

func subagentRecords(ref string) []subagent {
	b, _ := os.ReadFile(ref)
	data := [][]byte{b}
	id := strings.TrimSuffix(filepath.Base(ref), ".jsonl")
	children, _ := childTranscripts(filepath.Join(filepath.Dir(ref), id, "subagents"))
	data = append(data, children...)
	var lines [][]byte
	for _, b := range data {
		lines = append(lines, bytes.Split(b, []byte{'\n'})...)
	}
	prompts := map[string]string{}
	for _, line := range lines {
		var r record
		if json.Unmarshal(line, &r) != nil || r.Type != wireLoopEvent || r.Event.Type != "tool.call" || r.Event.Name != "Agent" {
			continue
		}
		var args struct {
			Prompt json.RawMessage `json:"prompt"`
		}
		if json.Unmarshal(r.Event.Args, &args) == nil {
			prompts[r.Event.ToolID] = contentText(args.Prompt)
		}
	}
	out := []subagent{}
	indices := map[string]int{}
	for _, line := range lines {
		var s subagent
		if json.Unmarshal(line, &s) != nil {
			continue
		}
		switch s.Type {
		case "subagent.spawned":
			s.Prompt = prompts[s.ToolID]
			indices[s.ID] = len(out)
			out = append(out, s)
		case "subagent.completed", "subagent.cancelled", "subagent.failed":
			if i, ok := indices[s.ID]; ok {
				out[i].Done = true
				out[i].Response = s.Response
			}
		}
	}
	return out
}
func hasForegroundSubagent(subs []subagent) bool {
	for _, s := range subs {
		if !s.Done && !s.Background && s.Parent == "main" {
			return true
		}
	}
	return false
}

type subagentQuery struct {
	Name, ID, ToolID, Prompt, Response string
	Done                               bool
}

func findSubagent(subs []subagent, q subagentQuery) (subagent, int) {
	var found subagent
	count := 0
	for _, s := range subs {
		if q.Name != "" && s.Name != q.Name || q.ID != "" && s.ID != q.ID || q.ToolID != "" && s.ToolID != q.ToolID || q.Prompt != "" && s.Prompt != q.Prompt || q.Response != "" && s.Response != q.Response {
			continue
		}
		if q.Done && !s.Done {
			continue
		}
		if q.ID == "" && q.ToolID == "" && q.Prompt == "" && q.Response == "" && s.Done != q.Done {
			continue
		}
		found = s
		count++
	}
	return found, count
}

func mainReadyForStop(ref string) bool {
	b, _ := os.ReadFile(ref)
	rs, _, _ := records(b, 0)
	ready := false
	for _, r := range rs {
		if r.Type == "turn.prompt" || r.Type == "turn.ended" {
			ready = false
		}
		if r.Type == wireLoopEvent {
			if r.Event.Type == "step.begin" {
				ready = false
			}
			if r.Event.Type == "step.end" {
				ready = r.Event.FinishReason == "end_turn" || r.Event.FinishReason == "stop"
			}
		}
	}
	return ready
}
func copySubagents(native, ref, id string) error {
	sourceDir := filepath.Dir(filepath.Dir(native))
	root, _ := canonical(sourceDir)
	entries, e := os.ReadDir(sourceDir)
	if e != nil {
		return e
	}
	for _, ent := range entries {
		if !ent.IsDir() || len(ent.Name()) < 6 || ent.Name()[:6] != "agent-" {
			continue
		}
		path := filepath.Join(sourceDir, ent.Name(), "wire.jsonl")
		actual, e := canonical(path)
		if e != nil || !within(root, actual) {
			continue
		}
		b, e := os.ReadFile(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		dest := filepath.Join(filepath.Dir(ref), id, "subagents", "agent-"+ent.Name()+".jsonl")
		if e = managedPath(dest); e != nil {
			return e
		}
		if e = atomicWrite(dest, completePrefix(b)); e != nil {
			return e
		}
	}
	return nil
}
func childTranscripts(dir string) ([][]byte, error) {
	// The subagents directory is optional and located by convention from the
	// transcript path. A path that is missing, or that resolves under a
	// non-directory (e.g. a transcript whose name is not ".jsonl"), simply has
	// no children; ReadDir alone reports that as ENOTDIR on Unix but not on
	// Windows, so stat the path to keep the behavior identical on both.
	info, e := os.Stat(dir)
	if os.IsNotExist(e) || errors.Is(e, syscall.ENOTDIR) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if !info.IsDir() {
		return nil, nil
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, e
	}
	root, _ := canonical(dir)
	out := [][]byte{}
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".jsonl" {
			continue
		}
		p := filepath.Join(dir, ent.Name())
		actual, e := canonical(p)
		if e != nil || !within(root, actual) {
			continue
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, nil
}
