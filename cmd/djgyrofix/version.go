package main

import (
	"runtime/debug"
	"strings"
)

// stamped is set at link time by the release workflow and by `make build`:
//
//	go build -ldflags "-X main.stamped=$(git describe --tags)"
//
// It is deliberately not a constant sitting next to the git tag. Two places
// holding the same version number drift the first time someone bumps one and
// forgets the other, and the binary then lies about which release it is —
// which matters here, because the version is recorded in every patch journal
// and is the first thing anyone reports in a bug.
var stamped string

// Version is what this binary reports and records.
var Version = resolveVersion()

// ToolName is what appears in journals and reports.
var ToolName = "djgyrofix " + Version

// resolveVersion picks the most authoritative version available, in order:
//
//  1. The value stamped at link time — a release build, or `make build`.
//  2. The module version Go records for `go install <pkg>@<version>`, which
//     carries no link flags but does know the tag it resolved.
//  3. The VCS revision Go embeds for a build from a working tree.
//
// The last case reports a commit rather than inventing a release number. A
// development build claiming to be "0.1.0" is worse than one that admits it is
// a development build.
func resolveVersion() string {
	if stamped != "" {
		return strings.TrimPrefix(stamped, "v")
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if version := info.Main.Version; version != "" && version != "(devel)" {
		return strings.TrimPrefix(version, "v")
	}
	return develVersion(info.Settings)
}

// develVersion composes a version from the VCS metadata Go embeds when building
// from a checkout.
func develVersion(settings []debug.BuildSetting) string {
	var revision string
	dirty := false
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "devel"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	version := "devel+" + revision
	if dirty {
		version += ".dirty"
	}
	return version
}
