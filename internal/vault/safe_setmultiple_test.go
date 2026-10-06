package vault

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kvServer is a local stand-in for a KV v1 mount holding one record. It
// answers reads with the configured status and body and records every write.
type kvServer struct {
	mu         sync.Mutex
	readStatus int
	readBody   string
	writes     []map[string]interface{}
}

func (k *kvServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		w.WriteHeader(k.readStatus)
		_, _ = io.WriteString(w, k.readBody)
	case http.MethodPut, http.MethodPost:
		var body map[string]interface{}

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)

		k.writes = append(k.writes, body)

		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newKVv1Safe(t *testing.T, server *kvServer) *Safe {
	t.Helper()

	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)

	cfg := api.DefaultConfig()
	cfg.Address = ts.URL
	cfg.MaxRetries = 0

	apiClient, err := api.NewClient(cfg)
	require.NoError(t, err)

	apiClient.SetToken("test-token")

	safe := NewSafe(buildVaultClient(apiClient, &Config{}))
	safe.engine.cache["secret"] = &EngineInfo{Type: kvV1Type, Path: "secret"}

	return safe
}

func TestSafeSetMultiple_ReadErrorWritesNothing(t *testing.T) {
	t.Parallel()

	for name, status := range map[string]int{"server error": http.StatusInternalServerError, "permission denied": http.StatusForbidden} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := &kvServer{readStatus: status, readBody: `{"errors":["boom"]}`}
			safe := newKVv1Safe(t, server)

			err := safe.SetMultiple("secret/config/bloc/mgmt/fqdns", map[string]interface{}{"shield": "s.example.io"})
			require.Error(t, err)

			server.mu.Lock()
			defer server.mu.Unlock()

			assert.Empty(t, server.writes, "a failed read must not be followed by a write that drops the keys it could not see")
		})
	}
}

func TestSafeSetMultiple_MissingRecordStartsEmpty(t *testing.T) {
	t.Parallel()

	server := &kvServer{readStatus: http.StatusNotFound, readBody: `{"errors":[]}`}
	safe := newKVv1Safe(t, server)

	require.NoError(t, safe.SetMultiple("secret/config/bloc/mgmt/fqdns", map[string]interface{}{"shield": "s.example.io"}))

	require.Len(t, server.writes, 1)
	assert.Equal(t, map[string]interface{}{"shield": "s.example.io"}, server.writes[0])
}

func TestSafeSetMultiple_MergesIntoExistingRecord(t *testing.T) {
	t.Parallel()

	server := &kvServer{readStatus: http.StatusOK, readBody: `{"data":{"legacy":"legacy.example.io","shield":"old.example.io"}}`}
	safe := newKVv1Safe(t, server)

	require.NoError(t, safe.SetMultiple("secret/config/bloc/mgmt/fqdns", map[string]interface{}{"shield": "s.example.io"}))

	require.Len(t, server.writes, 1)
	assert.Equal(t, "legacy.example.io", server.writes[0]["legacy"], "SetMultiple must merge, not replace")
	assert.Equal(t, "s.example.io", server.writes[0]["shield"])
}
