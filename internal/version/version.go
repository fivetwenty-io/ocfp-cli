package version

import (
	"fmt"
	"runtime"
)

// Build information set via ldflags
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
	GoVersion = runtime.Version()
)

// Info holds version information
type Info struct {
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	GitCommit string `json:"git_commit"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Get returns the version information
func Get() Info {
	return Info{
		Version:   Version,
		BuildTime: BuildTime,
		GitCommit: GitCommit,
		GoVersion: GoVersion,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// String returns a formatted version string
func (i Info) String() string {
	return fmt.Sprintf("OCFP CLI v%s (commit: %s, built: %s, go: %s, %s/%s)",
		i.Version, i.GitCommit, i.BuildTime, i.GoVersion, i.OS, i.Arch)
}

// Short returns a short version string
func (i Info) Short() string {
	return fmt.Sprintf("v%s", i.Version)
}