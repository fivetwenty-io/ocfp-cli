package keyfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A target counts as current by its name or by its URL, and an rc that
// cannot be read counts as naming it, so nothing deletes it on a guess.
func TestTargetIsCurrent(t *testing.T) {
	const rcTargets = "vaults:\n  lab-inception: {url: \"http://127.0.0.1:18200\"}\n  ops: {url: \"https://ops.example:8200\"}\n"

	for name, tc := range map[string]struct {
		rc   string
		want bool
	}{
		"current by name":           {rc: "current: lab-inception\n" + rcTargets, want: true},
		"current by URL":            {rc: "current: http://127.0.0.1:18200/\n" + rcTargets, want: true},
		"another target is current": {rc: "current: ops\n" + rcTargets},
		"nothing is current":        {rc: "current: \"\"\n" + rcTargets},
		"an unparseable rc":         {rc: "current: [\n", want: true},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			require.NoError(t, os.WriteFile(filepath.Join(home, ".saferc"), []byte(tc.rc), 0o600))
			assert.Equal(t, tc.want, TargetIsCurrent("lab-inception"))
		})
	}

	t.Run("no rc at all", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		assert.False(t, TargetIsCurrent("lab-inception"))
	})
}

func TestEnvWithoutSafeTarget(t *testing.T) {
	t.Setenv("SAFE_TARGET", "ghost")
	t.Setenv("OCFP_KEYFILE_TEST_KEEP", "1")

	env := EnvWithoutSafeTarget()
	assert.NotContains(t, env, "SAFE_TARGET=ghost")
	assert.Contains(t, env, "OCFP_KEYFILE_TEST_KEEP=1")
}

func TestLocalTargetName(t *testing.T) {
	assert.Equal(t, "lab-inception-local", LocalTargetName("lab-inception"))
}
