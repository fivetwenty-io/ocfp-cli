package provision

import (
	"strings"
)

const (
	// ocfpReleaseRepo is the GitHub repository that publishes ocfp releases.
	ocfpReleaseRepo = "fivetwenty-io/ocfp-cli"
	// ocfpReleaseInstallPath is where the bastion expects the ocfp binary. The
	// locators in directories.go and ocfp.go both search this path, so the
	// release install must land here rather than the /usr/bin the deb and rpm
	// packages would use.
	ocfpReleaseInstallPath = "/usr/local/bin/ocfp"
)

// GenerateOCFPReleaseInstallScript returns a bash fragment that installs the
// ocfp CLI on the bastion from a published GitHub release. An empty version
// resolves the latest release through the GitHub API; otherwise the exact
// version is fetched. The script runs inside the phase preamble, so it can
// rely on set -euo pipefail, the log_* helpers, and an exported GITHUB_TOKEN.
//
// The tarball is used instead of the deb or rpm so the binary lands at
// /usr/local/bin/ocfp where every bastion locator already looks. The archive
// checksum is verified against the release's SHA256SUMS before anything is
// installed. Unless force is set, an install whose `ocfp version` banner
// already reports the wanted version is skipped so re-runs stay cheap.
func GenerateOCFPReleaseInstallScript(version string, force bool) string {
	lines := make([]string, 0, scriptBufferBase)

	lines = append(lines,
		"# OCFP CLI release install",
		"",
		`OCFP_INSTALL_PATH="`+ocfpReleaseInstallPath+`"`,
		"",
	)

	lines = append(lines, ocfpReleaseVersionLines(version)...)
	lines = append(lines,
		"",
		"# Map the bastion architecture onto the release asset naming",
		`case "$(uname -m)" in`,
		`    x86_64) OCFP_ARCH="amd64" ;;`,
		`    aarch64) OCFP_ARCH="arm64" ;;`,
		`    arm64) OCFP_ARCH="arm64" ;;`,
		`    *) log_error "unsupported architecture for the ocfp release: $(uname -m)"; exit 1 ;;`,
		"esac",
		"",
	)

	if force {
		lines = append(lines, `log_info "Forcing reinstall of ocfp ${OCFP_VERSION}"`)
	} else {
		lines = append(lines,
			`if [ -x "${OCFP_INSTALL_PATH}" ] && "${OCFP_INSTALL_PATH}" version 2>/dev/null | grep -q "OCFP CLI v${OCFP_VERSION} "; then`,
			`    log_info "ocfp ${OCFP_VERSION} is already installed at ${OCFP_INSTALL_PATH}"`,
			"else",
		)
	}

	lines = append(lines, ocfpReleaseDownloadLines()...)

	if !force {
		lines = append(lines, "fi")
	}

	lines = append(lines,
		"",
		`"${OCFP_INSTALL_PATH}" version`,
	)

	return strings.Join(lines, "\n")
}

// GenerateOCFPCompletionsScript returns a bash fragment that writes the ocfp
// shell completions from the installed binary into the system completion
// directories. It works for a release download and a local upload alike, and
// re-running it simply refreshes the files.
func GenerateOCFPCompletionsScript() string {
	return strings.Join([]string{
		"# OCFP CLI shell completions",
		"",
		`OCFP_INSTALL_PATH="` + ocfpReleaseInstallPath + `"`,
		`if [ ! -x "${OCFP_INSTALL_PATH}" ]; then`,
		`    log_warning "ocfp is not installed at ${OCFP_INSTALL_PATH}; skipping shell completions"`,
		"else",
		"    sudo mkdir -p /usr/share/bash-completion/completions /usr/share/zsh/site-functions /usr/share/fish/vendor_completions.d",
		`    "${OCFP_INSTALL_PATH}" completion bash | sudo tee /usr/share/bash-completion/completions/ocfp >/dev/null`,
		`    "${OCFP_INSTALL_PATH}" completion zsh | sudo tee /usr/share/zsh/site-functions/_ocfp >/dev/null`,
		`    "${OCFP_INSTALL_PATH}" completion fish | sudo tee /usr/share/fish/vendor_completions.d/ocfp.fish >/dev/null`,
		`    log_success "Installed ocfp shell completions"`,
		"fi",
	}, "\n")
}

// ocfpReleaseVersionLines sets OCFP_VERSION either from the pinned value or
// from the latest release the GitHub API reports. The API response is parsed
// with sed rather than jq, because jq arrives in a later phase and is absent
// on a fresh bastion under `init bastion --ocfp`.
func ocfpReleaseVersionLines(version string) []string {
	if version != "" {
		return []string{
			"OCFP_VERSION='" + version + "'",
			`log_info "Using configured ocfp version: ${OCFP_VERSION}"`,
		}
	}

	const (
		latestURL = "https://api.github.com/repos/" + ocfpReleaseRepo + "/releases/latest"
		tagSed    = `sed -n 's/.*"tag_name":[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1`
	)

	return []string{
		"# Use the GitHub token when present so the API is not rate-limited",
		`GITHUB_AUTH_HEADER=""`,
		`if [ -n "${GITHUB_TOKEN:-}" ]; then`,
		`    GITHUB_AUTH_HEADER="Authorization: token ${GITHUB_TOKEN}"`,
		"fi",
		"",
		`OCFP_VERSION=$(curl -fsSL -H "${GITHUB_AUTH_HEADER}" '` + latestURL + `' | ` + tagSed + ` || true)`,
		`if [ -z "${OCFP_VERSION}" ] && [ -n "${GITHUB_AUTH_HEADER}" ]; then`,
		"    # A stale token fails where an anonymous request would succeed",
		`    log_warning "GitHub API request with GITHUB_TOKEN failed; retrying anonymously"`,
		`    OCFP_VERSION=$(curl -fsSL '` + latestURL + `' | ` + tagSed + ` || true)`,
		"fi",
		`if [ -z "${OCFP_VERSION}" ]; then`,
		`    log_error "Failed to resolve the latest ocfp release (the GitHub API may be rate-limited; set GITHUB_TOKEN or pin OCFP_CLI_VERSION)"`,
		"    exit 1",
		"fi",
		`log_info "Latest ocfp release: ${OCFP_VERSION}"`,
	}
}

// ocfpReleaseDownloadLines fetches, verifies, and installs the release
// tarball. Every line is indented for the else branch it may live in.
func ocfpReleaseDownloadLines() []string {
	return []string{
		`    OCFP_ASSET="ocfp_${OCFP_VERSION}_linux_${OCFP_ARCH}.tar.gz"`,
		`    OCFP_SUMS="ocfp_${OCFP_VERSION}_SHA256SUMS"`,
		`    OCFP_BASE_URL="https://github.com/` + ocfpReleaseRepo + `/releases/download/v${OCFP_VERSION}/"`,
		`    OCFP_TMP="$(mktemp -d)"`,
		`    trap 'rm -rf "${OCFP_TMP}"' EXIT`,
		"",
		`    log_info "Downloading ${OCFP_BASE_URL}${OCFP_ASSET}"`,
		`    curl -fsSL "${OCFP_BASE_URL}${OCFP_ASSET}" -o "${OCFP_TMP}/${OCFP_ASSET}"`,
		`    curl -fsSL "${OCFP_BASE_URL}${OCFP_SUMS}" -o "${OCFP_TMP}/${OCFP_SUMS}"`,
		`    (cd "${OCFP_TMP}" && grep " ${OCFP_ASSET}$" "${OCFP_SUMS}" | sha256sum -c --quiet -)`,
		`    log_success "Checksum verified for ${OCFP_ASSET}"`,
		"",
		`    mkdir -p "${OCFP_TMP}/extract"`,
		`    tar --no-same-owner --no-same-permissions -C "${OCFP_TMP}/extract" -xzf "${OCFP_TMP}/${OCFP_ASSET}"`,
		`    sudo install -m 0755 "${OCFP_TMP}/extract/ocfp" "${OCFP_INSTALL_PATH}"`,
		`    rm -rf "${OCFP_TMP}"`,
		`    log_success "Installed ocfp ${OCFP_VERSION} to ${OCFP_INSTALL_PATH}"`,
	}
}
