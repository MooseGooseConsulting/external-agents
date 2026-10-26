package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/kimi"
	"github.com/MooseGooseConsulting/external-agents/agents/entire-agent-kimi-code/internal/protocol"
)

// devVersion is the fallback version for builds without a stamp, module
// version, or VCS revision.
const devVersion = "dev"

// revisionLength bounds the VCS revision used in the fallback version.
const revisionLength = 12

// version is stamped by GoReleaser via -X main.version={{.Version}}. Ordinary
// builds keep the default and fall back to build information.
var version = devVersion

func main() {
	agent := kimi.New()
	if len(os.Args) < 2 {
		fatalf("usage: entire-agent-kimi-code <subcommand> [args]")
	}

	var err error
	switch os.Args[1] {
	case "version", "--version":
		if err = rejectExtraArgs(os.Args[1], os.Args[2:]); err == nil {
			fmt.Fprintf(os.Stdout, "entire-agent-kimi-code version %s\n", versionString())
		}
	case "dispatch-hook":
		if len(os.Args) != 3 {
			fatalf("usage: entire-agent-kimi-code dispatch-hook <hook-name>")
		}
		err = kimi.DispatchHook(os.Args[2], os.Stdin, os.Stdout, os.Stderr)
	case "calculate-tokens", "calculate-total-tokens", "extract-all-modified-files":
		err = agent.HandleAggregate(os.Args[1], os.Args[2:], os.Stdin, os.Stdout)
	case "info":
		if err = rejectExtraArgs(os.Args[1], os.Args[2:]); err == nil {
			err = protocol.WriteJSON(os.Stdout, agent.Info())
		}
	case "detect":
		if err = rejectExtraArgs(os.Args[1], os.Args[2:]); err == nil {
			err = protocol.WriteJSON(os.Stdout, agent.Detect())
		}
	case "get-session-id":
		err = protocol.HandleGetSessionID(os.Stdin, os.Stdout, agent)
	case "get-session-dir":
		err = protocol.HandleGetSessionDir(os.Args[2:], os.Stdout, agent)
	case "resolve-session-file":
		err = protocol.HandleResolveSessionFile(os.Args[2:], os.Stdout, agent)
	case "read-session":
		err = protocol.HandleReadSession(os.Stdin, os.Stdout, agent)
	case "write-session":
		err = protocol.HandleWriteSession(os.Stdin, agent)
	case "read-transcript":
		err = protocol.HandleReadTranscript(os.Args[2:], os.Stdout, agent)
	case "chunk-transcript":
		err = protocol.HandleChunkTranscript(os.Args[2:], os.Stdin, os.Stdout, agent)
	case "reassemble-transcript":
		err = protocol.HandleReassembleTranscript(os.Stdin, os.Stdout, agent)
	case "compact-transcript":
		err = protocol.HandleCompactTranscript(os.Args[2:], os.Stdout, agent)
	case "format-resume-command":
		err = protocol.HandleFormatResumeCommand(os.Args[2:], os.Stdout, agent)
	case "parse-hook":
		err = protocol.HandleParseHook(os.Args[2:], os.Stdin, os.Stdout, agent)
	case "install-hooks":
		err = protocol.HandleInstallHooks(os.Args[2:], os.Stdout, agent)
	case "uninstall-hooks":
		if err = rejectExtraArgs(os.Args[1], os.Args[2:]); err == nil {
			err = agent.UninstallHooks()
		}
	case "are-hooks-installed":
		if err = rejectExtraArgs(os.Args[1], os.Args[2:]); err == nil {
			err = protocol.WriteJSON(os.Stdout, protocol.AreHooksInstalledResponse{Installed: agent.AreHooksInstalled()})
		}
	case "get-transcript-position":
		err = protocol.HandleGetTranscriptPosition(os.Args[2:], os.Stdout, agent)
	case "extract-modified-files":
		err = protocol.HandleExtractModifiedFiles(os.Args[2:], os.Stdout, agent)
	case "extract-prompts":
		err = protocol.HandleExtractPrompts(os.Args[2:], os.Stdout, agent)
	case "extract-summary":
		err = protocol.HandleExtractSummary(os.Args[2:], os.Stdout, agent)
	default:
		fatalf("unknown subcommand: %s", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// versionString reports the effective version: the GoReleaser stamp when
// present, otherwise the build's module version or VCS revision.
func versionString() string {
	if version != "" && version != devVersion {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := versionFromBuildInfo(info); v != "" {
			return v
		}
	}
	return devVersion
}

// versionFromBuildInfo resolves a version from build information: the main
// module version when the build used a real module version, otherwise the VCS
// revision.
func versionFromBuildInfo(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return devVersion + "-" + truncate(setting.Value, revisionLength)
		}
	}
	return ""
}

// rejectExtraArgs returns an error when arguments are passed to a subcommand
// that accepts none.
func rejectExtraArgs(subcommand string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("unexpected arguments for %s: %s", subcommand, strings.Join(args, " "))
}

func truncate(value string, length int) string {
	if len(value) <= length {
		return value
	}
	return value[:length]
}
