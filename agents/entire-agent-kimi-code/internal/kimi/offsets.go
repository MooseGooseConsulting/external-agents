package kimi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

// Entire 0.12.1's condensation path counts JSONL records, despite the external
// protocol documenting bytes. Keep this explicit and repository-local rather
// than guessing whether an arbitrary numeric cursor is a line or byte offset.
type runtimeCompatibility struct {
	EntireVersion string `json:"entire_version"`
	OffsetUnit    string `json:"offset_unit"`
}

func runtimeConfigPath() string {
	dir, _ := New().GetSessionDir(protocol.RepoRoot())
	return filepath.Join(dir, "runtime.json")
}
func configureRuntime() error {
	if mode := os.Getenv("ENTIRE_KIMI_OFFSET_UNIT"); mode != "" {
		if mode != "bytes" && mode != "lines" {
			return fmt.Errorf("invalid ENTIRE_KIMI_OFFSET_UNIT %q", mode)
		}
		b, e := json.Marshal(runtimeCompatibility{EntireVersion: "explicit override", OffsetUnit: mode})
		if e != nil {
			return e
		}
		return atomicWrite(runtimeConfigPath(), append(b, '\n'))
	}
	bin, _ := exec.LookPath("entire")
	if bin == "" {
		return nil
	}
	out, e := exec.Command(bin, "version").Output()
	if e != nil {
		return fmt.Errorf("probe Entire offset compatibility: %w", e)
	}
	version := strings.SplitN(string(out), "\n", 2)[0]
	mode := "bytes"
	if strings.TrimSpace(version) == "Entire CLI 0.12.1" {
		mode = "lines"
	}
	b, e := json.Marshal(runtimeCompatibility{EntireVersion: strings.TrimSpace(version), OffsetUnit: mode})
	if e != nil {
		return e
	}
	return atomicWrite(runtimeConfigPath(), append(b, '\n'))
}
func usesLineOffsets() bool {
	b, e := os.ReadFile(runtimeConfigPath())
	if e != nil {
		return false
	}
	var cfg runtimeCompatibility
	return json.Unmarshal(b, &cfg) == nil && cfg.OffsetUnit == "lines"
}
func inputByteOffset(b []byte, offset int) int {
	if !usesLineOffsets() || offset < 0 {
		return offset
	}
	pos := 0
	for range offset {
		n := bytes.IndexByte(b[pos:], '\n')
		if n < 0 {
			return len(b)
		}
		pos += n + 1
	}
	return pos
}
func outputOffset(b []byte, pos int) int {
	if usesLineOffsets() {
		return bytes.Count(b[:pos], []byte{'\n'})
	}
	return pos
}

// Child transcripts retained under the managed store grow append-only, but
// Entire tracks a read cursor for the parent transcript alone. Persist each
// child's counted byte position beside it so a later calculate-total-tokens
// pass counts only usage recorded since the previous pass — the parent suffix
// treatment, per child.
const childOffsetsFile = "read-offsets.json"

func readChildOffsets(dir string) map[string]int {
	b, e := os.ReadFile(filepath.Join(dir, childOffsetsFile))
	if e != nil {
		return map[string]int{}
	}
	var m map[string]int
	if json.Unmarshal(b, &m) != nil {
		return map[string]int{}
	}
	for name, offset := range m {
		if offset < 0 {
			delete(m, name)
		}
	}
	return m
}
func writeChildOffsets(dir string, offsets map[string]int) error {
	b, e := json.Marshal(offsets)
	if e != nil {
		return e
	}
	return atomicWrite(filepath.Join(dir, childOffsetsFile), b)
}
