package state_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvider implements cpi.Provider for testing.
type mockProvider struct {
	name   string
	region string
}

func (m *mockProvider) Name() string                                               { return m.name }
func (m *mockProvider) Region() string                                             { return m.region }
func (m *mockProvider) Authenticate(_ctx context.Context) error                    { return nil }
func (m *mockProvider) ValidateCredentials(_ctx context.Context) error             { return nil }
func (m *mockProvider) NetworkManager() cpi.NetworkManager                         { return nil }
func (m *mockProvider) ComputeManager() cpi.ComputeManager                         { return nil }
func (m *mockProvider) StorageManager() cpi.StorageManager                         { return nil }
func (m *mockProvider) SecurityManager() cpi.SecurityManager                       { return nil }
func (m *mockProvider) LoadBalancerManager() cpi.LoadBalancerManager               { return nil }
func (m *mockProvider) Network() cpi.NetworkManager                                { return nil }
func (m *mockProvider) Compute() cpi.ComputeManager                                { return nil }
func (m *mockProvider) Storage() cpi.StorageManager                                { return nil }
func (m *mockProvider) Security() cpi.SecurityManager                              { return nil }
func (m *mockProvider) LoadBalancer() cpi.LoadBalancerManager                      { return nil }
func (m *mockProvider) SupportsStorage() bool                                      { return true }
func (m *mockProvider) Initialize(_ctx context.Context, _config interface{}) error { return nil }
func (m *mockProvider) Cleanup(_ctx context.Context) error                         { return nil }

func TestNewReconciler(t *testing.T) {
	tests := []struct {
		name        string
		provider    cpi.Provider
		manager     *state.Manager
		blocName    string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid reconciler creation",
			provider:    &mockProvider{name: "test", region: "us-east-1"},
			manager:     mustCreateTestManager(t),
			blocName:    "test-bloc",
			expectError: false,
		},
		{
			name:        "nil provider",
			provider:    nil,
			manager:     mustCreateTestManager(t),
			blocName:    "test-bloc",
			expectError: true,
			errorMsg:    "provider cannot be nil",
		},
		{
			name:        "nil state manager",
			provider:    &mockProvider{name: "test", region: "us-east-1"},
			manager:     nil,
			blocName:    "test-bloc",
			expectError: true,
			errorMsg:    "state manager cannot be nil",
		},
		{
			name:        "empty bloc name",
			provider:    &mockProvider{name: "test", region: "us-east-1"},
			manager:     mustCreateTestManager(t),
			blocName:    "",
			expectError: true,
			errorMsg:    "bloc name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, err := state.NewReconciler(tt.provider, tt.manager, tt.blocName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, reconciler)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, reconciler)
			}
		})
	}
}

func TestReconcile_BasicFlow(t *testing.T) {
	// Setup
	provider := &mockProvider{name: "test-provider", region: "test-region"}
	manager := mustCreateTestManager(t)
	blocName := "test-bloc"

	reconciler, err := state.NewReconciler(provider, manager, blocName)
	require.NoError(t, err)

	ctx := context.Background()

	// Test dry-run mode
	t.Run("dry run mode", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyAddOnly,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 0, result.ResourcesAdded)
		assert.Equal(t, 0, result.ResourcesUpdated)
		assert.Equal(t, 0, result.ResourcesRemoved)
		assert.Greater(t, result.Duration.Nanoseconds(), int64(0))
	})

	// Test different merge strategies
	t.Run("add-only strategy", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyAddOnly,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("update strategy", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyUpdate,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("full strategy", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyFull,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})
}

func TestValidateProvider(t *testing.T) {
	provider := &mockProvider{name: "test", region: "test-region"}
	manager := mustCreateTestManager(t)
	blocName := "test-bloc"

	reconciler, err := state.NewReconciler(provider, manager, blocName)
	require.NoError(t, err)

	ctx := context.Background()
	err = reconciler.ValidateProvider(ctx)
	assert.NoError(t, err)
}

func TestMergeStrategy_String(t *testing.T) {
	tests := []struct {
		strategy state.MergeStrategy
		expected string
	}{
		{state.MergeStrategyAddOnly, "add-only"},
		{state.MergeStrategyUpdate, "update"},
		{state.MergeStrategyFull, "full"},
		{state.MergeStrategy(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.strategy.String())
		})
	}
}

func TestParseMergeStrategy(t *testing.T) {
	tests := []struct {
		input       string
		expected    state.MergeStrategy
		expectError bool
	}{
		{"add-only", state.MergeStrategyAddOnly, false},
		{"update", state.MergeStrategyUpdate, false},
		{"full", state.MergeStrategyFull, false},
		{"invalid", state.MergeStrategyAddOnly, true},
		{"", state.MergeStrategyAddOnly, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			strategy, err := state.ParseMergeStrategy(tt.input)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid merge strategy")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, strategy)
			}
		})
	}
}

// TestReconcile_NonDryRun exercises the DryRun=false path so that
// mergeChanges and saveState are both called and verified.
func TestReconcile_NonDryRun(t *testing.T) {
	ctx := context.Background()

	t.Run("add-only non-dry-run writes state file", func(t *testing.T) {
		tmpDir := t.TempDir()
		manager, err := state.NewManager(tmpDir)
		require.NoError(t, err)

		provider := &mockProvider{name: "test-provider", region: "test-region"}
		blocName := "test-bloc"

		reconciler, err := state.NewReconciler(provider, manager, blocName)
		require.NoError(t, err)

		opts := state.ReconcileOptions{
			DryRun:   false,
			Strategy: state.MergeStrategyAddOnly,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		require.NotNil(t, result)

		// mergeChanges was called: result counts reflect merge output (0 from empty diff)
		assert.Equal(t, 0, result.ResourcesAdded)
		assert.Equal(t, 0, result.ResourcesUpdated)
		assert.Equal(t, 0, result.ResourcesRemoved)

		// saveState was called: state file must exist on disk
		stateFile := filepath.Join(tmpDir, blocName+".json")
		_, statErr := os.Stat(stateFile)
		assert.NoError(t, statErr, "state file should exist after non-dry-run reconcile")
	})

	t.Run("update non-dry-run writes state file", func(t *testing.T) {
		tmpDir := t.TempDir()
		manager, err := state.NewManager(tmpDir)
		require.NoError(t, err)

		provider := &mockProvider{name: "aws", region: "eu-west-1"}
		blocName := "update-bloc"

		reconciler, err := state.NewReconciler(provider, manager, blocName)
		require.NoError(t, err)

		opts := state.ReconcileOptions{
			DryRun:   false,
			Strategy: state.MergeStrategyUpdate,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		require.NotNil(t, result)

		stateFile := filepath.Join(tmpDir, blocName+".json")
		_, statErr := os.Stat(stateFile)
		assert.NoError(t, statErr, "state file should exist after update strategy non-dry-run reconcile")
	})

	t.Run("full non-dry-run writes state file", func(t *testing.T) {
		tmpDir := t.TempDir()
		manager, err := state.NewManager(tmpDir)
		require.NoError(t, err)

		provider := &mockProvider{name: "gcp", region: "us-central1"}
		blocName := "full-bloc"

		reconciler, err := state.NewReconciler(provider, manager, blocName)
		require.NoError(t, err)

		opts := state.ReconcileOptions{
			DryRun:   false,
			Strategy: state.MergeStrategyFull,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		require.NotNil(t, result)

		stateFile := filepath.Join(tmpDir, blocName+".json")
		_, statErr := os.Stat(stateFile)
		assert.NoError(t, statErr, "state file should exist after full strategy non-dry-run reconcile")
	})

	t.Run("non-dry-run loaded state matches reconciled content", func(t *testing.T) {
		tmpDir := t.TempDir()
		manager, err := state.NewManager(tmpDir)
		require.NoError(t, err)

		provider := &mockProvider{name: "stackit", region: "eu-de-1"}
		blocName := "roundtrip-bloc"

		reconciler, err := state.NewReconciler(provider, manager, blocName)
		require.NoError(t, err)

		opts := state.ReconcileOptions{
			DryRun:   false,
			Strategy: state.MergeStrategyAddOnly,
		}

		_, err = reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)

		// Load state via a fresh manager to confirm file is valid and readable.
		manager2, err := state.NewManager(tmpDir)
		require.NoError(t, err)

		loaded, err := manager2.Load(blocName)
		require.NoError(t, err)

		assert.Equal(t, blocName, loaded.BlocName)
		assert.Equal(t, "stackit", loaded.Provider)
		assert.Equal(t, "eu-de-1", loaded.Region)
	})
}

// mustCreateTestManager creates a test state manager in a per-test temp directory.
func mustCreateTestManager(t *testing.T) *state.Manager {
	t.Helper()

	manager, err := state.NewManager(t.TempDir())
	require.NoError(t, err)

	return manager
}
