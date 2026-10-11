package cmd

import (
	"runtime/debug"
	"strings"
)

// Set at release time with -ldflags "-X github.com/AustinMusiku/timeline/cmd.version=...".
var (
	version string
	commit  string
	date    string
)

// buildVersion describes this build, reading Go's build info for whatever the linker flags left unset.
func buildVersion() string {
	v, c, d := version, commit, date
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" {
			v = info.Main.Version
		}
		for _, s := range info.Settings {
			switch {
			case s.Key == "vcs.revision" && c == "":
				c = s.Value
			case s.Key == "vcs.time" && d == "":
				d = s.Value
			}
		}
	}
	return formatVersion(v, c, d)
}

func formatVersion(version, commit, date string) string {
	version = strings.TrimPrefix(version, "v")
	if version == "" || version == "(devel)" {
		version = "dev"
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if len(date) > 10 {
		date = date[:10]
	}

	// An untagged build's version already names its commit.
	if commit == "" || strings.Contains(version, commit) {
		return version
	}
	if date == "" {
		return version + " (" + commit + ")"
	}
	return version + " (" + commit + ", " + date + ")"
}
