package vault

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
)

// TestRecordingSafe_WritesRecordedNotForwarded — the core dry-run guarantee:
// every write lands in the plan and never in the underlying safe.
func TestRecordingSafe_WritesRecordedNotForwarded(t *testing.T) {
	t.Parallel()

	under := newFakeSafe()
	rec := newRecordingSafe(under)

	require.NoError(t, rec.SetMultiple("secret/cfg/net", map[string]interface{}{
		"bosh_ip":    "10.0.20.4",
		"haproxy_ip": "10.0.20.97",
	}))
	require.NoError(t, rec.Set("secret/cfg/az", "node_name", "pve-node1"))
	require.NoError(t, rec.Import("secret/cfg/imported", map[string]interface{}{"k": "v"}))

	assert.Empty(t, under.data, "dry-run must not write to the underlying safe")

	plan := rec.Plan()
	require.Len(t, plan, 3)
	assert.Equal(t, "secret/cfg/net", plan[0].Path)
	assert.Equal(t, []string{"bosh_ip", "haproxy_ip"}, plan[0].Keys, "keys must be sorted")
	assert.Equal(t, "secret/cfg/az", plan[1].Path)
	assert.Equal(t, []string{"node_name"}, plan[1].Keys)
	assert.Equal(t, "secret/cfg/imported", plan[2].Path)
}

// TestRecordingSafe_ReadYourWrites — providers read back records they wrote
// earlier in the same populate run (e.g. subnet records feeding reserved-ips),
// so recorded writes must be visible to subsequent reads, merged over the
// underlying data.
func TestRecordingSafe_ReadYourWrites(t *testing.T) {
	t.Parallel()

	under := newFakeSafe()
	require.NoError(t, under.SetMultiple("secret/cfg/net", map[string]interface{}{
		"existing": "keep",
		"stale":    "old",
	}))

	rec := newRecordingSafe(under)
	require.NoError(t, rec.SetMultiple("secret/cfg/net", map[string]interface{}{"stale": "new"}))
	require.NoError(t, rec.SetMultiple("secret/cfg/fresh", map[string]interface{}{"a": "1"}))

	v, err := rec.Get("secret/cfg/net", "stale")
	require.NoError(t, err)
	assert.Equal(t, "new", v, "recorded write must win over underlying data")

	all, err := rec.GetAll("secret/cfg/net")
	require.NoError(t, err)
	assert.Equal(t, "keep", all["existing"], "underlying keys must survive the overlay")
	assert.Equal(t, "new", all["stale"])

	exists, err := rec.Exists("secret/cfg/fresh")
	require.NoError(t, err)
	assert.True(t, exists, "a recorded path must exist for later reads")
}

// TestRecordingSafe_PlanDedupsRepeatWrites — a path written twice yields one
// plan entry holding the union of its keys, in first-write path order.
func TestRecordingSafe_PlanDedupsRepeatWrites(t *testing.T) {
	t.Parallel()

	rec := newRecordingSafe(newFakeSafe())

	require.NoError(t, rec.SetMultiple("secret/one", map[string]interface{}{"b": 1}))
	require.NoError(t, rec.SetMultiple("secret/two", map[string]interface{}{"x": 1}))
	require.NoError(t, rec.SetMultiple("secret/one", map[string]interface{}{"a": 2, "b": 3}))

	plan := rec.Plan()
	require.Len(t, plan, 2)
	assert.Equal(t, "secret/one", plan[0].Path)
	assert.Equal(t, []string{"a", "b"}, plan[0].Keys)
	assert.Equal(t, "secret/two", plan[1].Path)
}

// TestWritePopulatePlan_PrintsTargetAndKeyNamesOnly — the plan output names
// the resolved target vault and every path/key, and never a value.
func TestWritePopulatePlan_PrintsTargetAndKeyNamesOnly(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	writePopulatePlan(&buf, "https://10.0.20.5:8200", []PlannedWrite{
		{Path: "secret/cfg/net", Keys: []string{"bosh_ip", "haproxy_ip"}},
		{Path: "secret/cfg/az", Keys: []string{"node_name"}},
	})

	out := buf.String()
	assert.Contains(t, out, "https://10.0.20.5:8200")
	assert.Contains(t, out, "secret/cfg/net")
	assert.Contains(t, out, "bosh_ip, haproxy_ip")
	assert.Contains(t, out, "secret/cfg/az: node_name")
	assert.Contains(t, out, "no changes made")
}

// TestWritePopulatePlan_EmptyPlan — an empty plan still names the target and
// says nothing would be written.
func TestWritePopulatePlan_EmptyPlan(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	writePopulatePlan(&buf, "https://10.0.20.5:8200", nil)

	out := buf.String()
	assert.Contains(t, out, "https://10.0.20.5:8200")
	assert.Contains(t, out, "no writes")
}

// TestRecordingSafe_RealProviderPhase — the seam test: a real PVE provider
// phase run through the recorder produces the same writes in the plan as it
// would perform live, while the underlying safe stays untouched.
func TestRecordingSafe_RealProviderPhase(t *testing.T) {
	t.Parallel()

	under := newFakeSafe()
	rec := newRecordingSafe(under)

	cfg := &config.Config{Region: "pve-node1"}
	provider := &PVEVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		Safe:              rec,
		PathBuilder:       NewPathBuilder(cfg, "test-bloc"),
		logger:            logger.Get(),
		bucketEnsurer:     &recordingBucketEnsurer{},
	}

	require.NoError(t, provider.ConfigureAZs(MgmtEnvType))

	assert.Empty(t, under.data, "dry-run provider phase must not write through")
	assert.Len(t, rec.Plan(), pveWorkloadAZCount, "every AZ write must appear in the plan")
}
