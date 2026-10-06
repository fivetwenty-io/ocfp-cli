package vault

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeSet_ReadErrorWritesNothing(t *testing.T) {
	t.Parallel()

	for name, status := range map[string]int{"server error": http.StatusInternalServerError, "permission denied": http.StatusForbidden} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := &kvServer{readStatus: status, readBody: `{"errors":["boom"]}`}
			safe := newKVv1Safe(t, server)

			err := safe.Set("secret/config/bloc/mgmt/fqdns", "shield", "s.example.io")
			require.Error(t, err)

			server.mu.Lock()
			defer server.mu.Unlock()

			assert.Empty(t, server.writes, "a failed read must not be followed by a write that drops the keys it could not see")
		})
	}
}

func TestSafeSet_MissingRecordStartsEmpty(t *testing.T) {
	t.Parallel()

	server := &kvServer{readStatus: http.StatusNotFound, readBody: `{"errors":[]}`}
	safe := newKVv1Safe(t, server)

	require.NoError(t, safe.Set("secret/config/bloc/mgmt/fqdns", "shield", "s.example.io"))

	require.Len(t, server.writes, 1)
	assert.Equal(t, map[string]interface{}{"shield": "s.example.io"}, server.writes[0])
}

func TestSafeSet_MergesIntoExistingRecord(t *testing.T) {
	t.Parallel()

	server := &kvServer{readStatus: http.StatusOK, readBody: `{"data":{"legacy":"legacy.example.io"}}`}
	safe := newKVv1Safe(t, server)

	require.NoError(t, safe.Set("secret/config/bloc/mgmt/fqdns", "shield", "s.example.io"))

	require.Len(t, server.writes, 1)
	assert.Equal(t, map[string]interface{}{"legacy": "legacy.example.io", "shield": "s.example.io"}, server.writes[0])
}
