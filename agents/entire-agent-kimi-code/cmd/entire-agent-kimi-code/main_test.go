package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionStringStamped(t *testing.T) {
	original := version
	defer func() { version = original }()

	version = "v1.2.3"
	if got := versionString(); got != "v1.2.3" {
		t.Fatalf("versionString() = %q, want %q", got, "v1.2.3")
	}

	version = ""
	if got := versionString(); got == "" {
		t.Fatal("versionString() is empty for unset stamp, want non-empty")
	}
}

func TestVersionStringDefault(t *testing.T) {
	original := version
	defer func() { version = original }()

	version = devVersion
	if got := versionString(); got == "" || strings.ContainsAny(got, "\n\r") {
		t.Fatalf("versionString() = %q, want single-line non-empty", got)
	}
}

func TestVersionFromBuildInfoModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.5.1"}}
	if got := versionFromBuildInfo(info); got != "v0.5.1" {
		t.Fatalf("versionFromBuildInfo() = %q, want %q", got, "v0.5.1")
	}
}

func TestVersionFromBuildInfoVCSRevisionFallback(t *testing.T) {
	fullRevision := "31fb631a1b2c3d4e5f60718293a4b5c6d7e8f901"
	tests := map[string]struct {
		info *debug.BuildInfo
		want string
	}{
		"devel with revision": {
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: fullRevision},
				},
			},
			want: devVersion + "-" + fullRevision[:revisionLength],
		},
		"devel with short revision": {
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc123"},
				},
			},
			want: devVersion + "-abc123",
		},
		"devel without revision": {
			info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want: "",
		},
		"nil info": {info: nil, want: ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := versionFromBuildInfo(test.info); got != test.want {
				t.Fatalf("versionFromBuildInfo() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRejectExtraArgs(t *testing.T) {
	tests := map[string]struct {
		args    []string
		wantErr bool
	}{
		"no args":       {args: nil, wantErr: false},
		"empty args":    {args: []string{}, wantErr: false},
		"single arg":    {args: []string{"extra"}, wantErr: true},
		"multiple args": {args: []string{"extra", "args"}, wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := rejectExtraArgs("info", test.args)
			if test.wantErr != (err != nil) {
				t.Fatalf("rejectExtraArgs() error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "info") {
				t.Fatalf("rejectExtraArgs() error %q does not name the subcommand", err.Error())
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := map[string]struct {
		value  string
		length int
		want   string
	}{
		"shorter than limit":  {value: "abc", length: 12, want: "abc"},
		"exactly limit":       {value: "abcdefghijkl", length: 12, want: "abcdefghijkl"},
		"longer than limit":   {value: "abcdefghijklm", length: 12, want: "abcdefghijkl"},
		"empty value":         {value: "", length: 12, want: ""},
		"limit zero":          {value: "abc", length: 0, want: ""},
		"limit exceeds value": {value: "ab", length: 5, want: "ab"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := truncate(test.value, test.length); got != test.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", test.value, test.length, got, test.want)
			}
		})
	}
}
