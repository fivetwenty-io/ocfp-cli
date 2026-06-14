package precompile

import (
	"fmt"

	"github.com/goccy/go-yaml"
)

// cfReleaseDoc is the minimal shape parsed from a cf-deployment manifest's
// /releases entries. cf-deployment.yml lists source URLs + sha1; compiled
// builds for our target stemcell (noble-1.383) are not published upstream, so
// these releases take the compile-local path (the parsed url/sha1 are the
// SOURCE tarball the director must upload before compiling).
type cfReleaseDoc struct {
	Releases []struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		URL     string `yaml:"url"`
		SHA1    string `yaml:"sha1"`
	} `yaml:"releases"`
}

// ParseCFReleases extracts the release set from a cf-deployment manifest
// (cf-deployment.yml). The parsed url/sha1 populate the source fields used by
// the compile-local path. minExpected guards against a malformed/truncated
// manifest silently yielding a short release list.
func ParseCFReleases(manifestYAML []byte, minExpected int) ([]Release, error) {
	var doc cfReleaseDoc

	err := yaml.Unmarshal(manifestYAML, &doc)
	if err != nil {
		return nil, fmt.Errorf("parsing cf-deployment manifest: %w", err)
	}

	if len(doc.Releases) < minExpected {
		return nil, fmt.Errorf("cf-deployment manifest has %d releases, expected at least %d (truncated or wrong file?)",
			len(doc.Releases), minExpected)
	}

	out := make([]Release, 0, len(doc.Releases))
	for _, r := range doc.Releases {
		if r.Name == "" || r.Version == "" {
			return nil, fmt.Errorf("cf-deployment release entry missing name or version: %+v", r)
		}

		out = append(out, Release{
			Name:              r.Name,
			Version:           r.Version,
			UpstreamSourceURL: r.URL,
			UpstreamSourceSHA: r.SHA1,
			// No upstream compiled build for noble-1.383 -> compile-local.
			UpstreamCompiledURL: "",
		})
	}

	return out, nil
}

// CFMinReleases is the expected floor for cf-deployment v56.5.0 (39 releases).
// A parse yielding fewer signals a wrong or truncated manifest.
const CFMinReleases = 30
