package precompile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Director abstracts the bosh-CLI operations precompile drives against a BOSH
// director. The production implementation shells the `bosh` binary on the
// bastion (where ocfp runs); tests substitute a fake.
type Director interface {
	ReleasePresent(ctx context.Context, name, version string) (bool, error)
	UploadRelease(ctx context.Context, url, sha string) error
	Deploy(ctx context.Context, deployment, manifestPath string) error
	ExportRelease(ctx context.Context, deployment, name, version string, sc Stemcell, destDir string) (string, error)
}

// boshDirector drives the local `bosh` binary. envAlias is the director
// environment alias (`bosh -e <alias>`); empty uses the ambient BOSH_* env.
type boshDirector struct {
	envAlias string
	stdout   *os.File
	stderr   *os.File
}

// NewBOSHDirector returns a Director backed by the local bosh CLI.
func NewBOSHDirector(envAlias string) Director {
	return &boshDirector{envAlias: envAlias, stdout: os.Stdout, stderr: os.Stderr}
}

func (d *boshDirector) args(rest ...string) []string {
	out := make([]string, 0, len(rest)+2)
	if d.envAlias != "" {
		out = append(out, "-e", d.envAlias)
	}

	return append(out, rest...)
}

// boshReleasesJSON is the shape of `bosh releases --json`.
type boshReleasesJSON struct {
	Tables []struct {
		Rows []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"Rows"`
	} `json:"Tables"`
}

func (d *boshDirector) ReleasePresent(ctx context.Context, name, version string) (bool, error) {
	cmd := exec.CommandContext(ctx, "bosh", d.args("releases", "--json")...) // #nosec G204 -- fixed subcommand, no user shell

	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("bosh releases: %w", err)
	}

	var parsed boshReleasesJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false, fmt.Errorf("parsing bosh releases json: %w", err)
	}

	// bosh reports versions with a trailing '*' for currently-deployed; trim it.
	want := version

	for _, t := range parsed.Tables {
		for _, row := range t.Rows {
			if row.Name == name && trimVersionMark(row.Version) == want {
				return true, nil
			}
		}
	}

	return false, nil
}

func (d *boshDirector) UploadRelease(ctx context.Context, url, sha string) error {
	rest := []string{"-n", "upload-release"}
	if sha != "" {
		rest = append(rest, "--sha1", sha)
	}

	rest = append(rest, url)

	cmd := exec.CommandContext(ctx, "bosh", d.args(rest...)...) // #nosec G204 -- args validated by caller; no shell
	cmd.Stdout = d.stdout

	cmd.Stderr = d.stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("bosh upload-release %s: %w", url, err)
	}

	return nil
}

func (d *boshDirector) Deploy(ctx context.Context, deployment, manifestPath string) error {
	cmd := exec.CommandContext(ctx, "bosh", d.args("-n", "-d", deployment, "deploy", manifestPath)...) // #nosec G204 -- fixed args; no shell
	cmd.Stdout = d.stdout

	cmd.Stderr = d.stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("bosh deploy %s: %w", deployment, err)
	}

	return nil
}

func (d *boshDirector) ExportRelease(ctx context.Context, deployment, name, version string, sc Stemcell, destDir string) (string, error) {
	relRef := name + "/" + version
	scRef := sc.OS + "/" + sc.Version

	cmd := exec.CommandContext(ctx, "bosh", // #nosec G204 -- fixed args; no shell
		d.args("-d", deployment, "export-release", relRef, scRef, "--dir", destDir)...)
	cmd.Stdout = d.stdout

	cmd.Stderr = d.stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("bosh export-release %s: %w", relRef, err)
	}

	// export-release writes <name>-<version>-<os>-<scver>-<timestamp>.tgz into
	// destDir. Locate the produced tarball by prefix.
	path, err := findExportedTarball(destDir, name, version)
	if err != nil {
		return "", err
	}

	return path, nil
}

func findExportedTarball(dir, name, version string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading export dir %s: %w", dir, err)
	}

	prefix := fmt.Sprintf("%s-%s-", name, version)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		n := e.Name()
		if len(n) >= len(prefix) && n[:len(prefix)] == prefix && filepath.Ext(n) == ".tgz" {
			return filepath.Join(dir, n), nil
		}
	}

	return "", fmt.Errorf("no exported tarball for %s/%s in %s", name, version, dir)
}

func trimVersionMark(v string) string {
	if n := len(v); n > 0 && v[n-1] == '*' {
		return v[:n-1]
	}

	return v
}
