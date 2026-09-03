package commands

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/security"
)

var (
	validUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-_])*[a-zA-Z0-9]$`)
)

// FetchGitHubKeys fetches SSH keys from GitHub for a user.
func FetchGitHubKeys(ctx context.Context, username string) ([]string, error) {
	err := security.ValidateInput(username, validUsernamePattern)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub username: %w", err)
	}

	url := fmt.Sprintf("https://github.com/%s.keys", username)

	return fetchKeys(ctx, url, "GitHub")
}

// FetchGitLabKeys fetches SSH keys from GitLab for a user.
func FetchGitLabKeys(ctx context.Context, username string) ([]string, error) {
	err := security.ValidateInput(username, validUsernamePattern)
	if err != nil {
		return nil, fmt.Errorf("invalid GitLab username: %w", err)
	}

	url := fmt.Sprintf("https://gitlab.com/%s.keys", username)

	return fetchKeys(ctx, url, "GitLab")
}

// fetchKeys performs the HTTP request and parses newline-delimited SSH keys.
func fetchKeys(ctx context.Context, url, provider string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req) // #nosec -- URL is from trusted provider config
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s keys: %w", provider, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrFailedToFetchKeys(provider, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s keys: %w", provider, err)
	}

	var keys []string

	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key != "" {
			keys = append(keys, key)
		}
	}

	err = scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s keys: %w", provider, err)
	}

	return keys, nil
}
