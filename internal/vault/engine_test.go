package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestDetector builds an EngineDetector backed by a nil Client.
// Safe for tests that exercise cache or pure helpers only — never calls client.logical.
func newTestDetector() *EngineDetector {
	return &EngineDetector{
		client: nil,
		cache:  make(map[string]*EngineInfo),
		logger: logger.Get(),
	}
}

// --- extractMountPath ---

func TestExtractMountPath_RootSegment(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	assert.Equal(t, "secret", ed.extractMountPath("secret/config/mybloc"))
}

func TestExtractMountPath_LeadingSlash(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	assert.Equal(t, "secret", ed.extractMountPath("/secret/config/mybloc"))
}

func TestExtractMountPath_SingleSegment(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	assert.Equal(t, "kv", ed.extractMountPath("kv"))
}

func TestExtractMountPath_EmptyPath(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	assert.Equal(t, "", ed.extractMountPath(""))
}

func TestExtractMountPath_LeadingSlashOnly(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	// "/" strips to "" then splits to ["",""] — first part is "".
	assert.Equal(t, "", ed.extractMountPath("/"))
}

// --- configureEngineType / configureKVEngine / configureGenericEngine ---

func TestConfigureKVEngine_Version2(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "kv", Path: "secret"}
	configureKVEngine(info, map[string]interface{}{"version": "2"})
	assert.Equal(t, kvV2Type, info.Type)
	assert.Equal(t, "2", info.Version)
}

func TestConfigureKVEngine_Version1(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "kv", Path: "kv"}
	configureKVEngine(info, map[string]interface{}{"version": "1"})
	assert.Equal(t, kvV1Type, info.Type)
	assert.Equal(t, "1", info.Version)
}

func TestConfigureKVEngine_NoOptions(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "kv", Path: "kv"}
	configureKVEngine(info, nil)
	assert.Equal(t, kvV1Type, info.Type)
	assert.Equal(t, "1", info.Version)
}

func TestConfigureKVEngine_NonStringVersion(t *testing.T) {
	t.Parallel()
	// version is int, not string — falls through to default v1.
	info := &EngineInfo{Type: "kv", Path: "kv"}
	configureKVEngine(info, map[string]interface{}{"version": 2})
	assert.Equal(t, kvV1Type, info.Type)
	assert.Equal(t, "1", info.Version)
}

func TestConfigureGenericEngine(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "generic", Path: "generic"}
	configureGenericEngine(info)
	assert.Equal(t, kvV1Type, info.Type)
	assert.Equal(t, "1", info.Version)
}

func TestConfigureEngineType_KV(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "kv", Path: "kv"}
	configureEngineType(info, "kv", map[string]interface{}{"version": "2"})
	assert.Equal(t, kvV2Type, info.Type)
}

func TestConfigureEngineType_Generic(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "generic", Path: "generic"}
	configureEngineType(info, "generic", nil)
	assert.Equal(t, kvV1Type, info.Type)
	assert.Equal(t, "1", info.Version)
}

func TestConfigureEngineType_Default(t *testing.T) {
	t.Parallel()
	info := &EngineInfo{Type: "transit", Path: "transit"}
	configureEngineType(info, "transit", nil)
	// Default branch sets Version only.
	assert.Equal(t, "1", info.Version)
	assert.Equal(t, "transit", info.Type) // type not overwritten
}

// --- parseMountInfo ---

func TestParseMountInfo_KVv2(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	mountData := map[string]interface{}{
		"type": "kv",
		"options": map[string]interface{}{
			"version": "2",
		},
	}
	info, err := ed.parseMountInfo("secret/", mountData)
	require.NoError(t, err)
	assert.Equal(t, kvV2Type, info.Type)
	assert.Equal(t, "2", info.Version)
	assert.Equal(t, "secret", info.Path) // trailing slash stripped
}

func TestParseMountInfo_NoType(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	info, err := ed.parseMountInfo("custom/", map[string]interface{}{})
	require.NoError(t, err)
	// engineType "" hits default branch → Version "1".
	assert.Equal(t, "1", info.Version)
	assert.Equal(t, "custom", info.Path)
}

func TestParseMountInfo_NonStringOptions(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	mountData := map[string]interface{}{
		"type":    "kv",
		"options": "invalid-options-type",
	}
	info, err := ed.parseMountInfo("kv/", mountData)
	require.NoError(t, err)
	// options not a map → treated as no options → default v1.
	assert.Equal(t, kvV1Type, info.Type)
}

// --- inferEngineFromPath ---

func TestInferEngineFromPath_Secret(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	info, err := ed.inferEngineFromPath("secret")
	require.NoError(t, err)
	assert.Equal(t, kvV2Type, info.Type)
	assert.Equal(t, "2", info.Version)
}

func TestInferEngineFromPath_KV(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	info, err := ed.inferEngineFromPath("kv/")
	require.NoError(t, err)
	assert.Equal(t, kvV2Type, info.Type)
}

func TestInferEngineFromPath_Unknown(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	info, err := ed.inferEngineFromPath("custom-mount")
	require.NoError(t, err)
	assert.Equal(t, kvV1Type, info.Type)
	assert.Equal(t, "1", info.Version)
}

// --- GetCachedEngines / ClearCache ---

func TestGetCachedEngines_Empty(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	cached := ed.GetCachedEngines()
	assert.Empty(t, cached)
}

func TestGetCachedEngines_ReturnsCopy(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	ed.cache["secret"] = &EngineInfo{Type: kvV2Type, Version: "2", Path: "secret"}
	cached := ed.GetCachedEngines()
	assert.Len(t, cached, 1)
	// Mutating the returned copy must not affect internal cache.
	cached["secret"].Type = "mutated"
	assert.Equal(t, kvV2Type, ed.cache["secret"].Type)
}

func TestClearCache(t *testing.T) {
	t.Parallel()
	ed := newTestDetector()
	ed.cache["secret"] = &EngineInfo{Type: kvV2Type, Version: "2", Path: "secret"}
	ed.ClearCache()
	assert.Empty(t, ed.GetCachedEngines())
}
