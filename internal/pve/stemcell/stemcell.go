// Package stemcell provides idempotent BOSH stemcell upload helpers for PVE environments.
//
// Dependency injection (RunBosh, SHA1Fetcher) keeps tests hermetic — no shell-out required.
package stemcell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunBosh executes a bosh CLI command and returns its combined stdout output.
// ctx is propagated to exec.CommandContext so Ctrl-C cancels the subprocess.
// The caller is responsible for injecting the BOSH environment flag (e.g. -e <env>).
type RunBosh func(ctx context.Context, args ...string) ([]byte, error)

// SHA1Fetcher resolves the regular sha1 for the given stemcell name+version from
// an authoritative source (typically bosh.io).
type SHA1Fetcher func(ctx context.Context, name, version string) (string, error)

// boshStemcellsJSON is the top-level structure returned by `bosh stemcells --json`.
type boshStemcellsJSON struct {
	Tables []struct {
		Rows []map[string]string `json:"Rows"`
	} `json:"Tables"`
}

// boshIOStemcell is one entry from https://bosh.io/api/v1/stemcells/<name>.
type boshIOStemcell struct {
	Version string `json:"version"`
	Regular struct {
		SHA1 string `json:"sha1"`
		URL  string `json:"url"`
	} `json:"regular"`
}

// DefaultHTTPClient is a pre-configured http.Client with a 30-second timeout suitable
// for bosh.io API calls. Callers may substitute their own for testing.
var DefaultHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// IsStemcellUploaded returns true when a stemcell matching both name and version is
// present in the BOSH director's stemcell list.
//
// Inputs:
//   - ctx: cancelled contexts propagate into RunBosh error handling.
//   - runBosh: injected executor; called with ["stemcells", "--json"].
//   - name: full stemcell name, e.g. "bosh-openstack-kvm-ubuntu-noble-go_agent".
//   - version: exact version string to match, e.g. "1.584".
//
// Failure modes:
//   - runBosh error → wrapped error returned.
//   - malformed JSON → wrapped json.Unmarshal error returned.
//   - empty Tables or Rows → returns false (not an error; director has no stemcells).
func IsStemcellUploaded(ctx context.Context, runBosh RunBosh, name, version string) (bool, error) {
	if name == "" {
		return false, errors.New("stemcell: name must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	if version == "" {
		return false, errors.New("stemcell: version must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	out, err := runBosh(ctx, "stemcells", "--json")
	if err != nil {
		return false, fmt.Errorf("stemcell: bosh stemcells --json: %w", err)
	}

	var parsed boshStemcellsJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false, fmt.Errorf("stemcell: parse bosh stemcells output: %w", err)
	}

	for _, table := range parsed.Tables {
		for _, row := range table.Rows {
			// BOSH JSON output uses column keys "Name" and "Version".
			// Version may include a trailing "*" on the active stemcell; strip it.
			rowName := strings.TrimSpace(row["Name"])

			rowVersion := strings.TrimSpace(strings.TrimSuffix(row["Version"], "*"))
			if rowName == name && rowVersion == version {
				return true, nil
			}
		}
	}

	return false, nil
}

// FetchSHA1 queries the bosh.io stemcell API and returns the regular SHA1 checksum
// for the given stemcell name and version.
//
// API endpoint: GET https://bosh.io/api/v1/stemcells/<name>
// Response: JSON array of { version, regular: { sha1, url } }.
//
// Inputs:
//   - ctx: attached to the HTTP request; cancellation aborts the fetch.
//   - httpClient: injected HTTP client; use DefaultHTTPClient in production.
//   - name: full stemcell name.
//   - version: exact version to look up.
//
// Failure modes:
//   - name or version empty → error.
//   - HTTP non-2xx → error including status code.
//   - body read error → wrapped IO error.
//   - JSON parse error → wrapped parse error.
//   - version not found in response → descriptive error.
//   - sha1 field empty → descriptive error (version entry present but no regular build).
func FetchSHA1(ctx context.Context, httpClient *http.Client, name, version string) (string, error) {
	if name == "" {
		return "", errors.New("stemcell: name must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	if version == "" {
		return "", errors.New("stemcell: version must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	if httpClient == nil {
		return "", errors.New("stemcell: httpClient must not be nil") //nolint:err113 // descriptive error, not caller-testable
	}

	apiURL := "https://bosh.io/api/v1/stemcells/" + name

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("stemcell: build request for %s: %w", apiURL, err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stemcell: GET %s: %w", apiURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("stemcell: GET %s returned HTTP %d", apiURL, resp.StatusCode) //nolint:err113 // descriptive error, not caller-testable
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("stemcell: read response from %s: %w", apiURL, err)
	}

	var entries []boshIOStemcell
	if err := json.Unmarshal(body, &entries); err != nil {
		return "", fmt.Errorf("stemcell: parse bosh.io response for %s: %w", name, err)
	}

	for _, entry := range entries {
		if entry.Version == version {
			if entry.Regular.SHA1 == "" {
				return "", fmt.Errorf("stemcell: version %s of %s has no regular.sha1 in bosh.io response", version, name) //nolint:err113 // descriptive error, not caller-testable
			}

			return entry.Regular.SHA1, nil
		}
	}

	return "", fmt.Errorf("stemcell: version %s of %s not found in bosh.io response (%d entries)", version, name, len(entries)) //nolint:err113 // descriptive error, not caller-testable
}

// EnsureStemcell performs an idempotent stemcell upload:
//  1. Calls IsStemcellUploaded; returns nil immediately when already present.
//  2. Calls fetchSHA1 to retrieve the integrity checksum.
//  3. Runs `bosh upload-stemcell --sha1 <sha1> <url>`.
//
// Inputs:
//   - ctx: propagated to IsStemcellUploaded and fetchSHA1.
//   - runBosh: injected executor; called for stemcell check and (conditionally) upload.
//   - fetchSHA1: injected SHA1 resolver; called only when upload is needed.
//   - name: full stemcell name.
//   - version: version string.
//   - url: download URL for the stemcell tarball.
//
// Failure modes:
//   - name, version, or url empty → error before any bosh call.
//   - IsStemcellUploaded error → propagated.
//   - fetchSHA1 error → propagated.
//   - upload runBosh error → propagated.
func EnsureStemcell(ctx context.Context, runBosh RunBosh, fetchSHA1 SHA1Fetcher, name, version, url string) error {
	if name == "" {
		return errors.New("stemcell: name must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	if version == "" {
		return errors.New("stemcell: version must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	if url == "" {
		return errors.New("stemcell: url must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	uploaded, err := IsStemcellUploaded(ctx, runBosh, name, version)
	if err != nil {
		return fmt.Errorf("stemcell: check existing stemcells: %w", err)
	}

	if uploaded {
		return nil
	}

	sha1, err := fetchSHA1(ctx, name, version)
	if err != nil {
		return fmt.Errorf("stemcell: fetch sha1 for %s@%s: %w", name, version, err)
	}

	if _, err := runBosh(ctx, "upload-stemcell", "--sha1", sha1, url); err != nil {
		return fmt.Errorf("stemcell: bosh upload-stemcell %s@%s: %w", name, version, err)
	}

	return nil
}
