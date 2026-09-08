package pve

import (
	"context"
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestVolumeNameAndFormat pins the pure decision: file-backed plugin types
// get a ".qcow2" extension and a matching format, block plugin types keep
// the bare name and an empty format, and a name that already carries an
// extension is left alone.
func TestVolumeNameAndFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		storageType string
		volName     string
		wantName    string
		wantFormat  string
	}{
		{storageType: "nfs", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "dir", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "cifs", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "glusterfs", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "cephfs", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "btrfs", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "NFS", volName: "vm-20001-data", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "nfs", volName: "vm-20001-data.raw", wantName: "vm-20001-data.raw", wantFormat: "raw"},
		{storageType: "nfs", volName: "vm-20001-data.qcow2", wantName: "vm-20001-data.qcow2", wantFormat: "qcow2"},
		{storageType: "lvmthin", volName: "vm-20001-data", wantName: "vm-20001-data", wantFormat: ""},
		{storageType: "lvm", volName: "vm-20001-data", wantName: "vm-20001-data", wantFormat: ""},
		{storageType: "zfspool", volName: "vm-20001-data", wantName: "vm-20001-data", wantFormat: ""},
		{storageType: "rbd", volName: "vm-20001-data", wantName: "vm-20001-data", wantFormat: ""},
		{storageType: "iscsi", volName: "vm-20001-data", wantName: "vm-20001-data", wantFormat: ""},
	}

	for _, tc := range tests {
		t.Run(tc.storageType+"/"+tc.volName, func(t *testing.T) {
			t.Parallel()

			gotName, gotFormat := volumeNameAndFormat(tc.volName, tc.storageType)
			if gotName != tc.wantName || gotFormat != tc.wantFormat {
				t.Errorf("volumeNameAndFormat(%q, %q) = (%q, %q), want (%q, %q)",
					tc.volName, tc.storageType, gotName, gotFormat, tc.wantName, tc.wantFormat)
			}
		})
	}
}

// newVolumeTestClient wires a StorageManager over a fakePVEClient whose
// GET /storage/{pool} answers with the given plugin type. The real upstream
// storage service runs on top of the fake, so the POST body it records is
// exactly what PVE would receive.
func newVolumeTestClient(pool, storageType string) (*StorageManager, *fakePVEClient) {
	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/storage/" + pool: map[string]interface{}{"storage": pool, "type": storageType},
		},
	}

	client := &Client{
		config:    &Config{Node: "pvupvecf101", DefaultStorage: pool},
		pveClient: fake,
	}

	return &StorageManager{client: client}, fake
}

// TestCreateVolumeShapesNameForStorageType drives CreateVolume end to end
// against a fake API: an NFS pool must receive a ".qcow2" filename with
// format=qcow2, and an LVM-thin pool must receive the bare name with no
// format key, which is the behaviour that already worked on block pools.
func TestCreateVolumeShapesNameForStorageType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		storageType  string
		wantFilename string
		wantFormat   string
		wantFormatOK bool
	}{
		{name: "nfs pool", storageType: "nfs", wantFilename: "vm-20001-data.qcow2", wantFormat: "qcow2", wantFormatOK: true},
		{name: "dir pool", storageType: "dir", wantFilename: "vm-20001-data.qcow2", wantFormat: "qcow2", wantFormatOK: true},
		{name: "lvmthin pool", storageType: "lvmthin", wantFilename: "vm-20001-data", wantFormatOK: false},
		{name: "zfspool pool", storageType: "zfspool", wantFilename: "vm-20001-data", wantFormatOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr, fake := newVolumeTestClient("pvuproxcf1_ns_1", tc.storageType)

			vol, err := mgr.CreateVolume(context.Background(), &cpi.VolumeRequest{
				Name:       "ocfp-lab-artifacts-data",
				SizeGB:     50,
				Type:       "pvuproxcf1_ns_1",
				InstanceID: "20001",
			})
			if err != nil {
				t.Fatalf("CreateVolume: %v", err)
			}

			if fake.postCalls != 1 {
				t.Fatalf("postCalls = %d, want 1", fake.postCalls)
			}

			params := fake.postParams[0]

			if got := params["filename"]; got != tc.wantFilename {
				t.Errorf("filename = %v, want %q", got, tc.wantFilename)
			}

			format, ok := params["format"]
			if ok != tc.wantFormatOK {
				t.Errorf("format present = %v, want %v (params %v)", ok, tc.wantFormatOK, params)
			}

			if tc.wantFormatOK && format != tc.wantFormat {
				t.Errorf("format = %v, want %q", format, tc.wantFormat)
			}

			if got := params["vmid"]; got != 20001 {
				t.Errorf("vmid = %v, want 20001", got)
			}

			if vol.Name != tc.wantFilename {
				t.Errorf("Volume.Name = %q, want %q", vol.Name, tc.wantFilename)
			}
		})
	}
}

// TestCreateVolumeCachesStorageType proves the plugin type lookup hits the
// API once per pool: a second CreateVolume on the same pool must not issue
// another GET /storage/{pool}.
func TestCreateVolumeCachesStorageType(t *testing.T) {
	t.Parallel()

	mgr, fake := newVolumeTestClient("pvuproxcf1_ns_1", "nfs")

	for range 2 {
		_, err := mgr.CreateVolume(context.Background(), &cpi.VolumeRequest{
			Name: "ocfp-lab-artifacts-data", SizeGB: 50, InstanceID: "20001",
		})
		if err != nil {
			t.Fatalf("CreateVolume: %v", err)
		}
	}

	if fake.getCalls != 1 {
		t.Errorf("getCalls = %d, want 1 (storage type should be cached)", fake.getCalls)
	}

	if fake.postCalls != 2 {
		t.Errorf("postCalls = %d, want 2", fake.postCalls)
	}
}

// TestCreateVolumeFailsWhenStorageTypeUnknown refuses to guess: when PVE
// does not report a plugin type, CreateVolume returns an error before
// posting anything rather than silently falling back to block naming.
func TestCreateVolumeFailsWhenStorageTypeUnknown(t *testing.T) {
	t.Parallel()

	fake := &fakePVEClient{
		getResponses: map[string]interface{}{
			"/storage/mystery": map[string]interface{}{"storage": "mystery"},
		},
	}
	mgr := &StorageManager{client: &Client{
		config:    &Config{Node: "pvupvecf101", DefaultStorage: "mystery"},
		pveClient: fake,
	}}

	_, err := mgr.CreateVolume(context.Background(), &cpi.VolumeRequest{Name: "x", SizeGB: 1, InstanceID: "1"})
	if !errors.Is(err, ErrStorageTypeUnknown) {
		t.Fatalf("err = %v, want ErrStorageTypeUnknown", err)
	}

	if fake.postCalls != 0 {
		t.Errorf("postCalls = %d, want 0", fake.postCalls)
	}
}
