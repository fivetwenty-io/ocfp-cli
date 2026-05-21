package pve

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestPVEVolumeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reqName string
		vmid    int
		want    string
	}{
		{name: "vmid 0 + name → name", reqName: "ocfp-wayne-artifacts-data", vmid: 0, want: "ocfp-wayne-artifacts-data"},
		{name: "vmid set + name → vm-{vmid}-{suffix}", reqName: "ocfp-wayne-artifacts-data", vmid: 138, want: "vm-138-data"},
		{name: "vmid set + single-word name → vm-{vmid}-{name}", reqName: "scratch", vmid: 9000, want: "vm-9000-scratch"},
		{name: "vmid set + empty name → vm-{vmid}-disk", reqName: "", vmid: 42, want: "vm-42-disk"},
		{name: "vmid set + name already conforming → preserved", reqName: "vm-138-cloudinit", vmid: 138, want: "vm-138-cloudinit"},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := pveVolumeName(tc.reqName, tc.vmid)
			if got != tc.want {
				t.Errorf("pveVolumeName(%q, %d) = %q, want %q", tc.reqName, tc.vmid, got, tc.want)
			}
		})
	}
}

func TestParseVolumeOwnerVMID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		req     *cpi.VolumeRequest
		want    int
		wantErr bool
	}{
		{name: "nil request → 0", req: nil, want: 0},
		{name: "empty InstanceID → 0", req: &cpi.VolumeRequest{}, want: 0},
		{name: "numeric InstanceID → vmid", req: &cpi.VolumeRequest{InstanceID: "143"}, want: 143},
		{name: "non-numeric InstanceID → error", req: &cpi.VolumeRequest{InstanceID: "not-a-vmid"}, wantErr: true},
		{name: "negative InstanceID → error", req: &cpi.VolumeRequest{InstanceID: "-7"}, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseVolumeOwnerVMID(tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseVolumeOwnerVMID: want error, got nil")
				}

				return
			}

			if err != nil {
				t.Fatalf("parseVolumeOwnerVMID: %v", err)
			}

			if got != tc.want {
				t.Errorf("parseVolumeOwnerVMID = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestStorageManagerLocalModeRejects covers the BlobstoreMode=local guard on
// each bucket entrypoint. Local mode must return ErrBucketsNotSupported (or
// the documented empty-result equivalents) without instantiating the S3
// client, so the bootstrap step 7 skip path stays intact.
func TestStorageManagerLocalModeRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode string
		want bool // true = expect ErrBucketsNotSupported
	}{
		{name: "empty mode", mode: "", want: true},
		{name: "local mode", mode: "local", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := newStorageManagerForMode(tt.mode)
			ctx := context.Background()

			_, err := mgr.CreateBucket(ctx, &cpi.BucketRequest{Name: "any"})
			if tt.want && !errors.Is(err, ErrBucketsNotSupported) {
				t.Errorf("CreateBucket: err=%v, want ErrBucketsNotSupported", err)
			}

			_, err = mgr.GetBucket(ctx, "any")
			if tt.want && !errors.Is(err, ErrBucketsNotSupported) {
				t.Errorf("GetBucket: err=%v, want ErrBucketsNotSupported", err)
			}

			err = mgr.DeleteBucket(ctx, "any")
			if tt.want && !errors.Is(err, ErrBucketsNotSupported) {
				t.Errorf("DeleteBucket: err=%v, want ErrBucketsNotSupported", err)
			}

			err = mgr.EmptyBucket(ctx, "any")
			if tt.want && !errors.Is(err, ErrBucketsNotSupported) {
				t.Errorf("EmptyBucket: err=%v, want ErrBucketsNotSupported", err)
			}

			_, err = mgr.IsBucketEmpty(ctx, "any")
			if tt.want && !errors.Is(err, ErrBucketsNotSupported) {
				t.Errorf("IsBucketEmpty: err=%v, want ErrBucketsNotSupported", err)
			}

			// ListBuckets is the one method that degrades gracefully —
			// returns an empty slice in local mode so callers walking all
			// buckets don't bail out.
			buckets, err := mgr.ListBuckets(ctx)
			if err != nil {
				t.Errorf("ListBuckets: unexpected err=%v", err)
			}

			if len(buckets) != 0 {
				t.Errorf("ListBuckets: got %d buckets, want 0", len(buckets))
			}
		})
	}
}

// TestStorageManagerExternalModeRoutesToS3 stands up an httptest server that
// satisfies the minimum S3 surface required by the bucket methods (CreateBucket
// PUT, ListBuckets GET, ListObjectsV2 GET, DeleteBucket DELETE, HEAD bucket).
// The test asserts the manager routes through the S3 client and that the
// requests carry the operator's endpoint host.
func TestStorageManagerExternalModeRoutesToS3(t *testing.T) {
	t.Parallel()

	var (
		seenPaths []string
	)

	srv := httptest.NewServer(stubS3Handler(t, &seenPaths))
	t.Cleanup(srv.Close)

	cfg := &Config{
		BlobstoreMode:      "external",
		BlobstoreEndpoint:  srv.URL,
		BlobstoreRegion:    "us-east-1",
		BlobstoreAccessKey: "AKIA-TEST",
		BlobstoreSecretKey: "secret-test",
		BlobstorePathStyle: true,
		VerifySSL:          false,
	}

	mgr := &StorageManager{client: &Client{config: cfg}}
	ctx := context.Background()

	_, err := mgr.CreateBucket(ctx, &cpi.BucketRequest{Name: "bloc-mgmt-bosh"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	_, err = mgr.GetBucket(ctx, "bloc-mgmt-bosh")
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}

	empty, err := mgr.IsBucketEmpty(ctx, "bloc-mgmt-bosh")
	if err != nil {
		t.Fatalf("IsBucketEmpty: %v", err)
	}

	if !empty {
		t.Errorf("IsBucketEmpty: got false on empty stub bucket, want true")
	}

	got := strings.Join(seenPaths, "|")
	if !strings.Contains(got, "/bloc-mgmt-bosh") {
		t.Errorf("S3 path style not used; seen paths: %v", seenPaths)
	}
}

func newStorageManagerForMode(mode string) *StorageManager {
	return &StorageManager{
		client: &Client{
			config: &Config{BlobstoreMode: mode},
		},
	}
}
