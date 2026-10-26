package kimi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

func indexLine(t *testing.T, id, dir, work string) []byte {
	t.Helper()
	b, e := json.Marshal(map[string]string{"sessionId": id, "sessionDir": dir, "workDir": work})
	if e != nil {
		t.Fatal(e)
	}
	return append(b, '\n')
}

func sessionTree(t *testing.T, home, id, content string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "wd", id)
	put(t, filepath.Join(dir, "agents", "main", "wire.jsonl"), []byte(content))
	return dir
}

// sessionTreeAt builds a second tree for the same id under a distinct parent,
// since Kimi requires the session directory itself to be named for the id.
func sessionTreeAt(t *testing.T, home, parent, id, content string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", parent, id)
	put(t, filepath.Join(dir, "agents", "main", "wire.jsonl"), []byte(content))
	return dir
}

func TestSessionIDsAndResumeCommand(t *testing.T) {
	a := New()
	for _, id := range []string{"", ".", "..", ".hidden", "--auto", "-S", "sess id", "sess\tid", "x\x00y", strings.Repeat("a", 240)} {
		if validID(id) {
			t.Errorf("invalid session id accepted %q", id)
		}
		if got := a.FormatResumeCommand(id); got != "" {
			t.Errorf("unsafe id produced resume command %q -> %q", id, got)
		}
		if got := a.ResolveSessionFile(t.TempDir(), id); got != "" {
			t.Errorf("unsafe id resolved to file %q", got)
		}
	}
	for _, id := range []string{"session_test", "s.ession_1-x", strings.Repeat("a", 239)} {
		if !validID(id) {
			t.Errorf("valid session id rejected %q", id)
			continue
		}
		if got := a.FormatResumeCommand(id); got != "kimi --session "+id {
			t.Errorf("resume command %q", got)
		}
	}
	if got := a.GetSessionID(nil); got != "" {
		t.Errorf("nil hook input returned %q", got)
	}
	if got := a.GetSessionID(&protocol.HookInputJSON{SessionID: "session_test"}); got != "session_test" {
		t.Errorf("session id not extracted: %q", got)
	}
}

func TestGetSessionDirAndResolveAgreement(t *testing.T) {
	repo, _ := setup(t)
	a := New()
	dir, e := a.GetSessionDir(repo)
	if e != nil {
		t.Fatal(e)
	}
	if want := filepath.Join(repo, ".entire", "metadata", "kimi-code"); dir != want {
		t.Fatalf("session dir %q, want %q", dir, want)
	}
	ref := a.ResolveSessionFile(dir, "session_test")
	if ref != filepath.Join(dir, "session_test.jsonl") {
		t.Fatalf("session file %q", ref)
	}
	var dirOut, fileOut bytes.Buffer
	if e = protocol.HandleGetSessionDir(nil, &dirOut, a); e != nil {
		t.Fatal(e)
	}
	if e = protocol.HandleResolveSessionFile([]string{"--session-dir", dir, "--session-id", "session_test"}, &fileOut, a); e != nil {
		t.Fatal(e)
	}
	var dirResp protocol.SessionDirResponse
	var fileResp protocol.SessionFileResponse
	if e = json.Unmarshal(dirOut.Bytes(), &dirResp); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(fileOut.Bytes(), &fileResp); e != nil {
		t.Fatal(e)
	}
	if dirResp.SessionDir != dir || fileResp.SessionFile != ref {
		t.Fatalf("CLI handlers disagree with direct resolution: %+v %+v", dirResp, fileResp)
	}
}

func TestSessionIndexMalformedAndMultipleRecords(t *testing.T) {
	repo, home := setup(t)
	first := sessionTree(t, home, "session_multi", "{\"type\":\"first\"}\n")
	second := sessionTreeAt(t, home, "wd2", "session_multi", "{\"type\":\"second\"}\n")
	var idx bytes.Buffer
	idx.WriteString("not json\n")
	idx.WriteString("\n")
	idx.WriteString("{\"workDir\":\"other\"}\n")
	idx.Write(indexLine(t, "other_session", first, repo))
	idx.Write(indexLine(t, "session_multi", first, repo))
	idx.Write(indexLine(t, "session_multi", second, repo))
	put(t, filepath.Join(home, "session_index.jsonl"), idx.Bytes())
	p, e := nativeSession("session_multi", repo)
	if e != nil {
		t.Fatal(e)
	}
	want, e := os.ReadFile(filepath.Join(second, "agents", "main", "wire.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(p)
	if e != nil || !bytes.Equal(got, want) {
		t.Fatalf("duplicate records resolved to the wrong snapshot: %v", e)
	}
	if _, e = nativeSession("missing_session", repo); e == nil {
		t.Fatal("unindexed session resolved")
	}
	_, other := setup(t)
	if _, e = nativeSession("session_multi", other); e == nil {
		t.Fatal("resolved session without a session index")
	}
}

// TestSessionIndexSkipsInvalidRecordsForSameID covers regression: an earlier
// record for the target id that is foreign, malformed, or escapes the session
// store must not abort resolution when a later valid record for the same id
// exists. Resolution keeps the last valid record and errors only when no
// valid record remains.
func TestSessionIndexSkipsInvalidRecordsForSameID(t *testing.T) {
	repo, home := setup(t)
	id := "session_skip"
	valid := sessionTree(t, home, id, "{\"type\":\"valid\"}\n")
	otherRepo := t.TempDir()
	var idx bytes.Buffer
	idx.Write(indexLine(t, id, valid, otherRepo))
	idx.Write(indexLine(t, id, valid, repo))
	put(t, filepath.Join(home, "session_index.jsonl"), idx.Bytes())
	p, e := nativeSession(id, repo)
	if e != nil {
		t.Fatalf("foreign record blocked later valid record: %v", e)
	}
	if !samePath(p, filepath.Join(valid, "agents", "main", "wire.jsonl")) {
		t.Fatalf("resolved %q", p)
	}

	// Invalid records before, between, and after a valid record: the valid
	// record must still resolve and later invalid ones must not erase it.
	idx.Reset()
	idx.WriteString("not json\n")
	// Escaping session dir pointing outside the store.
	idx.Write(indexLine(t, id, filepath.Join(home, "escape", id), repo))
	// Session dir named for another id inside the store.
	idx.Write(indexLine(t, id, filepath.Join(home, "sessions", "wd", "not-the-id"), repo))
	idx.Write(indexLine(t, id, valid, repo))
	// Valid earlier, foreign later must not clear the found record either.
	idx.Write(indexLine(t, id, valid, otherRepo))
	put(t, filepath.Join(home, "session_index.jsonl"), idx.Bytes())
	p, e = nativeSession(id, repo)
	if e != nil {
		t.Fatalf("invalid records aborted resolution: %v", e)
	}
	if !samePath(p, filepath.Join(valid, "agents", "main", "wire.jsonl")) {
		t.Fatalf("resolved %q", p)
	}

	// With no valid record remaining, resolution must fail rather than fall
	// through: foreign only.
	idx.Reset()
	idx.Write(indexLine(t, id, valid, otherRepo))
	put(t, filepath.Join(home, "session_index.jsonl"), idx.Bytes())
	if _, e = nativeSession(id, repo); e == nil {
		t.Fatal("resolved a session with only a foreign workDir record")
	}
	// Escaping only.
	idx.Reset()
	idx.Write(indexLine(t, id, filepath.Join(home, "escape", id), repo))
	put(t, filepath.Join(home, "session_index.jsonl"), idx.Bytes())
	if _, e = nativeSession(id, repo); e == nil {
		t.Fatal("resolved a session with only an escaping record")
	}
	// Foreign first, escaping second: still no valid record remains.
	idx.Reset()
	idx.Write(indexLine(t, id, valid, otherRepo))
	idx.Write(indexLine(t, id, filepath.Join(home, "escape", id), repo))
	put(t, filepath.Join(home, "session_index.jsonl"), idx.Bytes())
	if _, e = nativeSession(id, repo); e == nil {
		t.Fatal("resolved a session with no valid record")
	}
}

func TestSessionIndexRejectsEscapingSessionDir(t *testing.T) {
	repo, home := setup(t)
	id := "session_escape"
	// A valid record whose sessionDir leaves Kimi's session store entirely.
	dir := filepath.Join(home, "escape", id)
	put(t, filepath.Join(dir, "agents", "main", "wire.jsonl"), []byte("x\n"))
	put(t, filepath.Join(home, "session_index.jsonl"), indexLine(t, id, dir, repo))
	if _, e := nativeSession(id, repo); e == nil {
		t.Fatal("accepted session dir outside the Kimi session store")
	}
	// A record inside the store whose session directory is not the session id.
	misnamed := filepath.Join(home, "sessions", "wd", "not-the-id")
	put(t, filepath.Join(misnamed, "agents", "main", "wire.jsonl"), []byte("x\n"))
	put(t, filepath.Join(home, "session_index.jsonl"), indexLine(t, id, misnamed, repo))
	if _, e := nativeSession(id, repo); e == nil {
		t.Fatal("accepted session dir named for another session id")
	}
}

func TestNonCanonicalRepoPathResolves(t *testing.T) {
	repo, home := setup(t)
	dir := sessionTree(t, home, "session_canon", "x\n")
	put(t, filepath.Join(home, "session_index.jsonl"), indexLine(t, "session_canon", dir, repo))
	variants := []string{
		filepath.Join(repo, "."),
		filepath.Join(repo, "sub", ".."),
		repo + string(filepath.Separator),
	}
	if runtime.GOOS == windowsOS {
		base := filepath.Base(repo)
		if upper := strings.ToUpper(base); upper != base && filepath.Base(repo) != upper {
			variants = append(variants, filepath.Join(filepath.Dir(repo), upper))
		}
	}
	for _, v := range variants {
		p, e := nativeSession("session_canon", v)
		if e != nil {
			t.Fatalf("repo variant %q: %v", v, e)
		}
		if !samePath(p, filepath.Join(dir, "agents", "main", "wire.jsonl")) {
			t.Fatalf("variant %q resolved to %q", v, p)
		}
	}
}

func TestManagedPathConfinement(t *testing.T) {
	repo, _ := setup(t)
	a := New()
	dir, _ := a.GetSessionDir(repo)
	for _, p := range []string{
		filepath.Join(dir, "notes.txt"),
		filepath.Join(repo, "elsewhere", "session_test.jsonl"),
		filepath.Join(repo, "session_test.jsonl"),
		filepath.Join(dir, "sub", "..", "..", "..", "escape.jsonl"),
	} {
		if managedPath(p) == nil {
			t.Errorf("accepted managed path %q", p)
		}
	}
	if e := managedPath(filepath.Join(dir, "session_test.jsonl")); e != nil {
		t.Fatalf("rejected a legitimately managed path: %v", e)
	}
	if e := a.WriteSession(protocol.AgentSessionJSON{SessionID: "session_test", SessionRef: ""}); e == nil {
		t.Fatal("accepted empty session ref")
	}
}

func TestReadSessionRestorationAndFallback(t *testing.T) {
	repo, _ := setup(t)
	a := New()
	dir, _ := a.GetSessionDir(repo)
	ref := a.ResolveSessionFile(dir, "session_test")
	native := []byte("{\"type\":\"turn.prompt\",\"input\":[{\"type\":\"text\",\"text\":\"hello\"}]}\n")
	restored := protocol.AgentSessionJSON{
		SessionID: "session_test", AgentName: "kimi-code", RepoPath: repo,
		SessionRef: ref, StartTime: "2026-01-01T00:00:00Z",
		NativeData: []byte("restored-bytes"), ModifiedFiles: []string{"a.txt"},
	}
	if e := a.WriteSession(restored); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(ref); e != nil {
		t.Fatal("write-session did not store native data")
	}
	if _, e := os.Stat(ref + ".session.json"); e != nil {
		t.Fatal("write-session did not store restoration data")
	}
	s, e := a.ReadSession(&protocol.HookInputJSON{SessionID: "session_test", SessionRef: ref})
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(s.NativeData, restored.NativeData) || s.ModifiedFiles[0] != "a.txt" || s.SessionRef != ref {
		t.Fatalf("restored session mismatched: %+v", s)
	}
	// Reading with no explicit ref must resolve the same restoration record.
	s2, e := a.ReadSession(&protocol.HookInputJSON{SessionID: "session_test"})
	if e != nil || !bytes.Equal(s2.NativeData, restored.NativeData) {
		t.Fatalf("default ref read: %v %+v", e, s2)
	}
	// The restoration record wins over newer raw snapshot content.
	put(t, ref, native)
	s3, e := a.ReadSession(&protocol.HookInputJSON{SessionID: "session_test", SessionRef: ref})
	if e != nil || !bytes.Equal(s3.NativeData, restored.NativeData) {
		t.Fatalf("restoration record was ignored: %v", e)
	}
	// A corrupt restoration record falls back to parsing the raw snapshot.
	put(t, ref+".session.json", []byte("{corrupt"))
	s4, e := a.ReadSession(&protocol.HookInputJSON{SessionID: "session_test", SessionRef: ref})
	if e != nil || !bytes.Equal(s4.NativeData, native) {
		t.Fatalf("fallback parse failed: %v", e)
	}
	if _, e = a.ReadSession(nil); e == nil {
		t.Fatal("accepted nil hook input")
	}
	if _, e = a.ReadSession(&protocol.HookInputJSON{SessionID: "../bad"}); e == nil {
		t.Fatal("accepted invalid session id")
	}
	if _, e = a.ReadSession(&protocol.HookInputJSON{SessionID: "session_test", SessionRef: filepath.Join(t.TempDir(), "x.jsonl")}); e == nil {
		t.Fatal("read a transcript outside the Entire store")
	}
}

func TestChunkTranscriptEdges(t *testing.T) {
	a := New()
	chunks, e := a.ChunkTranscript(nil, 4)
	if e != nil || len(chunks) != 0 {
		t.Fatalf("empty content: %v %d", e, len(chunks))
	}
	for _, empty := range [][][]byte{nil, {}} {
		if out, e := a.ReassembleTranscript(empty); e != nil || len(out) != 0 {
			t.Fatalf("empty reassembly: %v %d", e, len(out))
		}
	}
	data := []byte("abcdefgh")
	one, e := a.ChunkTranscript(data, 1)
	if e != nil || len(one) != len(data) {
		t.Fatalf("1-byte chunks: %v %d", e, len(one))
	}
	for i, c := range one {
		if len(c) != 1 || c[0] != data[i] {
			t.Fatalf("chunk %d = %q", i, c)
		}
	}
	if joined, e := a.ReassembleTranscript(one); e != nil || !bytes.Equal(joined, data) {
		t.Fatal("1-byte chunk roundtrip corrupted data")
	}
	exact, e := a.ChunkTranscript(data, len(data))
	if e != nil || len(exact) != 1 || !bytes.Equal(exact[0], data) {
		t.Fatalf("exact size must yield one chunk: %v %d", e, len(exact))
	}
	if _, e = a.ChunkTranscript(data, -1); e == nil {
		t.Fatal("accepted negative chunk size")
	}
	if out, e := a.ReassembleTranscript([][]byte{{}, []byte("a"), {}, []byte("b")}); e != nil || !bytes.Equal(out, []byte("ab")) {
		t.Fatalf("empty chunks corrupted reassembly: %q", out)
	}
}
