package kimi

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const windowsOS = "windows"

const beginMarker = "\n# BEGIN entire-agent-kimi-code managed hooks\n"
const endMarker = "# END entire-agent-kimi-code managed hooks\n"

func hookBlock() []byte {
	var b strings.Builder
	b.WriteString(beginMarker)
	for _, h := range hookRules {
		command := strconv.Quote("entire-agent-kimi-code dispatch-hook " + h.Name)
		fmt.Fprintf(&b, "[[hooks]]\nevent = %q\ncommand = %s\ntimeout = 30\n\n", h.Event, command)
	}
	b.WriteString(endMarker)
	return []byte(b.String())
}
func removeBlock(data []byte) ([]byte, error) {
	start := bytes.Index(data, []byte(beginMarker))
	if start < 0 {
		if bytes.Contains(data, []byte(endMarker)) {
			return nil, errors.New("unmatched Entire hook marker")
		}
		return data, nil
	}
	end := bytes.Index(data[start:], []byte(endMarker))
	if end < 0 {
		return nil, errors.New("unterminated Entire hook block")
	}
	end += start + len(endMarker)
	if bytes.Contains(data[end:], []byte(beginMarker)) {
		return nil, errors.New("duplicate Entire hook blocks")
	}
	return append(append([]byte{}, data[:start]...), data[end:]...), nil
}
func secureBackup(path string) error {
	if runtime.GOOS != windowsOS {
		return os.Chmod(path, 0600)
	}
	u, e := user.Current()
	if e != nil {
		return e
	}
	// Windows Go permission bits do not set a DACL. Restrict secret-bearing
	// backups explicitly rather than claiming chmod protects them.
	out, e := exec.Command("icacls", path, "/inheritance:r", "/grant:r", "*"+u.Uid+":(F)").CombinedOutput()
	if e != nil {
		return fmt.Errorf("restrict config backup permissions: %w (%d bytes diagnostics)", e, len(out))
	}
	return nil
}
func copySecurityDescriptor(source, destination string) error {
	script := "$ErrorActionPreference = 'Stop'\n" +
		"try {\n" +
		"  $s = [IO.File]::GetAccessControl($env:ENTIRE_ACL_SOURCE)\n" +
		"  [IO.File]::SetAccessControl($env:ENTIRE_ACL_DESTINATION, $s)\n" +
		"} catch {\n" +
		"  exit 1\n" +
		"}\n" +
		"exit 0"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(),
		"ENTIRE_ACL_SOURCE="+source,
		"ENTIRE_ACL_DESTINATION="+destination)
	out, e := cmd.CombinedOutput()
	if e != nil {
		return fmt.Errorf("preserve config security descriptor: %w (%d bytes diagnostics)", e, len(out))
	}
	return nil
}
func updateConfig(install bool) (int, error) {
	path := filepath.Join(kimiHome(), "config.toml")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return 0, e
	}
	lock, e := os.OpenFile(path+".entire-lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return 0, errors.New("kimi config is locked by another Entire installer")
	}
	defer os.Remove(lock.Name())
	defer lock.Close()
	before, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return 0, e
	}
	clean, e := removeBlock(before)
	if e != nil {
		return 0, e
	}
	after := clean
	count := 0
	if install {
		after = append(append([]byte{}, clean...), hookBlock()...)
		count = len(hookRules)
	}
	if bytes.Equal(before, after) {
		return 0, nil
	}
	if len(before) > 0 {
		backup := path + ".entire-agent-kimi-code." + time.Now().UTC().Format("20060102T150405.000000000") + ".bak"
		// Secure an empty backup before writing any credentials into it.
		f, e := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return 0, e
		}
		if e = secureBackup(backup); e != nil {
			_ = f.Close()
			_ = os.Remove(backup)
			return 0, e
		}
		_, e = f.Write(before)
		ce := f.Close()
		if e != nil {
			return 0, e
		}
		if ce != nil {
			return 0, ce
		}
	}
	current, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return 0, e
	}
	if !bytes.Equal(current, before) {
		return 0, errors.New("kimi config changed concurrently; retry installation")
	}
	// Stage the replacement in a sibling temporary file so the config itself is
	// never seen partially written: the temp is fully written and closed before
	// a single rename swaps it into place.
	f, e := os.CreateTemp(filepath.Dir(path), ".entire-config-*")
	if e != nil {
		return 0, e
	}
	temp := f.Name()
	defer os.Remove(temp)
	if e = f.Close(); e != nil {
		return 0, e
	}
	if len(before) > 0 && runtime.GOOS == windowsOS {
		// Renaming over the config would otherwise swap its security descriptor
		// for the temp file's restricted one, silently dropping inherited grants
		// such as SYSTEM or Administrators. Copy the config's descriptor onto
		// the freshly created temp while it is still empty: SetAccessControl
		// silently no-ops against a restricted destination, and later secret
		// bytes then land in a file protected exactly like the config itself.
		// A failed copy aborts before the config is touched at all.
		if e = copySecurityDescriptor(path, temp); e != nil {
			return 0, e
		}
	} else if e = secureBackup(temp); e != nil {
		return 0, e
	}
	if e = stagedWrite(temp, after); e != nil {
		return 0, e
	}
	current, e = os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return 0, e
	}
	if !bytes.Equal(current, before) {
		return 0, errors.New("kimi config changed concurrently; retry installation")
	}
	if e = os.Rename(temp, path); e != nil {
		return 0, e
	}
	return count, nil
}

// stagedWrite fills an already-staged sibling temp file with the replacement
// config bytes and syncs them to storage before the caller renames it into
// place. The target is never created or silently extended: it is opened
// without O_CREATE so a lost stage fails loudly, and the handle is synced and
// closed before the rename, which on Windows cannot displace a file that a
// live handle still holds open.
func stagedWrite(path string, data []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(data); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		_ = f.Close()
		return e
	}
	return f.Close()
}
func (a *Agent) InstallHooks(_ bool, _ bool) (int, error) {
	n, e := updateConfig(true)
	if e != nil {
		return n, e
	}
	return n, configureRuntime()
}
func (a *Agent) UninstallHooks() error { _, e := updateConfig(false); return e }
func (a *Agent) AreHooksInstalled() bool {
	b, e := os.ReadFile(filepath.Join(kimiHome(), "config.toml"))
	return e == nil && bytes.Count(b, []byte(beginMarker)) == 1 && bytes.Contains(b, hookBlock())
}
