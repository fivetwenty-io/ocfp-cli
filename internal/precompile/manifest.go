package precompile

import (
	"errors"
	"fmt"
	"strings"
)

// RenderCompileManifest renders a no-VM compilation deployment manifest:
// instance_groups is empty, so BOSH creates zero VMs and only compiles packages
// when export-release is invoked. Mirrors compilation_manifest in _cfcompile.py.
//
// name is the deployment name (e.g. "<bloc>-precompile-cf"); rels are the
// releases to register (their source must already be uploaded to the director).
func RenderCompileManifest(name string, rels []Release, sc Stemcell) ([]byte, error) {
	if name == "" {
		return nil, errors.New("compile manifest: empty deployment name")
	}

	if len(rels) == 0 {
		return nil, errors.New("compile manifest: no releases")
	}

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	b.WriteString("releases:\n")

	for _, r := range rels {
		fmt.Fprintf(&b, "- name: %s\n", r.Name)
		fmt.Fprintf(&b, "  version: %q\n", r.Version)
	}

	b.WriteString("stemcells:\n")
	b.WriteString("- alias: default\n")
	fmt.Fprintf(&b, "  os: %s\n", sc.OS)
	fmt.Fprintf(&b, "  version: %q\n", sc.Version)
	b.WriteString("instance_groups: []\n")
	b.WriteString("update:\n")
	b.WriteString("  canaries: 1\n")
	b.WriteString("  max_in_flight: 1\n")
	b.WriteString("  canary_watch_time: 1000-30000\n")
	b.WriteString("  update_watch_time: 1000-30000\n")
	b.WriteString("  serial: false\n")

	return []byte(b.String()), nil
}
