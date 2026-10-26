package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

// Kimi can overlap observational hooks. Hold the lock for the complete Entire
// process, not just parse-hook: lifecycle state is written after parsing returns.
func DispatchHook(name string, in io.Reader, out, diagnostics io.Writer) error {
	known := false
	for _, rule := range hookRules {
		known = known || rule.Name == name
	}
	if !known {
		return errors.New("unknown Kimi hook")
	}
	data, err := io.ReadAll(io.LimitReader(in, 4<<20))
	if err != nil {
		return err
	}
	var payload struct {
		ID  string `json:"session_id"`
		Cwd string `json:"cwd"`
	}
	if err = json.Unmarshal(data, &payload); err != nil {
		return err
	}
	if !validID(payload.ID) || payload.Cwd == "" || !samePath(payload.Cwd, protocol.RepoRoot()) {
		return errors.New("invalid hook session or repository")
	}
	// Preserve Entire's fast no-op outside enabled repositories. In particular,
	// do not create an untracked .entire directory just because global hooks run.
	if _, err = os.Stat(filepath.Join(payload.Cwd, ".entire")); os.IsNotExist(err) {
		return nil
	}
	dir, err := New().GetSessionDir(payload.Cwd)
	if err != nil {
		return err
	}
	if err = managedPath(filepath.Join(dir, payload.ID+".jsonl")); err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	unlock, err := acquireHookLock(ctx, filepath.Join(dir, payload.ID+".hook-lock"))
	if err != nil {
		return err
	}
	defer unlock()
	cmd := exec.CommandContext(ctx, "entire", "hooks", "kimi-code", name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(data), out, diagnostics
	return cmd.Run()
}

func acquireHookLock(ctx context.Context, path string) (func(), error) {
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("kimi hook lock busy (a crashed owner may require removing %s): %w", path, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
