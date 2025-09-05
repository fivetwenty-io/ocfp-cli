package commands

import (
	"bufio"
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

// fetchGitHubKeys fetches SSH keys from GitHub for a user.
func fetchGitHubKeys(username string) ([]string, error) {
	if err := security.ValidateInput(username, validUsernamePattern); err != nil {
		return nil, fmt.Errorf("invalid GitHub username: %w", err)
	}

	url := fmt.Sprintf("https://github.com/%s.keys", username)

	resp, err := http.Get(url) // #nosec G107 - URL constructed from validated username
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GitHub keys: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch GitHub keys: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GitHub keys: %w", err)
	}

	var keys []string

	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key != "" {
			keys = append(keys, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to fetch GitHub keys: %w", err)
	}

	return keys, nil
}

// fetchGitLabKeys fetches SSH keys from GitLab for a user.
func fetchGitLabKeys(username string) ([]string, error) {
	if err := security.ValidateInput(username, validUsernamePattern); err != nil {
		return nil, fmt.Errorf("invalid GitLab username: %w", err)
	}

	url := fmt.Sprintf("https://gitlab.com/%s.keys", username)

	resp, err := http.Get(url) // #nosec G107 - URL constructed from validated username
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GitLab keys: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch GitLab keys: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch GitLab keys: %w", err)
	}

	var keys []string

	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		key := strings.TrimSpace(scanner.Text())
		if key != "" {
			keys = append(keys, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to fetch GitLab keys: %w", err)
	}

	return keys, nil
}
