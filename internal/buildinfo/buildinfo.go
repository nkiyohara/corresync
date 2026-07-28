// Package buildinfo exposes reproducible version metadata for every adapter.
package buildinfo

import "runtime"

// Values are replaced by GoReleaser through -ldflags.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

// Info describes the exact Corresync binary currently running.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Current returns immutable build information.
func Current() Info {
	return Info{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}
