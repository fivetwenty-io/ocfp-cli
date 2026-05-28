package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- isValidTmuxSession ---

func TestIsValidTmuxSession_Valid(t *testing.T) {
	t.Parallel()
	cases := []string{
		"inception",
		"my-bloc-inception-vault",
		"session_1",
		"a.b.c",
		"Session123",
	}
	for _, s := range cases {
		assert.True(t, isValidTmuxSession(s), "expected valid: %q", s)
	}
}

func TestIsValidTmuxSession_Invalid(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"session name",    // space
		"session;name",    // semicolon
		"session$(cmd)",   // shell expansion
		"session\nname",   // newline
		"../traversal",    // path traversal
		"session|pipe",    // pipe
	}
	for _, s := range cases {
		assert.False(t, isValidTmuxSession(s), "expected invalid: %q", s)
	}
}

// --- stripANSI ---

func TestStripANSI_Plain(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hello", stripANSI("hello"))
}

func TestStripANSI_ColorCode(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hello world", stripANSI("\x1b[32mhello world\x1b[0m"))
}

func TestStripANSI_BoldAndReset(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "text", stripANSI("\x1b[1mtext\x1b[0m"))
}

func TestStripANSI_MultipleSequences(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc", stripANSI("\x1b[31ma\x1b[32mb\x1b[0mc"))
}

func TestStripANSI_Empty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", stripANSI(""))
}

// --- joinVaultPath ---

func TestJoinVaultPath_NonEmpty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "secret/config/subpath", joinVaultPath("secret/config/", "subpath"))
}

func TestJoinVaultPath_EmptyCurrent(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "secret/config/", joinVaultPath("secret/config/", ""))
}

func TestJoinVaultPath_BothEmpty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", joinVaultPath("", ""))
}

// --- sortedChildNames ---

func TestSortedChildNames_Sorted(t *testing.T) {
	t.Parallel()
	got := sortedChildNames([]string{"z/", "a/", "m/"})
	assert.Equal(t, []string{"a", "m", "z"}, got)
}

func TestSortedChildNames_TrailingSlashStripped(t *testing.T) {
	t.Parallel()
	got := sortedChildNames([]string{"child/"})
	assert.Equal(t, []string{"child"}, got)
}

func TestSortedChildNames_NoTrailingSlash(t *testing.T) {
	t.Parallel()
	got := sortedChildNames([]string{"b", "a"})
	assert.Equal(t, []string{"a", "b"}, got)
}

func TestSortedChildNames_Empty(t *testing.T) {
	t.Parallel()
	assert.Nil(t, sortedChildNames(nil))
	assert.Nil(t, sortedChildNames([]string{}))
}

// --- sortedMapKeys ---

func TestSortedMapKeys_Sorted(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{"z": 1, "a": 2, "m": 3}
	got := sortedMapKeys(data)
	assert.Equal(t, []string{"a", "m", "z"}, got)
}

func TestSortedMapKeys_EmptyMap(t *testing.T) {
	t.Parallel()
	assert.Nil(t, sortedMapKeys(nil))
	assert.Nil(t, sortedMapKeys(map[string]interface{}{}))
}

func TestSortedMapKeys_SingleKey(t *testing.T) {
	t.Parallel()
	got := sortedMapKeys(map[string]interface{}{"only": true})
	assert.Equal(t, []string{"only"}, got)
}

// --- joinChildPath ---

func TestJoinChildPath_EmptyCurrent(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "child", joinChildPath("", "child"))
}

func TestJoinChildPath_NonEmptyCurrent(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "parent/child", joinChildPath("parent", "child"))
}

func TestJoinChildPath_Nested(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a/b/c", joinChildPath("a/b", "c"))
}

// --- buildPathWithKey ---

func TestBuildPathWithKey_EmptyPath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ":mykey", buildPathWithKey("", "mykey"))
}

func TestBuildPathWithKey_NonEmptyPath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "config/bloc:mykey", buildPathWithKey("config/bloc", "mykey"))
}

func TestBuildPathWithKey_PathWithLeadingSlash(t *testing.T) {
	t.Parallel()
	// Leading slash stripped by TrimPrefix.
	assert.Equal(t, "config/bloc:mykey", buildPathWithKey("/config/bloc", "mykey"))
}

// --- filterInceptionSessions ---

func TestFilterInceptionSessions_ExactMatch(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	got := m.filterInceptionSessions([]string{"mybloc-inception-vault", "other"}, "mybloc-inception")
	assert.Equal(t, []string{"mybloc-inception-vault"}, got)
}

func TestFilterInceptionSessions_LegacyFallback(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	// legacy: contains "inception" AND "vault"
	got := m.filterInceptionSessions([]string{"some-inception-vault-session"}, "other-inception")
	assert.Equal(t, []string{"some-inception-vault-session"}, got)
}

func TestFilterInceptionSessions_NoMatch(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	got := m.filterInceptionSessions([]string{"unrelated", "also-unrelated"}, "mybloc-inception")
	assert.Empty(t, got)
}

func TestFilterInceptionSessions_EmptyInput(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	got := m.filterInceptionSessions(nil, "mybloc-inception")
	assert.Empty(t, got)
}

// --- getTargetTypesForKit ---

func TestGetTargetTypesForKit_MgmtOnly(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	for _, kit := range []string{"concourse", "doomsday", "jumpbox", "shield", "vault", "bosh"} {
		got := m.getTargetTypesForKit(kit)
		assert.Equal(t, []string{MgmtEnvType}, got, "kit=%s", kit)
	}
}

func TestGetTargetTypesForKit_Both(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	got := m.getTargetTypesForKit("prometheus")
	assert.Equal(t, []string{MgmtEnvType, OCFEnvType}, got)
}

func TestGetTargetTypesForKit_DefaultOCF(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	for _, kit := range []string{"cf", "autoscaler", "blacksmith", "scheduler", "unknown-kit"} {
		got := m.getTargetTypesForKit(kit)
		assert.Equal(t, []string{OCFEnvType}, got, "kit=%s", kit)
	}
}

// --- buildParsedEnv ---

func TestBuildParsedEnv_ValidOCF(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	env := m.buildParsedEnv("cf/mybloc-eu01-ocf")
	require.NotNil(t, env)
	assert.Equal(t, "cf", env.Kit)
	assert.Equal(t, "mybloc-eu01-ocf", env.Name)
	assert.Equal(t, OCFEnvType, env.Type)
}

func TestBuildParsedEnv_MgmtSuffix(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	env := m.buildParsedEnv("bosh/mybloc-eu01-mgmt")
	require.NotNil(t, env)
	assert.Equal(t, MgmtEnvType, env.Type)
}

func TestBuildParsedEnv_NoSlash(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	assert.Nil(t, m.buildParsedEnv("noslash"))
}

func TestBuildParsedEnv_EmptyKit(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	// "/envname" — kit part is blank after trim.
	assert.Nil(t, m.buildParsedEnv("/envname"))
}

func TestBuildParsedEnv_EmptyEnvName(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	assert.Nil(t, m.buildParsedEnv("kit/"))
}

func TestBuildParsedEnv_ANSIStripped(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	env := m.buildParsedEnv("\x1b[32mcf\x1b[0m/\x1b[31mmyenv\x1b[0m")
	require.NotNil(t, env)
	assert.Equal(t, "cf", env.Kit)
	assert.Equal(t, "myenv", env.Name)
}

// --- sleepFn seam wiring ---

// TestManagerSleepFnSeam verifies the package-level sleepFn can be overridden
// and that the seam is wired (SetSleepFn exported from export_test.go).
func TestManagerSleepFnSeam(t *testing.T) {
	var recorded []time.Duration
	restore := SetSleepFn(func(d time.Duration) {
		recorded = append(recorded, d)
	})
	defer restore()

	// Call sleepFn directly to prove override is active.
	sleepFn(5 * time.Second)
	assert.Equal(t, []time.Duration{5 * time.Second}, recorded)
}
