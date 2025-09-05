package vault

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

// EngineInfo holds information about a vault engine.
type EngineInfo struct {
	Type    string // kv, kv-v2, transit, etc.
	Version string // 1, 2, etc.
	Path    string // mount path
}

// EngineDetector provides engine detection and caching.
type EngineDetector struct {
	client *Client
	cache  map[string]*EngineInfo
	mutex  sync.RWMutex
	logger *zap.SugaredLogger
}

// NewEngineDetector creates a new engine detector.
func NewEngineDetector(client *Client) *EngineDetector {
	return &EngineDetector{
		client: client,
		cache:  make(map[string]*EngineInfo),
		logger: logger.Get(),
	}
}

// DetectEngineForPath detects the engine type for a given vault path.
func (ed *EngineDetector) DetectEngineForPath(path string) (*EngineInfo, error) {
	// Clean the path to get the mount point
	mountPath := ed.extractMountPath(path)

	// Check cache first
	ed.mutex.RLock()

	if info, exists := ed.cache[mountPath]; exists {
		ed.mutex.RUnlock()

		return info, nil
	}

	ed.mutex.RUnlock()

	// Query vault API for mount information
	info, err := ed.queryEngineInfo(mountPath)
	if err != nil {
		return nil, fmt.Errorf("failed to detect engine for path %s: %w", path, err)
	}

	// Cache the result
	ed.mutex.Lock()
	ed.cache[mountPath] = info
	ed.mutex.Unlock()

	ed.logger.Debug("Detected vault engine", "path", path, "mount", mountPath, "type", info.Type, "version", info.Version)

	return info, nil
}

// queryEngineInfo queries the vault API to determine engine information.
func (ed *EngineDetector) queryEngineInfo(mountPath string) (*EngineInfo, error) {
	// Query the sys/mounts endpoint to get mount information
	secret, err := ed.client.logical.Read("sys/mounts")
	if err != nil {
		return nil, fmt.Errorf("failed to read mount information: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return nil, errors.New("no mount information returned")
	}

	// Parse the mount information
	mounts, ok := secret.Data["data"].(map[string]interface{})
	if !ok {
		// Try direct access for older vault versions
		mounts = secret.Data
	}

	// Ensure mountPath ends with /
	if !strings.HasSuffix(mountPath, "/") {
		mountPath += "/"
	}

	// Look for the mount
	if mountInfo, exists := mounts[mountPath]; exists {
		if mountData, ok := mountInfo.(map[string]interface{}); ok {
			return ed.parseMountInfo(mountPath, mountData)
		}
	}

	// If not found in mounts, try to infer from path patterns
	return ed.inferEngineFromPath(mountPath)
}

// parseMountInfo parses mount information from vault API response.
func (ed *EngineDetector) parseMountInfo(mountPath string, mountData map[string]interface{}) (*EngineInfo, error) {
	engineType, _ := mountData["type"].(string)
	options, _ := mountData["options"].(map[string]interface{})

	info := &EngineInfo{
		Path: strings.TrimSuffix(mountPath, "/"),
		Type: engineType,
	}

	switch engineType {
	case "kv":
		// Check version in options
		if version, ok := options["version"]; ok {
			if versionStr, ok := version.(string); ok {
				info.Version = versionStr
				if versionStr == "2" {
					info.Type = "kv-v2"
				} else {
					info.Type = "kv-v1"
				}
			}
		} else {
			// Default to v1 if no version specified
			info.Type = "kv-v1"
			info.Version = "1"
		}
	case "generic":
		// Legacy generic engine is KV v1
		info.Type = "kv-v1"
		info.Version = "1"
	default:
		info.Version = "1" // Default version
	}

	return info, nil
}

// inferEngineFromPath attempts to infer engine type from path patterns.
func (ed *EngineDetector) inferEngineFromPath(mountPath string) (*EngineInfo, error) {
	mountPath = strings.TrimSuffix(mountPath, "/")

	// Common KV v2 mount points
	kvV2Mounts := []string{"secret", "kv"}
	for _, mount := range kvV2Mounts {
		if mountPath == mount {
			ed.logger.Debug("Inferred KV v2 engine from common mount path", "path", mountPath)

			return &EngineInfo{
				Path:    mountPath,
				Type:    "kv-v2",
				Version: "2",
			}, nil
		}
	}

	// Default to KV v1 for unknown mounts
	ed.logger.Debug("Defaulting to KV v1 engine", "path", mountPath)

	return &EngineInfo{
		Path:    mountPath,
		Type:    "kv-v1",
		Version: "1",
	}, nil
}

// extractMountPath extracts the mount path from a full vault path.
func (ed *EngineDetector) extractMountPath(fullPath string) string {
	// Remove leading slash if present
	path := strings.TrimPrefix(fullPath, "/")

	// Split on / and take first component
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[0]
	}

	return path
}

// IsKVv2 checks if a path uses KV v2 engine.
func (ed *EngineDetector) IsKVv2(path string) (bool, error) {
	info, err := ed.DetectEngineForPath(path)
	if err != nil {
		return false, err
	}

	return info.Type == "kv-v2", nil
}

// IsKVv1 checks if a path uses KV v1 engine.
func (ed *EngineDetector) IsKVv1(path string) (bool, error) {
	info, err := ed.DetectEngineForPath(path)
	if err != nil {
		return false, err
	}

	return info.Type == "kv-v1" || info.Type == "generic", nil
}

// GetEngineType returns the engine type for a path.
func (ed *EngineDetector) GetEngineType(path string) (string, error) {
	info, err := ed.DetectEngineForPath(path)
	if err != nil {
		return "", err
	}

	return info.Type, nil
}

// ClearCache clears the engine detection cache.
func (ed *EngineDetector) ClearCache() {
	ed.mutex.Lock()
	defer ed.mutex.Unlock()

	ed.cache = make(map[string]*EngineInfo)
	ed.logger.Debug("Cleared engine detection cache")
}

// GetCachedEngines returns all cached engine information.
func (ed *EngineDetector) GetCachedEngines() map[string]*EngineInfo {
	ed.mutex.RLock()
	defer ed.mutex.RUnlock()

	// Return a copy to prevent external modification
	result := make(map[string]*EngineInfo)
	for k, v := range ed.cache {
		result[k] = &EngineInfo{
			Type:    v.Type,
			Version: v.Version,
			Path:    v.Path,
		}
	}

	return result
}
