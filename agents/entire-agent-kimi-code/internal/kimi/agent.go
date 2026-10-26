// Package kimi integrates Kimi's native wire log without modifying Kimi state.
package kimi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

type Agent struct{}

func New() *Agent { return &Agent{} }

var hookRules = []struct {
	Event, Name string
	Type        int
}{
	{"SessionStart", "session-start", 1}, {"TurnStarted", "turn-started", 2},
	{"Stop", "stop", 3}, {"StopFailure", "stop-failure", 3}, {"Interrupt", "interrupt", 3},
	{"PreCompact", "pre-compact", 4}, {"SessionEnd", "session-end", 5},
	{"SubagentStart", "subagent-start", 6}, {"SubagentStop", "subagent-stop", 7},
}

func (a *Agent) Info() protocol.InfoResponse {
	names := []string{}
	for _, h := range hookRules {
		names = append(names, h.Name)
	}
	return protocol.InfoResponse{ProtocolVersion: 1, Name: "kimi-code", Type: "Kimi Code", Description: "Kimi Code native lifecycle and wire transcript integration", IsPreview: true, ProtectedDirs: []string{".kimi-code"}, HookNames: names, Capabilities: protocol.DeclaredCapabilities{Hooks: true, TranscriptAnalyzer: true, CompactTranscript: true, TokenCalculator: true, SubagentAwareExtractor: true, UsesTerminal: true}}
}
func (a *Agent) Detect() protocol.DetectResponse {
	_, e := exec.LookPath("kimi")
	return protocol.DetectResponse{Present: e == nil}
}
func (a *Agent) GetSessionID(in *protocol.HookInputJSON) string {
	if in == nil {
		return ""
	}
	return in.SessionID
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func validID(id string) bool {
	return safeID.MatchString(id) && id != "." && id != ".." && len(id) < 240
}
func (a *Agent) FormatResumeCommand(id string) string {
	if !validID(id) {
		return ""
	}
	return "kimi --session " + id
}
func kimiHome() string {
	if p := os.Getenv("KIMI_CODE_HOME"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".kimi-code")
}
func (a *Agent) GetSessionDir(repo string) (string, error) {
	if repo == "" {
		repo = protocol.RepoRoot()
	}
	p, e := filepath.Abs(filepath.Join(repo, ".entire", "metadata", "kimi-code"))
	return p, e
}
func (a *Agent) ResolveSessionFile(dir, id string) string {
	if !validID(id) {
		return ""
	}
	return filepath.Join(dir, id+".jsonl")
}
func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func canonical(p string) (string, error) {
	p, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	if s, e := filepath.EvalSymlinks(p); e == nil {
		return s, nil
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, nil
	}
	resolved, e := canonical(parent)
	if e != nil {
		return "", e
	}
	return filepath.Join(resolved, filepath.Base(p)), nil
}
func samePath(a, b string) bool {
	a, _ = canonical(a)
	b, _ = canonical(b)
	return filepath.Clean(a) == filepath.Clean(b) || strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) && os.PathSeparator == '\\'
}

// Native resolution trusts only index records belonging to this repository and
// contained inside Kimi's session root, including after symlink resolution.
func nativeSession(id, repo string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid session id")
	}
	root, _ := canonical(filepath.Join(kimiHome(), "sessions"))
	data, e := os.ReadFile(filepath.Join(kimiHome(), "session_index.jsonl"))
	if e != nil {
		return "", fmt.Errorf("read Kimi session index: %w", e)
	}
	var found string
	var foreign, escapes bool
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var r struct {
			ID   string `json:"sessionId"`
			Dir  string `json:"sessionDir"`
			Work string `json:"workDir"`
		}
		if json.Unmarshal(line, &r) != nil || r.ID != id {
			continue
		}
		if !samePath(repo, r.Work) {
			foreign = true
			continue
		}
		p, e := canonical(filepath.Join(r.Dir, "agents", "main", "wire.jsonl"))
		if e != nil || !within(root, p) || filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p)))) != id {
			escapes = true
			continue
		}
		found = p
	}
	if found != "" {
		return found, nil
	}
	if foreign {
		return "", errors.New("kimi session belongs to another repository")
	}
	if escapes {
		return "", errors.New("kimi session index escapes session store")
	}
	return "", errors.New("session not indexed by Kimi yet")
}
func atomicWrite(path string, data []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".entire-write-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0o600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, path)
}
func managedPath(ref string) error {
	dir, _ := New().GetSessionDir(protocol.RepoRoot())
	p, _ := canonical(ref)
	root, _ := canonical(dir)
	repo, _ := canonical(protocol.RepoRoot())
	if !within(repo, root) || !within(root, p) || filepath.Ext(p) != ".jsonl" {
		return errors.New("session write outside Entire-owned store")
	}
	return nil
}
func snapshot(id, repo string) (string, error) {
	source, e := nativeSession(id, repo)
	if e != nil {
		return "", e
	}
	data, e := os.ReadFile(source)
	if e != nil {
		return "", e
	}
	data = completePrefix(data)
	dir, _ := New().GetSessionDir(repo)
	ref := New().ResolveSessionFile(dir, id)
	if e = managedPath(ref); e != nil {
		return "", e
	}
	if e = atomicWrite(ref, data); e != nil {
		return "", e
	}
	_ = os.Remove(ref + ".session.json")
	if e = copySubagents(source, ref, id); e != nil {
		return "", e
	}
	return ref, nil
}

// Publish a consistent prefix. A concurrently appended partial record is
// deferred until the next snapshot, so Entire never advances past it.
func completePrefix(data []byte) []byte {
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return data[:0]
	}
	return data[:end+1]
}
func (a *Agent) ReadSession(in *protocol.HookInputJSON) (protocol.AgentSessionJSON, error) {
	if in == nil || !validID(in.SessionID) {
		return protocol.AgentSessionJSON{}, errors.New("valid session_id required")
	}
	ref := in.SessionRef
	if ref == "" {
		dir, _ := a.GetSessionDir(protocol.RepoRoot())
		ref = a.ResolveSessionFile(dir, in.SessionID)
	}
	if e := managedPath(ref); e != nil {
		return protocol.AgentSessionJSON{}, e
	}
	// Restored sessions are Entire-owned, never passed off as native Kimi state.
	if b, e := os.ReadFile(ref + ".session.json"); e == nil {
		var s protocol.AgentSessionJSON
		if json.Unmarshal(b, &s) == nil {
			return s, nil
		}
	}
	data, e := os.ReadFile(ref)
	if e != nil {
		return protocol.AgentSessionJSON{}, e
	}
	files, _, e := extractFiles(data, 0)
	if e != nil {
		return protocol.AgentSessionJSON{}, e
	}
	return protocol.AgentSessionJSON{SessionID: in.SessionID, AgentName: "kimi-code", RepoPath: protocol.RepoRoot(), SessionRef: ref, StartTime: in.Timestamp, NativeData: data, ModifiedFiles: files, NewFiles: []string{}, DeletedFiles: []string{}}, nil
}
func (a *Agent) WriteSession(s protocol.AgentSessionJSON) error {
	if !validID(s.SessionID) {
		return errors.New("invalid session id")
	}
	if e := managedPath(s.SessionRef); e != nil {
		return e
	}
	if e := atomicWrite(s.SessionRef, s.NativeData); e != nil {
		return e
	}
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	return atomicWrite(s.SessionRef+".session.json", b)
}
func (a *Agent) ReadTranscript(ref string) ([]byte, error) {
	i, e := os.Stat(ref)
	if e != nil {
		return nil, e
	}
	if !i.Mode().IsRegular() {
		return nil, errors.New("transcript must be a regular file")
	}
	return os.ReadFile(ref)
}
func (a *Agent) ChunkTranscript(b []byte, n int) ([][]byte, error) {
	if n <= 0 {
		return nil, errors.New("max-size must be positive")
	}
	out := [][]byte{}
	for len(b) > 0 {
		size := min(n, len(b))
		out = append(out, append([]byte{}, b[:size]...))
		b = b[size:]
	}
	return out, nil
}
func (a *Agent) ReassembleTranscript(chunks [][]byte) ([]byte, error) {
	return bytes.Join(chunks, nil), nil
}
func (a *Agent) ParseHook(name string, b []byte) (*protocol.EventJSON, error) {
	if name == "pre-compact" {
		// Kimi compacts model context by appending full_compaction records.
		// Its wire log is not rewritten. Entire's Compaction event resets raw
		// byte cursors, which would replay prompts and count old usage twice.
		fmt.Fprintln(os.Stderr, "kimi-code: compaction leaves wire.jsonl append-only; retaining Entire byte cursors")
		return nil, nil
	}
	if name == "user-prompt-submit" {
		// Unmanaged: TurnStarted is the only managed turn boundary, covering
		// user, task and system origins. This hook stays a diagnostic no-op.
		fmt.Fprintln(os.Stderr, "kimi-code: user-prompt-submit is unmanaged; turn boundaries come from turn-started")
		return nil, nil
	}
	kind := 0
	for _, h := range hookRules {
		if h.Name == name {
			kind = h.Type
		}
	}
	if kind == 0 {
		return nil, nil
	}
	var p hookPayload
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	if e := json.Unmarshal(b, &p); e != nil {
		return nil, e
	}
	if !validID(p.ID) || p.Cwd == "" {
		return nil, nil
	}
	if !samePath(p.Cwd, protocol.RepoRoot()) {
		return nil, errors.New("hook cwd does not match repository root")
	}
	if name == "turn-started" && isSubagentTurnOrigin(p) {
		return nil, nil
	}
	ref, e := snapshot(p.ID, p.Cwd)
	if e != nil {
		return nil, e
	}
	subs := subagentRecords(ref)
	if name == "stop" && len(subs) > 0 && !mainReadyForStop(ref) {
		return nil, nil
	}
	if kind == 3 && name != "stop" && hasForegroundSubagent(subs) {
		return nil, nil
	}
	ev := &protocol.EventJSON{Type: kind, SessionID: p.ID, SessionRef: ref, Prompt: contentText(p.Prompt), Model: p.Model, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), SubagentID: p.SubID, SubagentType: p.AgentType, TaskDescription: p.Description, ToolUseID: p.ToolID, Metadata: map[string]string{"hook": name}}
	if ev.SubagentID == "" {
		ev.SubagentID = p.AgentID
	}
	if ev.SubagentType == "" {
		ev.SubagentType = p.Name
	}
	if kind == 6 || kind == 7 {
		query := subagentQuery{Name: p.Name, ID: ev.SubagentID, ToolID: p.ToolID, Done: kind == 7}
		if kind == 6 {
			query.Prompt = contentText(p.Prompt)
		} else {
			query.Response = p.Response
		}
		sub, matches := findSubagent(subs, query)
		if matches > 1 {
			return nil, errors.New("cannot uniquely correlate kimi subagent hook; refusing to assign another child's identity")
		}
		if matches == 0 {
			return nil, nil
		}
		ev.SubagentID = sub.ID
		ev.ToolUseID = sub.ToolID
		ev.SubagentType = sub.Name
		ev.TaskDescription = sub.Description
	}
	if p.Reason != "" {
		ev.Metadata["reason"] = p.Reason
	}
	if p.ErrorType != "" {
		ev.Metadata["error_type"] = p.ErrorType
	}
	if p.ErrorMessage != "" {
		ev.Metadata["error_message"] = p.ErrorMessage
	}
	if name == "stop-failure" || name == "interrupt" {
		ev.Metadata["outcome"] = name
	}
	if kind == 1 {
		source, _ := nativeSession(p.ID, p.Cwd)
		if state, e := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(source))), "state.json")); e == nil {
			var s struct {
				ForkedFrom string `json:"forkedFrom"`
			}
			if json.Unmarshal(state, &s) == nil {
				ev.PreviousSessionID = s.ForkedFrom
			}
		}
	}
	return ev, nil
}

// turnOrigin mirrors the origin markers a TurnStarted payload carries. Kimi
// has captured the subagent marker as a flat "origin_name", as
// "origin_kind" (the main user turn reports "user"), and as a nested
// "origin" object whose own field spelling has not been pinned by a capture,
// so every alias below is inspected.
type turnOrigin struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	OriginName string `json:"origin_name"`
	OriginKind string `json:"origin_kind"`
}

type hookPayload struct {
	ID           string          `json:"session_id"`
	Cwd          string          `json:"cwd"`
	Prompt       json.RawMessage `json:"prompt"`
	Model        string          `json:"model"`
	Reason       string          `json:"reason"`
	SubID        string          `json:"subagent_id"`
	AgentID      string          `json:"agent_id"`
	AgentType    string          `json:"subagent_type"`
	Name         string          `json:"agent_name"`
	Description  string          `json:"description"`
	ToolID       string          `json:"tool_use_id"`
	OriginName   string          `json:"origin_name"`
	OriginKind   string          `json:"origin_kind"`
	Origin       turnOrigin      `json:"origin"`
	ErrorType    string          `json:"error_type"`
	ErrorMessage string          `json:"error_message"`
	Response     string          `json:"response"`
}

// isSubagentTurnOrigin suppresses child TurnStarted hooks. Only an explicit
// "subagent" marker suppresses: user, task and system turns (which report
// "user", a task kind, or no origin at all) must keep opening the turn.
func isSubagentTurnOrigin(p hookPayload) bool {
	for _, v := range []string{p.OriginName, p.OriginKind, p.Origin.Name, p.Origin.Kind, p.Origin.OriginName, p.Origin.OriginKind} {
		if strings.EqualFold(v, "subagent") {
			return true
		}
	}
	return false
}
