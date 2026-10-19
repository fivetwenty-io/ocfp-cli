package vault

import (
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCloudBlobstoreEndpointOverridesNativeStorage(t *testing.T) {
	opts := &PopulateOptions{DryRun: true, BlobstoreMode: "external", BlobstoreEndpoint: "http://10.0.0.11:9000", BlobstoreAccessKey: "test-key", BlobstoreSecretKey: "test-secret"}
	t.Run("aws", func(t *testing.T) {
		safe := &awsMockSafe{}
		p := newTestAWSProvider(&config.Config{Provider: "aws"}, safe)
		applyPopulateProviderOptions(p, "", opts)
		require.NoError(t, p.ConfigureBlobstores("", "ocf", nil, 1, 1))
		call := safe.findSetMultipleCall(p.PathBuilder.GetSystemBlobstorePath("ocf", "cf", "main"))
		require.NotNil(t, call)
		require.Equal(t, opts.BlobstoreEndpoint, call.data["endpoint"])
	})
	t.Run("gcp", func(t *testing.T) {
		safe := &gcpMockSafe{}
		p := newTestGCPProvider(&config.Config{Provider: "gcp"}, safe, "test-bloc")
		applyPopulateProviderOptions(p, "", opts)
		require.NoError(t, p.ConfigureBlobstores("", "ocf", nil, 1, 1))
		call := safe.findCall(p.PathBuilder.GetSystemBlobstorePath("ocf", "cf", "main"))
		require.NotNil(t, call)
		require.Equal(t, opts.BlobstoreEndpoint, call.data["endpoint"])
	})
}

func TestCloudNativeBucketNamesMatchBootstrap(t *testing.T) {
	a := newTestAWSProvider(&config.Config{}, &awsMockSafe{})
	g := newTestGCPProvider(&config.Config{}, &gcpMockSafe{}, "test-bloc")
	require.Equal(t, "test-bloc-mgmt-artifacts", a.getBlobstoresForSystem("bosh", "mgmt")["artifacts"]["name"])
	require.Equal(t, "test-bloc-mgmt-artifacts", g.getBlobstoresForSystem("bosh", "mgmt")["artifacts"]["bucket_name"])
	require.Equal(t, "test-bloc-ocf-cf-resource-pool", a.getBlobstoresForSystem("cf", "ocf")["resources"]["name"])
	require.Equal(t, "test-bloc-ocf-cf-resource-pool", g.getBlobstoresForSystem("cf", "ocf")["resources"]["bucket_name"])
}
