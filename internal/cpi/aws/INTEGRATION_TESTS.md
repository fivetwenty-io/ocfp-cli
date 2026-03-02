# AWS Provider Integration Tests

This directory contains integration tests for the AWS CPI provider implementation. These tests verify the full lifecycle of AWS resources through the actual AWS SDK.

## Overview

The integration tests cover:
- **VPC Lifecycle**: Create, get, list, and delete VPCs
- **Subnet Lifecycle**: Create, get, list, and delete subnets
- **Elastic IP Lifecycle**: Allocate, get, list, and release Elastic IPs
- **EC2 Key Pair Lifecycle**: Create, import, get, list, and delete SSH key pairs
- **Security Group Lifecycle**: Create, get, list, delete, and manage rules
- **EBS Volume Lifecycle**: Create, get, list, attach, detach, and delete volumes
- **EBS Snapshot Lifecycle**: Create, get, list, and delete snapshots
- **S3 Bucket Lifecycle**: Create, get, list, empty, and delete buckets
- **Load Balancer Lifecycle**: Create, get, list, and delete ALB/NLB load balancers
- **Cross-Service Scenarios**: Multi-service resource orchestration

## Prerequisites

### AWS Credentials

Integration tests require valid AWS credentials. You can provide credentials in several ways:

1. **Environment Variables:**
   ```bash
   export AWS_ACCESS_KEY_ID=your_access_key
   export AWS_SECRET_ACCESS_KEY=your_secret_key
   export AWS_REGION=us-west-2  # Optional, defaults to us-west-2
   ```

2. **AWS Profile:**
   ```bash
   export AWS_PROFILE=your_profile_name
   ```

3. **IAM Role** (when running on EC2/ECS):
   Instance profile credentials will be automatically detected

### Required IAM Permissions

The AWS credentials must have the following permissions:

- **EC2:**
  - `ec2:CreateVpc`, `ec2:DeleteVpc`, `ec2:DescribeVpcs`
  - `ec2:CreateSubnet`, `ec2:DeleteSubnet`, `ec2:DescribeSubnets`
  - `ec2:CreateSecurityGroup`, `ec2:DeleteSecurityGroup`, `ec2:DescribeSecurityGroups`
  - `ec2:AuthorizeSecurityGroupIngress`, `ec2:AuthorizeSecurityGroupEgress`
  - `ec2:RevokeSecurityGroupIngress`, `ec2:RevokeSecurityGroupEgress`
  - `ec2:CreateKeyPair`, `ec2:DeleteKeyPair`, `ec2:DescribeKeyPairs`, `ec2:ImportKeyPair`
  - `ec2:AllocateAddress`, `ec2:ReleaseAddress`, `ec2:DescribeAddresses`
  - `ec2:AssociateAddress`, `ec2:DisassociateAddress`
  - `ec2:CreateVolume`, `ec2:DeleteVolume`, `ec2:DescribeVolumes`
  - `ec2:AttachVolume`, `ec2:DetachVolume`, `ec2:ModifyVolume`
  - `ec2:CreateSnapshot`, `ec2:DeleteSnapshot`, `ec2:DescribeSnapshots`
  - `ec2:CreateTags`, `ec2:DescribeNetworkInterfaces`
  - `ec2:CreateInternetGateway`, `ec2:AttachInternetGateway`, `ec2:DetachInternetGateway`
  - `ec2:DeleteInternetGateway`, `ec2:DescribeInternetGateways`
  - `ec2:CreateRouteTable`, `ec2:DeleteRouteTable`, `ec2:DescribeRouteTables`
  - `ec2:CreateRoute`, `ec2:AssociateRouteTable`, `ec2:DisassociateRouteTable`

- **S3:**
  - `s3:CreateBucket`, `s3:DeleteBucket`, `s3:ListAllMyBuckets`
  - `s3:GetBucketLocation`, `s3:GetBucketVersioning`, `s3:GetBucketEncryption`
  - `s3:GetBucketTagging`, `s3:ListBucket`, `s3:ListBucketVersions`
  - `s3:DeleteObject`, `s3:DeleteObjectVersion`

- **ELB:**
  - `elasticloadbalancing:CreateLoadBalancer`, `elasticloadbalancing:DeleteLoadBalancer`
  - `elasticloadbalancing:DescribeLoadBalancers`, `elasticloadbalancing:ModifyLoadBalancerAttributes`
  - `elasticloadbalancing:CreateTargetGroup`, `elasticloadbalancing:DeleteTargetGroup`
  - `elasticloadbalancing:DescribeTargetGroups`, `elasticloadbalancing:ModifyTargetGroup`
  - `elasticloadbalancing:CreateListener`, `elasticloadbalancing:DescribeListeners`
  - `elasticloadbalancing:RegisterTargets`, `elasticloadbalancing:DeregisterTargets`
  - `elasticloadbalancing:DescribeTargetHealth`

## Running Integration Tests

### Run All Integration Tests

```bash
# Run with AWS credentials
go test -tags=integration ./internal/cpi/aws/... -v

# Run with timeout
go test -tags=integration ./internal/cpi/aws/... -v -timeout 30m
```

### Run Specific Tests

```bash
# Run only VPC lifecycle test
go test -tags=integration ./internal/cpi/aws/... -v -run TestIntegration_VPCLifecycle

# Run only storage-related tests
go test -tags=integration ./internal/cpi/aws/... -v -run TestIntegration_.*Volume.*

# Run cross-service scenario
go test -tags=integration ./internal/cpi/aws/... -v -run TestIntegration_CrossServiceScenario
```

### Skip Integration Tests

Integration tests are automatically skipped when:
1. Running with `-short` flag: `go test -short ./internal/cpi/aws/...`
2. AWS credentials are not available

```bash
# Skip integration tests (run unit tests only)
go test -short ./internal/cpi/aws/... -v
```

## Testing with LocalStack

LocalStack provides a local AWS cloud emulator for testing without incurring AWS costs.

### Setup LocalStack

1. **Install LocalStack:**
   ```bash
   pip install localstack
   # or with Docker
   docker pull localstack/localstack
   ```

2. **Start LocalStack:**
   ```bash
   localstack start
   # or with Docker
   docker run --rm -it -p 4566:4566 -p 4571:4571 localstack/localstack
   ```

3. **Configure Environment:**
   ```bash
   export AWS_ACCESS_KEY_ID=test
   export AWS_SECRET_ACCESS_KEY=test
   export AWS_REGION=us-west-2
   export AWS_ENDPOINT_URL=http://localhost:4566
   # or
   export LOCALSTACK_ENDPOINT=http://localhost:4566
   ```

4. **Run Tests:**
   ```bash
   go test -tags=integration ./internal/cpi/aws/... -v
   ```

### LocalStack Limitations

- Some AWS services may not be fully implemented
- Behavior may differ slightly from real AWS
- Not all AWS regions are supported
- Resource state transitions may be faster than real AWS

## Test Configuration

Integration tests use the following defaults:

- **Region:** `us-west-2`
- **Availability Zone:** `us-west-2a`
- **VPC CIDR:** `10.100.0.0/16`
- **Subnet CIDR:** `10.100.1.0/24`
- **Test Timeout:** 5 minutes per test
- **Resource Name Prefix:** `ocfp-inttest`

Override defaults by modifying constants in `integration_test.go`.

## Resource Cleanup

Integration tests include automatic cleanup via `defer` statements. All resources created during tests are deleted after test completion, even if tests fail.

**Manual Cleanup:**
If tests are interrupted or cleanup fails, you can manually clean up resources:

```bash
# List and delete test VPCs
aws ec2 describe-vpcs --filters "Name=tag:managed-by,Values=ocfp-test" --query "Vpcs[*].VpcId" --output text | \
  xargs -n1 aws ec2 delete-vpc --vpc-id

# List and delete test key pairs
aws ec2 describe-key-pairs --filters "Name=tag:test,Values=integration" --query "KeyPairs[*].KeyName" --output text | \
  xargs -n1 aws ec2 delete-key-pair --key-name

# List and delete test volumes
aws ec2 describe-volumes --filters "Name=tag:test,Values=integration" --query "Volumes[*].VolumeId" --output text | \
  xargs -n1 aws ec2 delete-volume --volume-id

# List and delete test buckets
aws s3 ls | grep ocfp-inttest | awk '{print $3}' | \
  xargs -n1 sh -c 'aws s3 rb s3://$0 --force'
```

## Continuous Integration

For CI/CD pipelines:

1. **Use IAM Roles:** Configure OIDC or use IAM roles instead of long-lived credentials
2. **Isolate Test Resources:** Use unique prefixes or tags for parallel test runs
3. **Set Timeouts:** Configure appropriate timeouts for resource provisioning
4. **Monitor Costs:** Track AWS usage to prevent unexpected charges
5. **Use LocalStack:** Consider LocalStack for cost-effective CI testing

## Troubleshooting

### Tests Skipped
- **Issue:** Tests are skipped with "AWS credentials not found"
- **Solution:** Verify `AWS_ACCESS_KEY_ID` or `AWS_PROFILE` is set

### Permission Denied
- **Issue:** Tests fail with permission errors
- **Solution:** Verify IAM permissions listed above are granted

### Resource Already Exists
- **Issue:** Tests fail because resources already exist
- **Solution:** Run manual cleanup commands or use a different region

### Timeout Errors
- **Issue:** Tests timeout during resource provisioning
- **Solution:** Increase timeout with `-timeout` flag or check AWS service health

### Rate Limiting
- **Issue:** Tests fail with throttling errors
- **Solution:** Reduce concurrent test execution or implement delays between tests

## Best Practices

1. **Use Separate AWS Account:** Run integration tests in a dedicated test AWS account
2. **Monitor Costs:** Set up billing alerts for test account
3. **Tag Resources:** All test resources are tagged with `managed-by: ocfp-test`
4. **Cleanup Regularly:** Schedule periodic cleanup jobs for orphaned resources
5. **Limit Scope:** Run only necessary tests during development
6. **Use LocalStack:** Prefer LocalStack for local development when possible

## Test Coverage

Current integration test coverage includes:

- ✅ VPC lifecycle (create, get, list, delete)
- ✅ Subnet lifecycle with Internet Gateway and routing
- ✅ Elastic IP allocation and release
- ✅ SSH key pair management
- ✅ Security group and rule management
- ✅ EBS volume lifecycle
- ✅ EBS snapshot lifecycle
- ✅ S3 bucket lifecycle
- ✅ Application Load Balancer lifecycle
- ✅ Cross-service resource orchestration
- ⚠️ EC2 instance lifecycle (requires AMI selection - skipped in basic tests)
- ⚠️ Volume attachment to instances (requires running instance)
- ⚠️ Elastic IP association to instances (requires running instance)
- ⚠️ Load balancer target registration (requires running instances)

## Contributing

When adding new integration tests:

1. Follow existing test patterns and naming conventions
2. Use `testing.Short()` to skip in short mode
3. Implement proper cleanup with `defer`
4. Use unique resource names with timestamps
5. Add appropriate timeouts for long-running operations
6. Document any new IAM permissions required
7. Update this README with new test coverage
