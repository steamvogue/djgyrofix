package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestDevelVersionFromVCSSettings(t *testing.T) {
	cases := map[string]struct {
		settings []debug.BuildSetting
		want     string
	}{
		"clean checkout": {
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
				{Key: "vcs.modified", Value: "false"},
			},
			want: "devel+0123456789ab",
		},
		"modified checkout": {
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
				{Key: "vcs.modified", Value: "true"},
			},
			want: "devel+0123456789ab.dirty",
		},
		"short revision is not truncated": {
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}},
			want:     "devel+abc123",
		},
		// A build from an exported tarball has no VCS metadata at all. It must
		// say so rather than invent a release number.
		"no vcs metadata": {
			settings: []debug.BuildSetting{{Key: "-buildmode", Value: "exe"}},
			want:     "devel",
		},
		"no settings": {settings: nil, want: "devel"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if got := develVersion(test.settings); got != test.want {
				t.Errorf("develVersion = %q, want %q", got, test.want)
			}
		})
	}
}

// TestStampedVersionWins pins the precedence: a link-time stamp is the most
// authoritative source, because it is what the release workflow sets from the
// git tag being built.
func TestStampedVersionWins(t *testing.T) {
	original := stamped
	t.Cleanup(func() { stamped = original })

	for input, want := range map[string]string{
		"v1.2.3":         "1.2.3",
		"1.2.3":          "1.2.3",
		"v0.1.0-rc.1":    "0.1.0-rc.1",
		"v2.0.0+dirty":   "2.0.0+dirty",
		"devel+abc12345": "devel+abc12345",
	} {
		stamped = input
		if got := resolveVersion(); got != want {
			t.Errorf("resolveVersion with stamp %q = %q, want %q", input, got, want)
		}
	}
}

// TestVersionIsReportedEverywhere guards against the version being resolved in
// one place and hardcoded in another — the drift this whole mechanism exists to
// prevent. The journal records ToolName, and a bug report quotes `version`.
func TestVersionIsReportedEverywhere(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
	if !strings.HasPrefix(ToolName, "djgyrofix ") {
		t.Errorf("ToolName = %q, want it to start with the binary name", ToolName)
	}
	if !strings.Contains(ToolName, Version) {
		t.Errorf("ToolName %q does not contain Version %q", ToolName, Version)
	}
	if !strings.Contains(usage(), Version) {
		t.Errorf("the top-level help does not name the version %q", Version)
	}
	// A stray "v" prefix would make the reported version disagree with the tag
	// it was built from in a way that is easy to miss in a bug report.
	if strings.HasPrefix(Version, "v") {
		t.Errorf("Version %q keeps its leading v; the tag prefix should be trimmed", Version)
	}
}
