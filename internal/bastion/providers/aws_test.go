package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// fakeEC2Client is a test double for EC2DescribeInstancesAPI.
type fakeEC2Client struct {
	calls  int
	output *ec2.DescribeInstancesOutput
	err    error
}

func (f *fakeEC2Client) DescribeInstances(_ context.Context, _ *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.calls++

	return f.output, f.err
}

// newAWSBastionInitWithFakeEC2 constructs an AWSBastionInit wired with a fake EC2 client.
// The config has no BastionIP, no env var, and no state — so EC2 fallback always triggers.
func newAWSBastionInitWithFakeEC2(blocName string, fake EC2DescribeInstancesAPI) *AWSBastionInit {
	cfg := &config.Config{
		Name:   blocName,
		Region: "us-east-1",
	}

	a := NewAWSBastionInit(cfg)
	a.ec2Client = fake

	return a
}

// TestGetBastionIPFromEC2_ReturnsIPWhenInstanceFound verifies that when EC2 returns
// a running instance with a public IP, getBastionIPFromEC2 returns that IP.
func TestGetBastionIPFromEC2_ReturnsIPWhenInstanceFound(t *testing.T) {
	fake := &fakeEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{
					Instances: []types.Instance{
						{
							InstanceId:      aws.String("i-0abc123456789def0"),
							PublicIpAddress: aws.String("203.0.113.42"),
							State: &types.InstanceState{
								Name: types.InstanceStateNameRunning,
							},
						},
					},
				},
			},
		},
	}

	a := newAWSBastionInitWithFakeEC2("mybloc", fake)

	ip, err := a.getBastionIPFromEC2(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if ip != "203.0.113.42" {
		t.Errorf("expected IP 203.0.113.42, got %q", ip)
	}

	if fake.calls != 1 {
		t.Errorf("expected exactly 1 EC2 call, got %d", fake.calls)
	}
}

// TestGetBastionIPFromEC2_ErrorOnEmptyResults verifies that when EC2 returns no
// reservations, getBastionIPFromEC2 returns ErrCouldNotDetermineBastionIP.
func TestGetBastionIPFromEC2_ErrorOnEmptyResults(t *testing.T) {
	fake := &fakeEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{},
		},
	}

	a := newAWSBastionInitWithFakeEC2("mybloc", fake)

	_, err := a.getBastionIPFromEC2(context.Background())
	if err == nil {
		t.Fatal("expected error for empty results, got nil")
	}

	if !errors.Is(err, ErrCouldNotDetermineBastionIP) {
		t.Errorf("expected ErrCouldNotDetermineBastionIP, got: %v", err)
	}

	// Error message must mention the bastion name so the operator knows which resource is missing.
	const wantFragment = "mybloc-bastion"
	if msg := err.Error(); len(msg) == 0 {
		t.Error("error message is empty")
	} else {
		found := false

		for i := 0; i <= len(msg)-len(wantFragment); i++ {
			if msg[i:i+len(wantFragment)] == wantFragment {
				found = true

				break
			}
		}

		if !found {
			t.Errorf("expected error message to contain %q, got: %q", wantFragment, msg)
		}
	}
}

// TestGetBastionIPFromEC2_ErrorOnAPIFailure verifies that an EC2 API error is
// propagated as a wrapped error from getBastionIPFromEC2.
func TestGetBastionIPFromEC2_ErrorOnAPIFailure(t *testing.T) {
	apiErr := errors.New("RequestExpired: Request has expired")
	fake := &fakeEC2Client{
		err: apiErr,
	}

	a := newAWSBastionInitWithFakeEC2("mybloc", fake)

	_, err := a.getBastionIPFromEC2(context.Background())
	if err == nil {
		t.Fatal("expected error from API failure, got nil")
	}

	if !errors.Is(err, apiErr) {
		t.Errorf("expected wrapped apiErr, got: %v", err)
	}
}

// TestGetBastionIP_StatePresent_NoEC2Call verifies that when the config carries
// an explicit BastionIP, getBastionIP returns it immediately without calling EC2.
func TestGetBastionIP_StatePresent_NoEC2Call(t *testing.T) {
	fake := &fakeEC2Client{
		output: &ec2.DescribeInstancesOutput{},
	}

	cfg := &config.Config{
		Name:      "mybloc",
		Region:    "us-east-1",
		BastionIP: "10.0.1.5",
	}

	a := NewAWSBastionInit(cfg)
	a.ec2Client = fake

	ip, err := a.getBastionIP(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if ip != "10.0.1.5" {
		t.Errorf("expected IP 10.0.1.5, got %q", ip)
	}

	if fake.calls != 0 {
		t.Errorf("expected 0 EC2 calls when BastionIP in config, got %d", fake.calls)
	}
}

// TestGetBastionIP_StateMissing_EC2FallbackInvoked verifies that when no IP is
// available in config or env, getBastionIP falls through to EC2 and returns the
// discovered IP.
func TestGetBastionIP_StateMissing_EC2FallbackInvoked(t *testing.T) {
	fake := &fakeEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{
					Instances: []types.Instance{
						{
							InstanceId:      aws.String("i-0abc123456789def0"),
							PublicIpAddress: aws.String("54.1.2.3"),
							State: &types.InstanceState{
								Name: types.InstanceStateNameRunning,
							},
						},
					},
				},
			},
		},
	}

	// No BastionIP, no env var set; state dir will not exist, so state lookup
	// returns an error and the code falls through to EC2.
	cfg := &config.Config{
		Name:   "mybloc",
		Region: "us-east-1",
	}

	a := NewAWSBastionInit(cfg)
	a.ec2Client = fake

	ip, err := a.getBastionIP(context.Background())
	if err != nil {
		t.Fatalf("expected no error from EC2 fallback, got: %v", err)
	}

	if ip != "54.1.2.3" {
		t.Errorf("expected IP 54.1.2.3 from EC2 fallback, got %q", ip)
	}

	if fake.calls != 1 {
		t.Errorf("expected exactly 1 EC2 call in fallback path, got %d", fake.calls)
	}
}

// TestGetBastionIPFromEC2_SkipsInstancesWithNoPublicIP verifies that instances
// without a public IP are skipped, and the first instance that has one is returned.
func TestGetBastionIPFromEC2_SkipsInstancesWithNoPublicIP(t *testing.T) {
	fake := &fakeEC2Client{
		output: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{
					Instances: []types.Instance{
						{
							InstanceId:      aws.String("i-00000000000000001"),
							PublicIpAddress: nil, // private-only instance
							State: &types.InstanceState{
								Name: types.InstanceStateNameRunning,
							},
						},
						{
							InstanceId:      aws.String("i-00000000000000002"),
							PublicIpAddress: aws.String(""), // empty public IP
							State: &types.InstanceState{
								Name: types.InstanceStateNameRunning,
							},
						},
						{
							InstanceId:      aws.String("i-00000000000000003"),
							PublicIpAddress: aws.String("198.51.100.7"),
							State: &types.InstanceState{
								Name: types.InstanceStateNameRunning,
							},
						},
					},
				},
			},
		},
	}

	a := newAWSBastionInitWithFakeEC2("mybloc", fake)

	ip, err := a.getBastionIPFromEC2(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if ip != "198.51.100.7" {
		t.Errorf("expected IP 198.51.100.7 (first with public IP), got %q", ip)
	}
}

// TestGetBastionIPFromEC2_FilterNames verifies that the DescribeInstances call
// uses the correct tag:Name filter value (bloc + "-bastion").
func TestGetBastionIPFromEC2_FilterNames(t *testing.T) {
	var capturedInput *ec2.DescribeInstancesInput

	fake := &capturingEC2Client{
		capture: func(input *ec2.DescribeInstancesInput) {
			capturedInput = input
		},
		output: &ec2.DescribeInstancesOutput{},
	}

	a := newAWSBastionInitWithFakeEC2("prod-bloc", fake)

	//nolint:errcheck // error not relevant; we only check the captured input
	_, _ = a.getBastionIPFromEC2(context.Background())

	if capturedInput == nil {
		t.Fatal("DescribeInstances was not called")
	}

	wantNameFilter := "prod-bloc-bastion"
	wantStateFilter := "running"

	var foundName, foundState bool

	for _, f := range capturedInput.Filters {
		switch aws.ToString(f.Name) {
		case "tag:Name":
			if len(f.Values) == 1 && f.Values[0] == wantNameFilter {
				foundName = true
			}
		case "instance-state-name":
			if len(f.Values) == 1 && f.Values[0] == wantStateFilter {
				foundState = true
			}
		}
	}

	if !foundName {
		t.Errorf("expected filter tag:Name=%s, filters were: %v", wantNameFilter, capturedInput.Filters)
	}

	if !foundState {
		t.Errorf("expected filter instance-state-name=%s, filters were: %v", wantStateFilter, capturedInput.Filters)
	}
}

// capturingEC2Client captures the DescribeInstancesInput for inspection.
type capturingEC2Client struct {
	capture func(*ec2.DescribeInstancesInput)
	output  *ec2.DescribeInstancesOutput
	err     error
}

func (c *capturingEC2Client) DescribeInstances(_ context.Context, params *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if c.capture != nil {
		c.capture(params)
	}

	return c.output, c.err
}
