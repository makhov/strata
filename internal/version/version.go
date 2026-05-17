// Package version exposes the t4 build version. Values are populated via
// -ldflags "-X" at link time; when unset, Info falls back to runtime build
// info so locally-built binaries still report a useful identity.
package version

import (
	"runtime"
	"runtime/debug"
)

var (
	Version = ""
	Commit  = ""
	Date    = ""
)

type BuildInfo struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
}

func Info() BuildInfo {
	info := BuildInfo{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		if info.Version == "" {
			info.Version = "dev"
		}
		return info
	}

	if info.Version == "" {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		} else {
			info.Version = "dev"
		}
	}
	if info.Commit == "" || info.Date == "" {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			case "vcs.modified":
				if s.Value == "true" && info.Commit != "" {
					info.Commit += "-dirty"
				}
			}
		}
	}
	return info
}
