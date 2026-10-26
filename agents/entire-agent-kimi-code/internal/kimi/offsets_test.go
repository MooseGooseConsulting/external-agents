package kimi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionLocalLineBridgeCountsOnlyNewCalls(t *testing.T) {
	setup(t)
	put(t, runtimeConfigPath(), []byte(`{"entire_version":"Entire CLI 0.12.1","offset_unit":"lines"}`))
	first := []byte("{\"type\":\"usage.record\",\"usage\":{\"inputOther\":10,\"output\":1}}\n")
	second := []byte("{\"type\":\"usage.record\",\"usage\":{\"inputOther\":20,\"output\":2}}\n")
	data := append(append([]byte{}, first...), second...)
	path := filepath.Join(t.TempDir(), "wire.jsonl")
	put(t, path, data)
	position, e := New().GetTranscriptPosition(path)
	if e != nil || position != 2 {
		t.Fatalf("line cursor %d %v", position, e)
	}
	var out bytes.Buffer
	if e = New().HandleAggregate("calculate-tokens", []string{"--offset", "1"}, bytes.NewReader(data), &out); e != nil {
		t.Fatal(e)
	}
	var got tokens
	if e = json.Unmarshal(out.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if got.Input != 20 || got.Output != 2 || got.Calls != 1 {
		t.Fatal("line cursor replayed prior usage", out.String())
	}
	if e = os.Remove(runtimeConfigPath()); e != nil {
		t.Fatal(e)
	}
	position, e = New().GetTranscriptPosition(path)
	if e != nil || position != len(data) {
		t.Fatalf("default byte cursor %d %v", position, e)
	}
	if inputByteOffset(data, len(first)) != len(first) {
		t.Fatal("standalone protocol lost byte offset semantics")
	}
}
