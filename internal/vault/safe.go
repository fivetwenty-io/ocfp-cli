package vault

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"

	"github.com/hashicorp/vault/api"
)

// Safe provides operations compatible with the Genesis 'safe' CLI tool
// but uses the HashiCorp Vault API directly.
type Safe struct {
	client *Client
	engine *EngineDetector
	logger *zap.SugaredLogger
}

// NewSafe creates a new Safe wrapper around a vault client.
func NewSafe(client *Client) *Safe {
	return &Safe{
		client: client,
		engine: NewEngineDetector(client),
		logger: logger.Get(),
	}
}

// Set stores a key-value pair at the specified path
// This mimics the behavior of 'safe set path key=value'.
func (s *Safe) Set(path, key string, value interface{}) error {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	s.logger.Debug("Setting vault secret", "path", path, "key", key)

	// Read existing data first to preserve other keys
	existingData := make(map[string]interface{})

	secret, err := s.client.logical.Read(path)
	if err != nil {
		s.logger.Debug("Failed to read existing data (may not exist yet)", "path", path, "error", err)
	} else if secret != nil && secret.Data != nil {
		// Handle both KV v1 and v2 formats
		if data, ok := secret.Data["data"].(map[string]interface{}); ok {
			// KV v2 format
			existingData = data
		} else {
			// KV v1 format
			existingData = secret.Data
		}
	}

	// Update with new key-value
	existingData[key] = value

	// Determine if we're using KV v1 or v2
	var writeData map[string]interface{}

	isKVv2, err := s.engine.IsKVv2(path)
	if err != nil {
		s.logger.Warn("Failed to detect engine type, assuming KV v1", "path", path, "error", err)

		isKVv2 = false
	}

	if isKVv2 {
		// KV v2 format - wrap in "data" field
		writeData = map[string]interface{}{
			"data": existingData,
		}
	} else {
		// KV v1 format - use data directly
		writeData = existingData
	}

	// Write the updated data with retry
	err = RetryableVaultOperation("set", path, key, func() error {
		_, writeErr := s.client.logical.Write(path, writeData)

		return fmt.Errorf("vault write operation failed: %w", writeErr)
	})
	if err != nil {
		return fmt.Errorf("failed to write secret to %s: %w", path, err)
	}

	s.logger.Debug("Successfully set vault secret", "path", path, "key", key)

	return nil
}

// SetMultiple stores multiple key-value pairs at the specified path
// This is more efficient than calling Set multiple times.
func (s *Safe) SetMultiple(path string, data map[string]interface{}) error {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	s.logger.Debug("Setting multiple vault secrets", "path", path, "keys", len(data))

	// Read existing data first to preserve other keys
	existingData := make(map[string]interface{})

	secret, err := s.client.logical.Read(path)
	if err != nil {
		s.logger.Debug("Failed to read existing data (may not exist yet)", "path", path, "error", err)
	} else if secret != nil && secret.Data != nil {
		// Handle both KV v1 and v2 formats
		if secretData, ok := secret.Data["data"].(map[string]interface{}); ok {
			// KV v2 format
			existingData = secretData
		} else {
			// KV v1 format
			existingData = secret.Data
		}
	}

	// Merge with new data
	for key, value := range data {
		existingData[key] = value
	}

	// Determine if we're using KV v1 or v2
	var writeData map[string]interface{}

	isKVv2, err := s.engine.IsKVv2(path)
	if err != nil {
		s.logger.Warn("Failed to detect engine type, assuming KV v1", "path", path, "error", err)

		isKVv2 = false
	}

	if isKVv2 {
		// KV v2 format - wrap in "data" field
		writeData = map[string]interface{}{
			"data": existingData,
		}
	} else {
		// KV v1 format - use data directly
		writeData = existingData
	}

	// Write the updated data with retry
	err = RetryableVaultOperation("set_multiple", path, "", func() error {
		_, writeErr := s.client.logical.Write(path, writeData)

		return fmt.Errorf("vault write operation failed: %w", writeErr)
	})
	if err != nil {
		return fmt.Errorf("failed to write secrets to %s: %w", path, err)
	}

	s.logger.Debug("Successfully set multiple vault secrets", "path", path, "keys", len(data))

	return nil
}

// Get retrieves a value for a specific key at the given path
// This mimics the behavior of 'safe get path:key'.
func (s *Safe) Get(path, key string) (interface{}, error) {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	s.logger.Debug("Getting vault secret", "path", path, "key", key)

	var secret *api.Secret

	err := RetryableVaultOperation("get", path, key, func() error {
		var readErr error

		secret, readErr = s.client.logical.Read(path)

		return fmt.Errorf("vault read operation failed: %w", readErr)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read secret from %s: %w", path, err)
	}

	if secret == nil || secret.Data == nil {
		return nil, ErrNoSecretFoundAtPath(path)
	}

	// Handle both KV v1 and v2 formats
	var data map[string]interface{}
	if secretData, ok := secret.Data["data"].(map[string]interface{}); ok {
		// KV v2 format
		data = secretData
	} else {
		// KV v1 format
		data = secret.Data
	}

	if key == "" {
		// Return all data if no specific key requested
		return data, nil
	}

	value, exists := data[key]
	if !exists {
		return nil, ErrKeyNotFoundAtPath(key, path)
	}

	s.logger.Debug("Successfully retrieved vault secret", "path", path, "key", key)

	return value, nil
}

// GetAll retrieves all key-value pairs at the given path
// This mimics the behavior of 'safe get path'.
func (s *Safe) GetAll(path string) (map[string]interface{}, error) {
	result, err := s.Get(path, "")
	if err != nil {
		return nil, err
	}

	data, ok := result.(map[string]interface{})
	if !ok {
		return nil, ErrUnexpectedDataTypeAtPath(path)
	}

	return data, nil
}

// Exists checks if a path exists in vault.
func (s *Safe) Exists(path string) (bool, error) {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	secret, err := s.client.logical.Read(path)
	if err != nil {
		// If we get a permission denied or similar, the path might exist
		// but we can't read it. For now, treat any error as "doesn't exist"
		s.logger.Debug("Error checking path existence", "path", path, "error", err)

		return false, nil
	}

	return secret != nil, nil
}

// Delete removes a specific key from a path, or the entire path if no key specified.
func (s *Safe) Delete(path, key string) error {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	if key == "" {
		// Delete entire path
		s.logger.Debug("Deleting entire vault path", "path", path)

		_, err := s.client.logical.Delete(path)
		if err != nil {
			return fmt.Errorf("failed to delete path %s: %w", path, err)
		}

		return nil
	}

	// Delete specific key - need to read, modify, and write back
	s.logger.Debug("Deleting vault secret key", "path", path, "key", key)

	data, err := s.GetAll(path)
	if err != nil {
		return fmt.Errorf("failed to read path before deletion: %w", err)
	}

	delete(data, key)

	// Write back the modified data
	return s.SetMultiple(path, data)
}

// List returns all paths under the given path.
func (s *Safe) List(path string) ([]string, error) {
	// Ensure path doesn't start with / but ends with /
	path = strings.TrimPrefix(path, "/")
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	s.logger.Debug("Listing vault paths", "path", path)

	secret, err := s.client.logical.List(path)
	if err != nil {
		return nil, fmt.Errorf("failed to list paths under %s: %w", path, err)
	}

	if secret == nil || secret.Data == nil {
		return []string{}, nil
	}

	keys, ok := secret.Data["keys"].([]interface{})
	if !ok {
		return []string{}, nil
	}

	paths := make([]string, 0, len(keys))
	for _, key := range keys {
		if keyStr, ok := key.(string); ok {
			paths = append(paths, keyStr)
		}
	}

	s.logger.Debug("Successfully listed vault paths", "path", path, "count", len(paths))

	return paths, nil
}

// Export exports all secrets from a path recursively.
func (s *Safe) Export(path string) (map[string]interface{}, error) {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	s.logger.Debug("Exporting vault secrets", "path", path)

	result := make(map[string]interface{})

	err := s.exportRecursive(path, "", result)
	if err != nil {
		return nil, fmt.Errorf("failed to export from %s: %w", path, err)
	}

	s.logger.Debug("Successfully exported vault secrets", "path", path, "entries", len(result))

	return result, nil
}

// Import imports secrets to a path.
func (s *Safe) Import(path string, data map[string]interface{}) error {
	// Ensure path doesn't start with /
	path = strings.TrimPrefix(path, "/")

	s.logger.Debug("Importing vault secrets", "path", path, "entries", len(data))

	for subPath, value := range data {
		fullPath := path
		if subPath != "" {
			fullPath = filepath.Join(path, subPath)
		}

		if valueMap, ok := value.(map[string]interface{}); ok {
			// This is a nested structure, import as multiple keys
			err := s.SetMultiple(fullPath, valueMap)
			if err != nil {
				return fmt.Errorf("failed to import to %s: %w", fullPath, err)
			}
		} else {
			// This is a single value, import as a single key
			err := s.Set(fullPath, "value", value)
			if err != nil {
				return fmt.Errorf("failed to import single value to %s: %w", fullPath, err)
			}
		}
	}

	s.logger.Debug("Successfully imported vault secrets", "path", path, "entries", len(data))

	return nil
}

// GetEngineInfo returns engine information for a path.
func (s *Safe) GetEngineInfo(path string) (*EngineInfo, error) {
	return s.engine.DetectEngineForPath(path)
}

// MustGet is like Get but panics on error (for compatibility with Genesis patterns).
func (s *Safe) MustGet(path, key string) interface{} {
	value, err := s.Get(path, key)
	if err != nil {
		panic(fmt.Sprintf("failed to get %s:%s - %v", path, key, err))
	}

	return value
}

// GetString is a convenience method to get a string value.
func (s *Safe) GetString(path, key string) (string, error) {
	value, err := s.Get(path, key)
	if err != nil {
		return "", err
	}

	if str, ok := value.(string); ok {
		return str, nil
	}

	return "", ErrValueNotStringAtPath(path, key)
}

// GetJSON gets a value and returns it as JSON.
func (s *Safe) GetJSON(path, key string) ([]byte, error) {
	value, err := s.Get(path, key)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value: %w", err)
	}

	return data, nil
}

// exportRecursive recursively exports secrets from a path.
func (s *Safe) exportRecursive(basePath, currentPath string, result map[string]interface{}) error {
	fullPath := basePath
	if currentPath != "" {
		fullPath = filepath.Join(basePath, currentPath)
	}

	// Try to read as a secret first
	data, err := s.GetAll(fullPath)
	if err == nil {
		// This is a secret, store it
		if currentPath == "" {
			// Root level
			for k, v := range data {
				result[k] = v
			}
		} else {
			result[currentPath] = data
		}

		return nil
	}

	// Try to list as a directory
	paths, err := s.List(fullPath)
	if err != nil {
		// Neither a secret nor a directory we can list
		return err
	}

	// Process each sub-path
	for _, subPath := range paths {
		var newCurrentPath string
		if currentPath == "" {
			newCurrentPath = strings.TrimSuffix(subPath, "/")
		} else {
			newCurrentPath = filepath.Join(currentPath, strings.TrimSuffix(subPath, "/"))
		}

		err := s.exportRecursive(basePath, newCurrentPath, result)
		if err != nil {
			return err
		}
	}

	return nil
}
