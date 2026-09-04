package bastion

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ocfpCLISource names where the bastion's ocfp binary comes from.
type ocfpCLISource string

const (
	// ocfpCLISourceRelease downloads the published GitHub release on the bastion.
	ocfpCLISourceRelease ocfpCLISource = "release"
	// ocfpCLISourceLocal uploads a linux binary built on the operator machine.
	ocfpCLISourceLocal ocfpCLISource = "local"

	// ocfpCLIVersionLatest is the spelling operators may use to ask for the
	// newest release explicitly instead of the operator's own version.
	ocfpCLIVersionLatest = "latest"

	envOCFPCLISource  = "OCFP_CLI_SOURCE"
	envOCFPCLIVersion = "OCFP_CLI_VERSION"
	envOCFPBinaryPath = "OCFP_BINARY_PATH"
)

// ocfpCLIInstallSpec is the resolved answer to "which ocfp goes on the bastion".
type ocfpCLIInstallSpec struct {
	Source ocfpCLISource
	// Version is the release version without its leading v. Empty means the
	// latest release. It is always empty for the local source.
	Version string
}

// OCFP CLI install resolution errors.
var (
	ErrOCFPCLISourceInvalid  = errors.New("ocfp cli source must be release or local")
	ErrOCFPCLIVersionInvalid = errors.New("ocfp cli version contains characters that are not safe in a release tag")
)

// ocfpCLIVersionPattern bounds what an operator-supplied pin may splice into
// the generated bash script. Release tags are semver-ish, so anything outside
// this set is a typo or an injection attempt, and either deserves an error
// rather than a shell.
var ocfpCLIVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]*$`)

// ocfpCLIReleasePattern recognises a version that a tagged release build
// stamps: a bare semver, optionally with a prerelease suffix. Anything else,
// such as the git-describe strings the Makefile stamps (v0.1.0-3-gabc1234,
// v0.1.0-dirty, dev/main/abc1234), has no release asset to download, so it
// falls back to the latest release instead of a guaranteed 404.
var ocfpCLIReleasePattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc|pre)[0-9A-Za-z.]*)?$`)

// ocfpCLIResolveInputs carries everything the resolver reads so tests can
// substitute the environment and the filesystem.
type ocfpCLIResolveInputs struct {
	Config config.OCFPCLIConfig
	Getenv func(string) string
	// FileExists reports whether an operator-supplied path is present. It
	// stops a stale OCFP_BINARY_PATH from silently selecting the local source.
	FileExists func(string) bool
	// OperatorVersion is the version of the ocfp running this command.
	OperatorVersion string
}

// resolveOCFPCLIInstall decides the install source and version from the bloc
// config, the environment, and the version of the ocfp running this command.
//
// Source precedence is OCFP_CLI_SOURCE, then an existing OCFP_BINARY_PATH
// implying local, then bastion.ocfpCli.source, then release. The version only
// matters for the release source; its precedence is OCFP_CLI_VERSION, then
// bastion.ocfpCli.version, then the operator's own version when that is a
// release build, and otherwise the latest release.
func resolveOCFPCLIInstall(in ocfpCLIResolveInputs) (ocfpCLIInstallSpec, error) {
	source, err := resolveOCFPCLISource(in)
	if err != nil {
		return ocfpCLIInstallSpec{}, err
	}

	if source == ocfpCLISourceLocal {
		return ocfpCLIInstallSpec{Source: source}, nil
	}

	version, err := resolveOCFPCLIVersion(in)
	if err != nil {
		return ocfpCLIInstallSpec{}, err
	}

	return ocfpCLIInstallSpec{Source: source, Version: version}, nil
}

func resolveOCFPCLISource(in ocfpCLIResolveInputs) (ocfpCLISource, error) {
	if raw := in.Getenv(envOCFPCLISource); raw != "" {
		return parseOCFPCLISource(raw)
	}

	if path := in.Getenv(envOCFPBinaryPath); path != "" && in.FileExists != nil && in.FileExists(path) {
		return ocfpCLISourceLocal, nil
	}

	if in.Config.Source != "" {
		return parseOCFPCLISource(in.Config.Source)
	}

	return ocfpCLISourceRelease, nil
}

func parseOCFPCLISource(raw string) (ocfpCLISource, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ocfpCLISourceRelease):
		return ocfpCLISourceRelease, nil
	case string(ocfpCLISourceLocal):
		return ocfpCLISourceLocal, nil
	default:
		return "", fmt.Errorf("%w: got %q", ErrOCFPCLISourceInvalid, raw)
	}
}

func resolveOCFPCLIVersion(in ocfpCLIResolveInputs) (string, error) {
	for _, candidate := range []string{in.Getenv(envOCFPCLIVersion), in.Config.Version} {
		if candidate == "" {
			continue
		}

		return normalizeOCFPCLIVersion(candidate)
	}

	// Only a release build has a matching asset to download. Dev and
	// git-describe stamps get the latest release instead.
	operator := strings.TrimPrefix(strings.TrimSpace(in.OperatorVersion), "v")
	if ocfpCLIReleasePattern.MatchString(operator) {
		return operator, nil
	}

	return "", nil
}

// normalizeOCFPCLIVersion strips a leading v, maps the "latest" spelling to
// the empty string the script generator treats as latest, and rejects
// anything that could not be a release tag.
func normalizeOCFPCLIVersion(raw string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")

	if strings.EqualFold(trimmed, ocfpCLIVersionLatest) {
		return "", nil
	}

	if !ocfpCLIVersionPattern.MatchString(trimmed) {
		return "", fmt.Errorf("%w: got %q", ErrOCFPCLIVersionInvalid, raw)
	}

	return trimmed, nil
}
