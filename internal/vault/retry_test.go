package vault_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultRetryConfigDefaults asserts the defaults are sane and match the constants.
func TestDefaultRetryConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := vault.DefaultRetryConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, vault.VaultMaxAttempts, cfg.MaxAttempts)
	assert.Equal(t, 1*time.Second, cfg.BaseDelay)
	assert.Equal(t, vault.VaultMaxDelaySec*time.Second, cfg.MaxDelay)
	assert.Equal(t, vault.VaultBackoffFactor, cfg.BackoffFactor)
	assert.Greater(t, cfg.MaxAttempts, 0, "MaxAttempts must be positive")
	assert.Greater(t, cfg.BaseDelay, time.Duration(0), "BaseDelay must be positive")
	assert.GreaterOrEqual(t, cfg.MaxDelay, cfg.BaseDelay, "MaxDelay must be >= BaseDelay")
	assert.Greater(t, cfg.BackoffFactor, 0.0, "BackoffFactor must be positive")
}

// TestNewOperationError covers field assignment and IsRetryable branching.
func TestNewOperationError(t *testing.T) {
	t.Parallel()

	t.Run("retryable error", func(t *testing.T) {
		t.Parallel()

		err := vault.NewOperationError("read", "secret/foo", "key", vault.ErrConnectionTimeout)
		require.NotNil(t, err)
		assert.Equal(t, "read", err.Operation)
		assert.Equal(t, "secret/foo", err.Path)
		assert.Equal(t, "key", err.Key)
		assert.True(t, err.IsRetryable())
		assert.Contains(t, err.Error(), "read")
		assert.Contains(t, err.Error(), "secret/foo")
		assert.Contains(t, err.Error(), "key")
	})

	t.Run("non-retryable error", func(t *testing.T) {
		t.Parallel()

		err := vault.NewOperationError("write", "secret/bar", "", vault.ErrAccessDenied)
		require.NotNil(t, err)
		assert.False(t, err.IsRetryable())
		assert.Contains(t, err.Error(), "write")
		assert.Contains(t, err.Error(), "secret/bar")
	})

	t.Run("path only no key separator in message", func(t *testing.T) {
		t.Parallel()

		err := vault.NewOperationError("list", "secret/baz", "", errors.New("connection refused"))
		require.NotNil(t, err)
		assert.Contains(t, err.Error(), "secret/baz")
	})

	t.Run("no path no key in message", func(t *testing.T) {
		t.Parallel()

		err := vault.NewOperationError("check", "", "", errors.New("connection refused"))
		require.NotNil(t, err)
		assert.Contains(t, err.Error(), "check")
	})

	t.Run("unwrap returns inner error", func(t *testing.T) {
		t.Parallel()

		inner := errors.New("some inner error")
		opErr := vault.NewOperationError("op", "", "", inner)
		assert.Equal(t, inner, errors.Unwrap(opErr))
	})
}

// TestIsRetryableEdgeCases covers exact-match, prefix/suffix, and nil.
func TestIsRetryableEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		msg       string
		retryable bool
	}{
		{"exact connection refused", "connection refused", true},
		{"prefix context", "context deadline exceeded", true},
		{"rate limit substring", "too many requests right now", true},
		{"502 bad gateway", "502 Bad Gateway", true},
		{"503 service unavailable", "503 Service Unavailable", true},
		{"504 gateway timeout", "504 Gateway Timeout", true},
		{"429 too many requests", "429 Too Many Requests", true},
		{"500 internal server error", "500 Internal Server Error", true},
		{"access denied not retryable", "access denied", false},
		{"permission denied not retryable", "permission denied", false},
		{"not found not retryable", "not found", false},
		{"unrelated message not retryable", "something completely different", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := fmt.Errorf("%s", tt.msg)
			assert.Equal(t, tt.retryable, vault.IsRetryable(err), "msg=%q", tt.msg)
		})
	}

	// nil handled separately (not a fmt.Errorf case)
	t.Run("nil error", func(t *testing.T) {
		t.Parallel()

		assert.False(t, vault.IsRetryable(nil))
	})
}

// TestRetryableVaultOperation groups all retry-seam tests serially to avoid
// data races on the package-level sleepFn variable.
func TestRetryableVaultOperation(t *testing.T) {
	// NOT parallel: these tests mutate the package-level sleepFn.

	t.Run("success on first try", func(t *testing.T) {
		restore := vault.SetSleepFn(func(time.Duration) {})
		defer restore()

		called := 0
		err := vault.RetryableVaultOperation("read", "secret/x", "k", func() error {
			called++
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 1, called)
	})

	t.Run("success after N retries", func(t *testing.T) {
		restore := vault.SetSleepFn(func(time.Duration) {})
		defer restore()

		called := 0
		err := vault.RetryableVaultOperation("read", "secret/x", "k", func() error {
			called++
			if called < 3 {
				return fmt.Errorf("connection refused attempt %d", called)
			}
			return nil
		})

		require.NoError(t, err)
		assert.Equal(t, 3, called)
	})

	t.Run("failure after max attempts", func(t *testing.T) {
		restore := vault.SetSleepFn(func(time.Duration) {})
		defer restore()

		called := 0
		err := vault.RetryableVaultOperation("read", "secret/x", "k", func() error {
			called++
			return fmt.Errorf("connection refused always")
		})

		require.Error(t, err)
		assert.Equal(t, vault.VaultMaxAttempts, called)
	})

	t.Run("non-retryable stops immediately", func(t *testing.T) {
		restore := vault.SetSleepFn(func(time.Duration) {})
		defer restore()

		called := 0
		err := vault.RetryableVaultOperation("write", "secret/y", "", func() error {
			called++
			return vault.ErrAccessDenied
		})

		require.Error(t, err)
		assert.Equal(t, 1, called, "non-retryable error must not trigger retries")
	})
}

// TestWithRetryNilConfig ensures nil config falls back to defaults.
// Not parallel: mutates sleepFn.
func TestWithRetryNilConfig(t *testing.T) {
	restore := vault.SetSleepFn(func(time.Duration) {})
	defer restore()

	err := vault.WithRetry(func() error { return nil }, nil)
	require.NoError(t, err)
}

// TestCalculateDelayMatrix validates delay values for various attempt/factor combos.
// Not parallel: mutates sleepFn.
func TestCalculateDelayMatrix(t *testing.T) {
	tests := []struct {
		name        string
		baseDelay   time.Duration
		maxDelay    time.Duration
		factor      float64
		attempts    int
		wantSleeps  int
		wantMaxSeen time.Duration
	}{
		{
			name:        "base delay factor=2 attempt=2",
			baseDelay:   100 * time.Millisecond,
			maxDelay:    10 * time.Second,
			factor:      2.0,
			attempts:    2,
			wantSleeps:  1,
			wantMaxSeen: 200 * time.Millisecond,
		},
		{
			name:        "factor=0 produces zero delay",
			baseDelay:   1 * time.Second,
			maxDelay:    30 * time.Second,
			factor:      0.0,
			attempts:    2,
			wantSleeps:  1,
			wantMaxSeen: 0,
		},
		{
			name:        "factor=0.5 reduces delay",
			baseDelay:   4 * time.Second,
			maxDelay:    30 * time.Second,
			factor:      0.5,
			attempts:    2,
			wantSleeps:  1,
			wantMaxSeen: 2 * time.Second,
		},
		{
			name:        "delay capped at maxDelay",
			baseDelay:   5 * time.Second,
			maxDelay:    6 * time.Second,
			factor:      10.0,
			attempts:    3,
			wantSleeps:  2,
			wantMaxSeen: 6 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// NOT parallel: each subtest replaces the global sleepFn.
			var sleeps []time.Duration
			restore := vault.SetSleepFn(func(d time.Duration) {
				sleeps = append(sleeps, d)
			})
			defer restore()

			cfg := &vault.RetryConfig{
				MaxAttempts:   tt.attempts,
				BaseDelay:     tt.baseDelay,
				MaxDelay:      tt.maxDelay,
				BackoffFactor: tt.factor,
			}

			call := 0
			_ = vault.WithRetry(func() error {
				call++
				if call < tt.attempts {
					return fmt.Errorf("connection refused call %d", call)
				}
				return nil
			}, cfg)

			assert.Len(t, sleeps, tt.wantSleeps)
			for _, s := range sleeps {
				assert.LessOrEqual(t, s, tt.wantMaxSeen,
					"sleep %v exceeded max observed %v", s, tt.wantMaxSeen)
				assert.LessOrEqual(t, s, tt.maxDelay,
					"sleep %v exceeded configured maxDelay %v", s, tt.maxDelay)
			}
		})
	}
}
