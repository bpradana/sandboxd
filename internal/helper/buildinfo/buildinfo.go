// Package buildinfo exposes the version, commit, and build time stamped into the binary at link time.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// These values are populated by Go linker flags in release builds. Local
// builds fall back to the VCS settings recorded by the Go toolchain.
var (
	buildVersion string
	buildTime    string
)

type Info struct {
	Version   string `json:"version"`
	Tag       string `json:"tag"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Modified  bool   `json:"modified"`
}

// unknownValue is the placeholder used wherever build metadata is absent,
// which is the normal case for a `go run` build with no VCS stamping.
const unknownValue = "unknown"

func Read(environment string) Info {
	environment = strings.TrimSpace(environment)
	injectedVersion := strings.TrimSpace(buildVersion)
	info := Info{
		BuildTime: strings.TrimSpace(buildTime),
	}
	if strings.Contains(strings.ToLower(environment), "production") {
		info.Tag = injectedVersion
	} else {
		info.Commit = injectedVersion
	}

	build, ok := debug.ReadBuildInfo()
	if ok {
		if info.Tag == "" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Tag = build.Main.Version
		}

		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = setting.Value
				}
			case "vcs.time":
				if info.BuildTime == "" {
					info.BuildTime = setting.Value
				}
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}

	if info.Tag == "" {
		info.Tag = unknownValue
	}
	info.Commit = shortCommit(info.Commit)
	info.Version = formatVersion(environment, info.Tag, info.Commit, injectedVersion == "")
	return info
}

func shortCommit(commit string) string {
	const shortLength = 7

	commit = strings.TrimSpace(commit)
	if commit == "" {
		return unknownValue
	}
	if len(commit) <= shortLength {
		return commit
	}

	return commit[:shortLength]
}

func formatVersion(environment, tag, revision string, local bool) string {
	environment = strings.ToLower(strings.TrimSpace(environment))

	switch {
	case strings.Contains(environment, "production"):
		return valueOrUnknown(tag)
	case strings.Contains(environment, "staging"):
		return "staging." + valueOrUnknown(revision)
	case strings.Contains(environment, "develop"):
		if local {
			return "dev.local+" + valueOrUnknown(revision)
		}
		return "dev." + valueOrUnknown(revision)
	default:
		return unknownValue
	}
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknownValue
	}
	return value
}
