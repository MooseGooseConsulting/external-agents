package kimi

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const installerSentinel = "SENTINEL_KIMI_INSTALLER_SECRET_DO_NOT_DISCLOSE"

func configPath(home string) string { return filepath.Join(home, "config.toml") }
func lockPath(home string) string   { return configPath(home) + ".entire-lock" }
func backupsOf(home string) []string {
	m, _ := filepath.Glob(configPath(home) + ".entire-agent-kimi-code.*.bak")
	sort.Strings(m)
	return m
}

func TestHookBlockDeclaresExactNineSerializedRules(t *testing.T) {
	want := []struct{ event, name string }{
		{"SessionStart", "session-start"}, {"TurnStarted", "turn-started"},
		{"Stop", "stop"}, {"StopFailure", "stop-failure"}, {"Interrupt", "interrupt"},
		{"PreCompact", "pre-compact"}, {"SessionEnd", "session-end"},
		{"SubagentStart", "subagent-start"}, {"SubagentStop", "subagent-stop"},
	}
	block := hookBlock()
	if n := bytes.Count(block, []byte("[[hooks]]")); n != len(want) {
		t.Fatalf("hook block declares %d rules, want %d", n, len(want))
	}
	if !bytes.HasPrefix(block, []byte(beginMarker)) || !bytes.HasSuffix(block, []byte(endMarker)) {
		t.Fatal("managed block markers missing")
	}
	if c := bytes.Count(block, []byte("timeout = 30")); c != len(want) {
		t.Fatalf("timeout lines: %d, want %d", c, len(want))
	}
	s := string(block)
	last := -1
	for _, w := range want {
		line := fmt.Sprintf("event = %q\ncommand = %q\ntimeout = 30", w.event, "entire-agent-kimi-code dispatch-hook "+w.name)
		i := strings.Index(s, line)
		if i < 0 {
			t.Fatalf("missing or malformed rule %s/%s", w.event, w.name)
		}
		if i <= last {
			t.Fatalf("rule %s out of order", w.event)
		}
		last = i
	}
	if c := bytes.Count(block, []byte("turn-started")); c != 1 {
		t.Fatalf("turn-started appears %d times, want exactly once as the turn boundary", c)
	}
}

func TestRemoveBlockRejectsMalformedMarkers(t *testing.T) {
	good := string(hookBlock())
	cases := []struct{ name, data, wantErr string }{
		{"end marker without begin", "x = 1\n" + endMarker, "unmatched Entire hook marker"},
		{"unterminated block", "x = 1\n" + beginMarker, "unterminated Entire hook block"},
		{"duplicate blocks", "x = 1\n" + good + good, "duplicate Entire hook blocks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := removeBlock([]byte(c.data)); err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("removeBlock error = %v, want %q", err, c.wantErr)
			}
			_, home := setup(t)
			body := "api_key = '" + installerSentinel + "'\n" + c.data
			path := configPath(home)
			put(t, path, []byte(body))
			for _, op := range []func() (int, error){func() (int, error) { return updateConfig(true) }, func() (int, error) { return updateConfig(false) }} {
				_, err := op()
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("installer error = %v, want %q", err, c.wantErr)
				}
				if strings.Contains(err.Error(), installerSentinel) {
					t.Fatal("installer error disclosed secret-shaped sentinel")
				}
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, []byte(body)) {
				t.Fatal("failed install/uninstall mutated the config")
			}
			if len(backupsOf(home)) != 0 {
				t.Fatal("failed install/uninstall created a backup")
			}
			if New().AreHooksInstalled() {
				t.Fatal("malformed marker block counted as installed")
			}
		})
	}
}

func TestLockConflictAndStaleLock(t *testing.T) {
	_, home := setup(t)
	path := configPath(home)
	put(t, path, []byte("api_key = '"+installerSentinel+"'\n"))
	put(t, lockPath(home), []byte("held by another installer"))

	if _, err := New().InstallHooks(false, false); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("install with held lock: %v", err)
	} else if strings.Contains(err.Error(), installerSentinel) {
		t.Fatal("lock error disclosed secret-shaped sentinel")
	}
	if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(beginMarker)) {
		t.Fatal("locked install modified the config")
	}
	if err := New().UninstallHooks(); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("uninstall with held lock: %v", err)
	}
	// A lock left behind by a crashed installer blocks every later run until
	// the file is removed; after removal the installer must recover.
	if err := os.Remove(lockPath(home)); err != nil {
		t.Fatal(err)
	}
	n, err := New().InstallHooks(false, false)
	if err != nil || n != len(hookRules) {
		t.Fatalf("install after clearing the lock: %d %v", n, err)
	}
	if _, err := os.Stat(lockPath(home)); !os.IsNotExist(err) {
		t.Fatal("successful install left a stale lock")
	}
}

func TestConcurrentInstallsSerializeThroughLock(t *testing.T) {
	_, home := setup(t)
	put(t, configPath(home), []byte("x = 1\n"))
	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = New().InstallHooks(false, false)
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case strings.Contains(err.Error(), "locked"):
		default:
			t.Fatalf("unexpected error under lock contention: %v", err)
		}
	}
	if wins == 0 {
		t.Fatal("every concurrent install failed on the lock")
	}
	a := New()
	if !a.AreHooksInstalled() {
		t.Fatal("config not installed after concurrent run")
	}
	if b, _ := os.ReadFile(configPath(home)); bytes.Count(b, []byte(beginMarker)) != 1 {
		t.Fatal("concurrent installs duplicated or corrupted the managed block")
	}
}

func TestConcurrentConfigModificationDetected(t *testing.T) {
	_, home := setup(t)
	path := configPath(home)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		flip := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			if flip {
				_ = os.WriteFile(path, []byte("api_key = '"+installerSentinel+"'\nforeign = 'B'\n"), 0600)
			} else {
				_ = os.WriteFile(path, []byte("api_key = '"+installerSentinel+"'\nforeign = 'A'\n"), 0600)
			}
			flip = !flip
			time.Sleep(time.Millisecond)
		}
	}()
	seen := false
	var sawErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !seen {
		_, err := updateConfig(true)
		switch {
		case err == nil:
		case strings.Contains(err.Error(), "concurrently"):
			seen, sawErr = true, err
		default:
			// The racing writer can transiently win filesystem races on
			// Windows (rename over an open file); keep retrying.
		}
	}
	close(stop)
	<-done
	if seen && strings.Contains(sawErr.Error(), installerSentinel) {
		t.Fatal("concurrent-modification error disclosed secret-shaped sentinel")
	}
	if !seen {
		t.Skip("concurrent-modification window not observed within deadline")
	}
	if _, err := updateConfig(true); err != nil {
		t.Fatalf("installer did not converge after writer stopped: %v", err)
	}
	if !New().AreHooksInstalled() {
		t.Fatal("config not installed after convergence")
	}
}

func TestBackupCountContentAndProtection(t *testing.T) {
	_, home := setup(t)
	path := configPath(home)
	original := []byte("api_key = '" + installerSentinel + "'\n[[hooks]]\nevent = 'Stop'\ncommand = 'echo foreign'\n")
	put(t, path, original)
	a := New()
	if _, err := a.InstallHooks(false, false); err != nil {
		t.Fatal(err)
	}
	bs := backupsOf(home)
	if len(bs) != 1 {
		t.Fatalf("backup count after install: %d, want 1", len(bs))
	}
	if !regexp.MustCompile(`\.entire-agent-kimi-code\.\d{8}T\d{6}\.\d{9}\.bak$`).MatchString(bs[0]) {
		t.Fatalf("unexpected backup name shape: %s", bs[0])
	}
	if b, _ := os.ReadFile(bs[0]); !bytes.Equal(b, original) {
		t.Fatal("backup does not hold the exact pre-install bytes")
	}
	if fi, err := os.Stat(bs[0]); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != windowsOS && fi.Mode().Perm() != 0600 {
		t.Fatalf("backup is world-readable: %v", fi.Mode())
	}
	if runtime.GOOS == windowsOS {
		out, err := exec.Command("icacls", bs[0]).CombinedOutput()
		if err == nil && strings.Contains(string(out), "(I)") {
			t.Fatalf("backup inherits ACL entries: %s", out)
		}
	}
	if _, err := a.InstallHooks(false, false); err != nil {
		t.Fatal(err)
	}
	if len(backupsOf(home)) != 1 {
		t.Fatal("idempotent install created an extra backup")
	}
	installed, _ := os.ReadFile(path)
	if err := a.UninstallHooks(); err != nil {
		t.Fatal(err)
	}
	bs = backupsOf(home)
	if len(bs) != 2 {
		t.Fatalf("backup count after uninstall: %d, want 2", len(bs))
	}
	if b, _ := os.ReadFile(bs[1]); !bytes.Equal(b, installed) {
		t.Fatal("uninstall backup does not hold the exact pre-uninstall bytes")
	}
}

func TestInstallUninstallEdgePreservation(t *testing.T) {
	t.Run("fresh install over missing config", func(t *testing.T) {
		_, home := setup(t)
		a := New()
		n, err := a.InstallHooks(false, false)
		if err != nil || n != len(hookRules) {
			t.Fatalf("install: %d %v", n, err)
		}
		b, err := os.ReadFile(configPath(home))
		if err != nil || !bytes.Equal(b, hookBlock()) || !a.AreHooksInstalled() {
			t.Fatal("fresh install did not write exactly the managed block")
		}
		if len(backupsOf(home)) != 0 {
			t.Fatal("fresh install invented a backup of nothing")
		}
		if err = a.UninstallHooks(); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(configPath(home))
		if err != nil || len(after) != 0 {
			t.Fatalf("uninstall left residue: %d bytes (%v)", len(after), err)
		}
		bs := backupsOf(home)
		if len(bs) != 1 {
			t.Fatalf("uninstall of the managed block kept no recovery backup: %d", len(bs))
		}
		if b, _ := os.ReadFile(bs[0]); !bytes.Equal(b, hookBlock()) {
			t.Fatal("uninstall backup content mismatch")
		}
	})
	t.Run("uninstall with missing config", func(t *testing.T) {
		_, home := setup(t)
		if err := New().UninstallHooks(); err != nil {
			t.Fatalf("uninstall without config: %v", err)
		}
		if _, err := os.Stat(configPath(home)); !os.IsNotExist(err) {
			t.Fatal("uninstall created a config file")
		}
		if len(backupsOf(home)) != 0 {
			t.Fatal("uninstall without config created a backup")
		}
	})
	t.Run("uninstall leaves foreign-only config untouched", func(t *testing.T) {
		_, home := setup(t)
		original := []byte("x = 1\n[[hooks]]\nevent = 'Stop'\ncommand = 'echo foreign'\n")
		put(t, configPath(home), original)
		if err := New().UninstallHooks(); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(configPath(home)); !bytes.Equal(b, original) {
			t.Fatal("uninstall modified a foreign-only config")
		}
		if len(backupsOf(home)) != 0 {
			t.Fatal("no-op uninstall created a backup")
		}
	})
	t.Run("duplicate managed blocks are not installed", func(t *testing.T) {
		_, home := setup(t)
		put(t, configPath(home), append(append([]byte{}, hookBlock()...), hookBlock()...))
		if New().AreHooksInstalled() {
			t.Fatal("duplicated managed block counted as installed")
		}
	})
}

func savedDescriptor(t *testing.T, path string) []byte {
	t.Helper()
	save := filepath.Join(t.TempDir(), "descriptor.acl")
	if out, err := exec.Command("icacls", path, "/save", save).CombinedOutput(); err != nil {
		t.Fatalf("icacls save of %s: %v: %s", path, err, out)
	}
	b, err := os.ReadFile(save)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertConfigExtended(t *testing.T, path string, original, descriptor []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, original) {
		t.Fatalf("unrelated config bytes were rewritten:\nbefore: %q\nafter:  %q", original, b)
	}
	if !bytes.HasSuffix(b, hookBlock()) {
		t.Fatal("managed hook block missing after install")
	}
	if runtime.GOOS == windowsOS {
		if now := savedDescriptor(t, path); !bytes.Equal(descriptor, now) {
			t.Fatalf("install replaced the config security descriptor:\nbefore: %q\nafter:  %q", descriptor, now)
		}
	} else if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0600 {
		t.Fatalf("install changed config mode: %v", fi.Mode())
	}
}

func TestInstallReinstallPreservesConfigSecurityAndUnrelatedBytes(t *testing.T) {
	_, home := setup(t)
	path := configPath(home)
	original := []byte("api_key = '" + installerSentinel + "'\nforeign = 'A'\n")
	put(t, path, original)
	a := New()
	var descriptor []byte
	if runtime.GOOS == windowsOS {
		out, err := exec.Command("icacls", path).CombinedOutput()
		if err != nil {
			t.Fatalf("icacls on fixture config: %v: %s", err, out)
		}
		if !strings.Contains(string(out), "(I)") {
			t.Skip("fixture config carries no inherited ACL entries to preserve")
		}
		descriptor = savedDescriptor(t, path)
	}
	if n, err := a.InstallHooks(false, false); err != nil || n != len(hookRules) {
		t.Fatalf("install: %d %v", n, err)
	}
	assertConfigExtended(t, path, original, descriptor)

	reinstalled := []byte("api_key = '" + installerSentinel + "'\nforeign = 'B'\n")
	put(t, path, reinstalled)
	if runtime.GOOS == windowsOS {
		descriptor = savedDescriptor(t, path)
	}
	if n, err := a.InstallHooks(false, false); err != nil || n != len(hookRules) {
		t.Fatalf("reinstall: %d %v", n, err)
	}
	assertConfigExtended(t, path, reinstalled, descriptor)

	bs := backupsOf(home)
	if len(bs) != 2 {
		t.Fatalf("backup count after install and reinstall: %d, want 2", len(bs))
	}
	if b, _ := os.ReadFile(bs[0]); !bytes.Equal(b, original) {
		t.Fatal("first backup does not hold the pre-install bytes")
	}
	if b, _ := os.ReadFile(bs[1]); !bytes.Equal(b, reinstalled) {
		t.Fatal("second backup does not hold the pre-reinstall bytes")
	}
	for _, backup := range bs {
		if runtime.GOOS == windowsOS {
			out, err := exec.Command("icacls", backup).CombinedOutput()
			if err != nil {
				t.Fatalf("icacls on backup %s: %v: %s", backup, err, out)
			}
			if strings.Contains(string(out), "(I)") {
				t.Fatalf("backup %s inherits directory ACL entries: %s", backup, out)
			}
		} else if fi, err := os.Stat(backup); err != nil {
			t.Fatal(err)
		} else if fi.Mode().Perm() != 0600 {
			t.Fatalf("backup is world-readable: %v", fi.Mode())
		}
	}
}

// Stale locks stay in place: the lock file records no holder identity, so a
// lock left by a crashed installer must not be reclaimed on a PID or age
// guess that could strand a live installer. Recovery stays manual removal,
// after which TestLockConflictAndStaleLock shows the installer converges.
func TestStaleLockIsReportedAndKeptForManualRecovery(t *testing.T) {
	_, home := setup(t)
	put(t, configPath(home), []byte("x = 1\n"))
	lockBody := []byte("left by a crashed installer; no holder identity is recorded")
	put(t, lockPath(home), lockBody)
	if _, err := New().InstallHooks(false, false); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("stale lock not reported: %v", err)
	}
	if b, err := os.ReadFile(lockPath(home)); err != nil || !bytes.Equal(b, lockBody) {
		t.Fatalf("installer disturbed a stale lock it cannot attribute: %v %q", err, b)
	}
}

func TestReplacementConsumesStagedTempFile(t *testing.T) {
	_, home := setup(t)
	put(t, configPath(home), []byte("x = 1\n"))
	a := New()
	if _, err := a.InstallHooks(false, false); err != nil {
		t.Fatal(err)
	}
	stale, err := filepath.Glob(filepath.Join(home, ".entire-config-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("install left staged temp files in the config directory: %v", stale)
	}
	if !a.AreHooksInstalled() {
		t.Fatal("config not installed after staged replacement")
	}
}

// The config must be replaced as a whole unit, not truncated in place: a
// failed descriptor-copy step has to abort while the original config bytes
// still stand and the staged temp is gone. A fake powershell on PATH forces
// the Windows descriptor-copy step to fail deterministically.
func TestDescriptorCopyFailureLeavesConfigUntouched(t *testing.T) {
	if runtime.GOOS != windowsOS {
		t.Skip("descriptor copy guards the Windows install path")
	}
	_, home := setup(t)
	path := configPath(home)
	original := []byte("api_key = '" + installerSentinel + "'\n")
	put(t, path, original)
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "powershell.cmd"), []byte("@exit /b 1\r\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := New().InstallHooks(false, false); err == nil || !strings.Contains(err.Error(), "security descriptor") {
		t.Fatalf("install without a working descriptor-copy step: %v", err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, original) {
		t.Fatal("failed descriptor copy mutated the config")
	}
	if len(backupsOf(home)) != 1 {
		t.Fatalf("failed descriptor copy produced %d backups, want 1", len(backupsOf(home)))
	}
	if stale, _ := filepath.Glob(filepath.Join(home, ".entire-config-*")); len(stale) != 0 {
		t.Fatalf("failed install left staged temp files: %v", stale)
	}
	if _, err := os.Stat(lockPath(home)); !os.IsNotExist(err) {
		t.Fatal("failed install left a lock behind")
	}
}

// stagedWrite must neither create nor partially fill a stage: the temp file is
// staged beforehand, so an unexpected missing stage has to fail loudly rather
// than reappear with the replacement bytes, and a present stage is overwritten
// whole. Crash behavior after Sync is not asserted here: whether the synced
// bytes survive a power cut is a property of the storage stack, not of this
// function.
func TestStagedWriteRequiresStagedTargetAndReplacesItWhole(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-staged.toml")
	if err := stagedWrite(missing, []byte("x = 1\n")); err == nil {
		t.Fatal("staged write accepted a target that was never staged")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("failed staged write created a file")
	}
	path := filepath.Join(dir, "staged.toml")
	stale := []byte("previous contents, longer than the replacement")
	if err := os.WriteFile(path, stale, 0600); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("x = 2\n")
	if err := stagedWrite(path, replacement); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, replacement) {
		t.Fatalf("staged write left %q (%v), want %q", b, err, replacement)
	}
	if err := stagedWrite(path, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Fatalf("staged write truncated to %q, want an empty file", b)
	}
}
