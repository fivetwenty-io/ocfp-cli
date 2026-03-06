package vault

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// awsMockSafe tracks both SetMultiple and Set calls for testing.
type awsMockSafe struct {
	setMultipleCalls []setCall
	setSingleCalls   []setSingleCall
}

type setSingleCall struct {
	path  string
	key   string
	value interface{}
}

func (m *awsMockSafe) Set(path, key string, value interface{}) error {
	m.setSingleCalls = append(m.setSingleCalls, setSingleCall{
		path:  path,
		key:   key,
		value: value,
	})

	return nil
}

func (m *awsMockSafe) SetMultiple(path string, data map[string]interface{}) error {
	m.setMultipleCalls = append(m.setMultipleCalls, setCall{
		path: path,
		data: data,
	})

	return nil
}

func (m *awsMockSafe) Get(_, _ string) (interface{}, error)            { return "", nil }
func (m *awsMockSafe) GetAll(_ string) (map[string]interface{}, error) { return nil, nil }
func (m *awsMockSafe) Exists(_ string) (bool, error)                   { return false, nil }
func (m *awsMockSafe) Delete(_, _ string) error                        { return nil }
func (m *awsMockSafe) List(_ string) ([]string, error)                 { return nil, nil }
func (m *awsMockSafe) Export(_ string) (map[string]interface{}, error) { return nil, nil }
func (m *awsMockSafe) Import(_ string, _ map[string]interface{}) error { return nil }
func (m *awsMockSafe) GetEngineInfo(_ string) (*EngineInfo, error)     { return nil, nil }
func (m *awsMockSafe) MustGet(_, _ string) interface{}                 { return "" }
func (m *awsMockSafe) GetString(_, _ string) (string, error)           { return "", nil }
func (m *awsMockSafe) GetJSON(_, _ string) ([]byte, error)             { return nil, nil }

// findSetMultipleCall searches for a SetMultiple call by path.
func (m *awsMockSafe) findSetMultipleCall(path string) *setCall {
	for i := range m.setMultipleCalls {
		if m.setMultipleCalls[i].path == path {
			return &m.setMultipleCalls[i]
		}
	}

	return nil
}

// findSetSingleCall searches for a Set call by path and key.
func (m *awsMockSafe) findSetSingleCall(path, key string) *setSingleCall {
	for i := range m.setSingleCalls {
		if m.setSingleCalls[i].path == path && m.setSingleCalls[i].key == key {
			return &m.setSingleCalls[i]
		}
	}

	return nil
}

func newTestAWSProvider(cfg *config.Config, mock *awsMockSafe) *AWSVaultProvider {
	blocName := "test-bloc"

	return &AWSVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, blocName),
		Safe:              mock,
		PathBuilder:       NewPathBuilder(cfg, blocName),
		logger:            logger.Get(),
	}
}

// TestAWS_Bug5_DNSStoredAsString verifies DNS is written as a string, not array.
func TestAWS_Bug5_DNSStoredAsString(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		Region: "us-east-1",
		DNS:    []string{"10.0.0.2", "10.0.0.3"},
	}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureVPC(MgmtEnvType)
	require.NoError(t, err)

	// Find the VPC SetMultiple call
	vpcPath := provider.PathBuilder.GetNetPath(MgmtEnvType)
	call := mock.findSetMultipleCall(vpcPath)
	require.NotNil(t, call, "VPC SetMultiple call should exist at %s", vpcPath)

	// DNS should be a string (first element), not the array
	assert.Equal(t, "10.0.0.2", call.data["dns"], "DNS should be the first element as string")
}

// TestAWS_Bug5_DNSDefaultsToCloudflare verifies DNS defaults when no DNS configured.
func TestAWS_Bug5_DNSDefaultsToCloudflare(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		Region: "us-east-1",
		DNS:    []string{},
	}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureVPC(MgmtEnvType)
	require.NoError(t, err)

	vpcPath := provider.PathBuilder.GetNetPath(MgmtEnvType)
	call := mock.findSetMultipleCall(vpcPath)
	require.NotNil(t, call)

	assert.Equal(t, DefaultDNSServer, call.data["dns"], "DNS should default to DefaultDNSServer")
}

// TestAWS_Bug4_BlobstoreKeyIsBosh verifies blobstore uses "bosh" key, not "artifacts".
func TestAWS_Bug4_BlobstoreKeyIsBosh(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "us-east-1"}
	provider := newTestAWSProvider(cfg, mock)

	blobstores := provider.getBlobstoresForSystem(boshSystem, MgmtEnvType)

	// Key should be "bosh", not "artifacts"
	_, hasArtifacts := blobstores["artifacts"]
	assert.False(t, hasArtifacts, "Should not have 'artifacts' key")

	boshBlob, hasBosh := blobstores["bosh"]
	assert.True(t, hasBosh, "Should have 'bosh' key")

	// Name should be {bloc}-{env}-bosh, not {bloc}-{env}-bosh-artifacts
	assert.Equal(t, "test-bloc-mgmt-bosh", boshBlob["name"])
	assert.Equal(t, "us-east-1", boshBlob["region"])
	assert.Equal(t, "s3", boshBlob["type"])
}

// TestAWS_Bug3_SecurityGroupUsesOCFP verifies SG uses "ocfp" name, not envType.
func TestAWS_Bug3_SecurityGroupUsesOCFP(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "us-east-1"}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureSecurityGroups(MgmtEnvType)
	require.NoError(t, err)

	// Should have 2 calls: default + ocfp
	assert.Equal(t, 2, len(mock.setMultipleCalls), "Should have 2 SetMultiple calls")

	// The second call should be to sgs/ocfp, not sgs/mgmt
	expectedPath := provider.PathBuilder.GetSecurityGroupPath(MgmtEnvType, DefaultSubnetType)
	call := mock.findSetMultipleCall(expectedPath)
	require.NotNil(t, call, "Should write to sgs/ocfp, not sgs/mgmt")

	assert.Equal(t, "test-bloc-ocfp", call.data["id"])
	assert.Equal(t, "ocfp", call.data["name"])
}

// TestAWS_Bug3_SecurityGroupOCFEnv verifies SG path for ocf environment too.
func TestAWS_Bug3_SecurityGroupOCFEnv(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "us-east-1"}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureSecurityGroups(OCFEnvType)
	require.NoError(t, err)

	// The ocfp SG should also be at sgs/ocfp under ocf env, not sgs/ocf
	expectedPath := provider.PathBuilder.GetSecurityGroupPath(OCFEnvType, DefaultSubnetType)
	call := mock.findSetMultipleCall(expectedPath)
	require.NotNil(t, call, "Should write to ocf env sgs/ocfp, not sgs/ocf")

	assert.Equal(t, "test-bloc-ocfp", call.data["id"])
	assert.Equal(t, "ocfp", call.data["name"])
}

// TestAWS_Bug2_CPIHasAllFields verifies CPI config includes keypair_name, instance type, and disk type.
func TestAWS_Bug2_CPIHasAllFields(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		Region: "us-east-1",
		S3: map[string]string{
			"access_key_id":     "AKID",
			"secret_access_key": "SECRET",
		},
	}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)

	require.Equal(t, 1, len(mock.setMultipleCalls), "Should have 1 SetMultiple call for CPI")

	cpi := mock.setMultipleCalls[0].data

	// keypair_name must exist (kit uses this, not just default_key_name)
	assert.Equal(t, "test-bloc-bastion", cpi["keypair_name"], "keypair_name should be set")

	// default_key_name should also exist (backward compat)
	assert.Equal(t, "test-bloc-bastion", cpi["default_key_name"])

	// default_instance_type should default to t3.large
	assert.Equal(t, "t3.large", cpi["default_instance_type"])

	// default_disk_type should default to gp2
	assert.Equal(t, "gp2", cpi["default_disk_type"])
}

// TestAWS_Bug2_CPIConfigurableDefaults verifies CPI respects config overrides.
func TestAWS_Bug2_CPIConfigurableDefaults(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		Region:              "eu-west-1",
		DefaultInstanceType: "m6i.xlarge",
		DefaultDiskType:     "gp3",
		S3: map[string]string{
			"access_key_id":     "AKID",
			"secret_access_key": "SECRET",
		},
	}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureCPI(MgmtEnvType)
	require.NoError(t, err)

	cpi := mock.setMultipleCalls[0].data
	assert.Equal(t, "m6i.xlarge", cpi["default_instance_type"])
	assert.Equal(t, "gp3", cpi["default_disk_type"])
}

// TestAWS_Bug1_IAMWritesToThreePaths verifies IAM credentials go to iam/bosh, iam/s3, and s3.
func TestAWS_Bug1_IAMWritesToThreePaths(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{
		Region: "us-east-1",
		S3: map[string]string{
			"access_key_id":     "AKIAEXAMPLE",
			"secret_access_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		},
	}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureIAM(MgmtEnvType)
	require.NoError(t, err)

	// Should have exactly 3 SetMultiple calls
	assert.Equal(t, 3, len(mock.setMultipleCalls), "Should write to 3 paths")

	// Path 1: bosh/iam/bosh
	iamBoshPath := provider.PathBuilder.GetIAMBoshPath(MgmtEnvType)
	boshCall := mock.findSetMultipleCall(iamBoshPath)
	require.NotNil(t, boshCall, "Should write to bosh/iam/bosh")
	assert.Equal(t, "AKIAEXAMPLE", boshCall.data["access_key"])
	assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", boshCall.data["secret_key"])

	// Path 2: bosh/iam/s3
	iamS3Path := provider.PathBuilder.GetIAMS3Path(MgmtEnvType)
	s3IAMCall := mock.findSetMultipleCall(iamS3Path)
	require.NotNil(t, s3IAMCall, "Should write to bosh/iam/s3")
	assert.Equal(t, "AKIAEXAMPLE", s3IAMCall.data["access_key"])

	// Path 3: bosh/s3 (backward compat)
	s3Path := provider.PathBuilder.GetS3Path(MgmtEnvType)
	s3Call := mock.findSetMultipleCall(s3Path)
	require.NotNil(t, s3Call, "Should write to bosh/s3")
	assert.Equal(t, "AKIAEXAMPLE", s3Call.data["access_key"])
}

// TestAWS_Bug1_IAMSkipsWhenNoCreds verifies IAM is skipped when no credentials.
func TestAWS_Bug1_IAMSkipsWhenNoCreds(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "us-east-1"}
	provider := newTestAWSProvider(cfg, mock)

	err := provider.configureIAM(MgmtEnvType)
	require.NoError(t, err)

	assert.Equal(t, 0, len(mock.setMultipleCalls), "Should not write when no credentials")
}

// TestAWS_Bug6_BuildDeploymentDBPath verifies region-to-path conversion.
func TestAWS_Bug6_BuildDeploymentDBPath(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "us-east-1"}
	provider := newTestAWSProvider(cfg, mock)

	path := provider.buildDeploymentDBPath(MgmtEnvType)
	assert.Equal(t, "secret/ocfp/aws/mgmt/us/east/1/bosh/db/bosh", path)
}

// TestAWS_Bug6_BuildDeploymentDBPathEURegion verifies EU region path.
func TestAWS_Bug6_BuildDeploymentDBPathEURegion(t *testing.T) {
	mock := &awsMockSafe{}
	cfg := &config.Config{Region: "eu-central-1"}
	provider := newTestAWSProvider(cfg, mock)

	path := provider.buildDeploymentDBPath(OCFEnvType)
	assert.Equal(t, "secret/ocfp/aws/ocf/eu/central/1/bosh/db/bosh", path)
}

// TestAWS_PathBuilder_GetIAMBoshPath verifies the IAM BOSH path helper.
func TestAWS_PathBuilder_GetIAMBoshPath(t *testing.T) {
	pb := NewPathBuilder(&config.Config{}, "mybloc")

	assert.Equal(t, "secret/config/mybloc/mgmt/bosh/iam/bosh", pb.GetIAMBoshPath(MgmtEnvType))
	assert.Equal(t, "secret/config/mybloc/ocf/bosh/iam/bosh", pb.GetIAMBoshPath(OCFEnvType))
}

// TestAWS_PathBuilder_GetIAMS3Path verifies the IAM S3 path helper.
func TestAWS_PathBuilder_GetIAMS3Path(t *testing.T) {
	pb := NewPathBuilder(&config.Config{}, "mybloc")

	assert.Equal(t, "secret/config/mybloc/mgmt/bosh/iam/s3", pb.GetIAMS3Path(MgmtEnvType))
	assert.Equal(t, "secret/config/mybloc/ocf/bosh/iam/s3", pb.GetIAMS3Path(OCFEnvType))
}
