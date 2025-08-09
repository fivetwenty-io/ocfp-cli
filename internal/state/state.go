package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"gopkg.in/yaml.v3"
)

// State represents the current state of an OCFP environment
type State struct {
	Version      string                 `json:"version" yaml:"version"`
	BlocName     string                 `json:"bloc_name" yaml:"bloc_name"`
	Provider     string                 `json:"provider" yaml:"provider"`
	Region       string                 `json:"region" yaml:"region"`
	CreatedAt    time.Time              `json:"created_at" yaml:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at" yaml:"updated_at"`
	Resources    map[string]*Resource   `json:"resources" yaml:"resources"`
	Outputs      map[string]interface{} `json:"outputs" yaml:"outputs"`
	Dependencies map[string][]string    `json:"dependencies" yaml:"dependencies"`
	mu           sync.RWMutex
}

// Resource represents a managed resource
type Resource struct {
	ID         string                 `json:"id" yaml:"id"`
	Type       string                 `json:"type" yaml:"type"`
	Name       string                 `json:"name" yaml:"name"`
	Provider   string                 `json:"provider" yaml:"provider"`
	State      string                 `json:"state" yaml:"state"`
	Properties map[string]interface{} `json:"properties" yaml:"properties"`
	Tags       map[string]string      `json:"tags" yaml:"tags"`
	CreatedAt  time.Time              `json:"created_at" yaml:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at" yaml:"updated_at"`
}

// Manager handles state persistence and retrieval
type Manager struct {
	stateDir string
	current  *State
	mu       sync.RWMutex
}

// NewManager creates a new state manager
func NewManager(stateDir string) (*Manager, error) {
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		stateDir = filepath.Join(home, ".ocfp", "state")
	}

	// Ensure state directory exists
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &Manager{
		stateDir: stateDir,
	}, nil
}

// Load loads state for a specific bloc
func (m *Manager) Load(blocName string) (*State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	statePath := m.getStatePath(blocName)
	
	// Check if state file exists
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		// Create new state
		state := &State{
			Version:      "1.0",
			BlocName:     blocName,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
			Resources:    make(map[string]*Resource),
			Outputs:      make(map[string]interface{}),
			Dependencies: make(map[string][]string),
		}
		m.current = state
		return state, nil
	}

	// Load existing state
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		// Try YAML format
		if err := yaml.Unmarshal(data, &state); err != nil {
			return nil, fmt.Errorf("failed to parse state file: %w", err)
		}
	}

	m.current = &state
	logger.Debugf("Loaded state for bloc %s with %d resources", blocName, len(state.Resources))
	return &state, nil
}

// Save persists the current state
func (m *Manager) Save() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return fmt.Errorf("no state loaded")
	}

	m.current.UpdatedAt = time.Now()
	
	// Create backup of existing state
	statePath := m.getStatePath(m.current.BlocName)
	if _, err := os.Stat(statePath); err == nil {
		backupPath := statePath + ".backup"
		if err := os.Rename(statePath, backupPath); err != nil {
			logger.Warnf("Failed to create state backup: %v", err)
		}
	}

	// Marshal state to JSON
	data, err := json.MarshalIndent(m.current, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write state file
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	logger.Debugf("Saved state for bloc %s with %d resources", m.current.BlocName, len(m.current.Resources))
	return nil
}

// AddResource adds or updates a resource in the state
func (m *Manager) AddResource(resource *Resource) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return fmt.Errorf("no state loaded")
	}

	if resource.ID == "" {
		return fmt.Errorf("resource ID is required")
	}

	// Generate resource key
	key := fmt.Sprintf("%s.%s", resource.Type, resource.Name)
	
	// Check if resource exists
	if existing, ok := m.current.Resources[key]; ok {
		// Update existing resource
		existing.State = resource.State
		existing.Properties = resource.Properties
		existing.Tags = resource.Tags
		existing.UpdatedAt = time.Now()
		logger.Debugf("Updated resource %s in state", key)
	} else {
		// Add new resource
		resource.CreatedAt = time.Now()
		resource.UpdatedAt = time.Now()
		m.current.Resources[key] = resource
		logger.Debugf("Added resource %s to state", key)
	}

	return nil
}

// RemoveResource removes a resource from the state
func (m *Manager) RemoveResource(resourceType, resourceName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return fmt.Errorf("no state loaded")
	}

	key := fmt.Sprintf("%s.%s", resourceType, resourceName)
	
	if _, ok := m.current.Resources[key]; !ok {
		return fmt.Errorf("resource %s not found", key)
	}

	delete(m.current.Resources, key)
	
	// Remove from dependencies
	delete(m.current.Dependencies, key)
	for k, deps := range m.current.Dependencies {
		filtered := make([]string, 0)
		for _, dep := range deps {
			if dep != key {
				filtered = append(filtered, dep)
			}
		}
		m.current.Dependencies[k] = filtered
	}

	logger.Debugf("Removed resource %s from state", key)
	return nil
}

// GetResource retrieves a resource from the state
func (m *Manager) GetResource(resourceType, resourceName string) (*Resource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return nil, fmt.Errorf("no state loaded")
	}

	key := fmt.Sprintf("%s.%s", resourceType, resourceName)
	resource, ok := m.current.Resources[key]
	if !ok {
		return nil, fmt.Errorf("resource %s not found", key)
	}

	return resource, nil
}

// ListResources returns all resources of a specific type
func (m *Manager) ListResources(resourceType string) ([]*Resource, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return nil, fmt.Errorf("no state loaded")
	}

	var resources []*Resource
	prefix := resourceType + "."
	
	for key, resource := range m.current.Resources {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			resources = append(resources, resource)
		}
	}

	return resources, nil
}

// SetOutput sets an output value
func (m *Manager) SetOutput(key string, value interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return fmt.Errorf("no state loaded")
	}

	m.current.Outputs[key] = value
	logger.Debugf("Set output %s in state", key)
	return nil
}

// GetOutput retrieves an output value
func (m *Manager) GetOutput(key string) (interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return nil, fmt.Errorf("no state loaded")
	}

	value, ok := m.current.Outputs[key]
	if !ok {
		return nil, fmt.Errorf("output %s not found", key)
	}

	return value, nil
}

// AddDependency adds a dependency between resources
func (m *Manager) AddDependency(resource, dependsOn string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil {
		return fmt.Errorf("no state loaded")
	}

	if m.current.Dependencies[resource] == nil {
		m.current.Dependencies[resource] = make([]string, 0)
	}

	// Check if dependency already exists
	for _, dep := range m.current.Dependencies[resource] {
		if dep == dependsOn {
			return nil
		}
	}

	m.current.Dependencies[resource] = append(m.current.Dependencies[resource], dependsOn)
	logger.Debugf("Added dependency %s -> %s", resource, dependsOn)
	return nil
}

// GetDependencies returns dependencies for a resource
func (m *Manager) GetDependencies(resource string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.current == nil {
		return nil, fmt.Errorf("no state loaded")
	}

	deps, ok := m.current.Dependencies[resource]
	if !ok {
		return []string{}, nil
	}

	return deps, nil
}

// Lock acquires an exclusive lock on the state
func (m *Manager) Lock(blocName string) error {
	lockPath := m.getLockPath(blocName)
	
	// Check if lock exists
	if _, err := os.Stat(lockPath); err == nil {
		// Read lock info
		data, err := os.ReadFile(lockPath)
		if err != nil {
			return fmt.Errorf("failed to read lock file: %w", err)
		}
		
		var lockInfo map[string]interface{}
		if err := json.Unmarshal(data, &lockInfo); err == nil {
			return fmt.Errorf("state is locked by %v at %v", 
				lockInfo["owner"], lockInfo["created_at"])
		}
		
		return fmt.Errorf("state is locked")
	}

	// Create lock file
	lockInfo := map[string]interface{}{
		"owner":      os.Getenv("USER"),
		"hostname":   getHostname(),
		"pid":        os.Getpid(),
		"created_at": time.Now(),
	}

	data, err := json.MarshalIndent(lockInfo, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to create lock info: %w", err)
	}

	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}

	logger.Debugf("Acquired state lock for bloc %s", blocName)
	return nil
}

// Unlock releases the lock on the state
func (m *Manager) Unlock(blocName string) error {
	lockPath := m.getLockPath(blocName)
	
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}

	logger.Debugf("Released state lock for bloc %s", blocName)
	return nil
}

// getStatePath returns the path to the state file
func (m *Manager) getStatePath(blocName string) string {
	return filepath.Join(m.stateDir, fmt.Sprintf("%s.json", blocName))
}

// getLockPath returns the path to the lock file
func (m *Manager) getLockPath(blocName string) string {
	return filepath.Join(m.stateDir, fmt.Sprintf("%s.lock", blocName))
}

// getHostname returns the hostname
func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}

// Current returns the current state
func (m *Manager) Current() *State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Clear clears the current state from memory
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = nil
}