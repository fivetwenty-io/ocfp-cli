package cpi

import (
	"context"
	"fmt"
	"sync"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// ProviderFactory is a function that creates a new provider instance.
type ProviderFactory func(config interface{}) (Provider, error)

// Registry manages provider registrations.
type Registry struct {
	providers map[string]ProviderFactory
	mu        sync.RWMutex
}

// globalRegistry is the global provider registry.
var globalRegistry = &Registry{ //nolint:gochecknoglobals // singleton registry for provider factories
    providers: make(map[string]ProviderFactory),
}

// Register registers a provider factory.
func Register(name string, factory ProviderFactory) error {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	if _, exists := globalRegistry.providers[name]; exists {
		return fmt.Errorf("provider %s already registered", name)
	}

	globalRegistry.providers[name] = factory
	logger.Debugf("Registered provider: %s", name)

	return nil
}

// Get retrieves a provider factory.
func Get(name string) (ProviderFactory, error) {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	factory, exists := globalRegistry.providers[name]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", name)
	}

	return factory, nil
}

// GetProvider creates a new provider instance by name.
//nolint:ireturn // returning Provider interface is intentional for registry API
func GetProvider(name string) (Provider, error) {
	factory, err := Get(name)
	if err != nil {
		return nil, err
	}

	// For now, pass nil config - the provider will be initialized separately
	return factory(nil)
}

// List returns all registered provider names.
func List() []string {
	globalRegistry.mu.RLock()
	defer globalRegistry.mu.RUnlock()

	names := make([]string, 0, len(globalRegistry.providers))
	for name := range globalRegistry.providers {
		names = append(names, name)
	}

	return names
}

// CreateProvider creates a provider instance by name.
//nolint:ireturn // returning Provider interface is intentional for registry API
func CreateProvider(ctx context.Context, name string, config interface{}) (Provider, error) {
	factory, err := Get(name)
	if err != nil {
		return nil, err
	}

	provider, err := factory(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider %s: %w", name, err)
	}

	// Initialize the provider
	if err := provider.Initialize(ctx, config); err != nil {
		return nil, fmt.Errorf("failed to initialize provider %s: %w", name, err)
	}

	logger.Infof("Created provider: %s", name)

	return provider, nil
}

// ProviderConfig represents generic provider configuration.
type ProviderConfig struct {
	Type      string                 `mapstructure:"type"`
	Region    string                 `mapstructure:"region"`
	ProjectID string                 `mapstructure:"project_id"`
	Auth      map[string]interface{} `mapstructure:"auth"`
	Settings  map[string]interface{} `mapstructure:"settings"`
}
