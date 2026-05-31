package aws

import (
	"context"
	"testing"
)

// ---- validateRegion with custom endpoint ------------------------------------

func TestClientValidateRegion_CustomEndpoint_AllowsNonStandardRegion(t *testing.T) {
	t.Parallel()

	config := &Config{
		Region:      "custom-local",
		EC2Endpoint: "http://localhost:4566",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.config = config

	err = client.validateRegion(context.Background())
	if err != nil {
		t.Errorf("validateRegion with EC2Endpoint: expected nil, got %v", err)
	}
}

func TestClientValidateRegion_EndpointURL_AllowsNonStandardRegion(t *testing.T) {
	t.Parallel()

	config := &Config{
		Region:      "localstack-region",
		EndpointURL: "http://localhost:4566",
	}

	client, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.config = config

	err = client.validateRegion(context.Background())
	if err != nil {
		t.Errorf("validateRegion with EndpointURL: expected nil, got %v", err)
	}
}
