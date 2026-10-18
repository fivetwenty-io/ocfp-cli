package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// fakeDiskStorage stands in for the PVE StorageManager. It embeds a nil
// cpi.StorageManager, so any call outside the two disk methods panics, which
// is how the tests prove the check touches nothing else.
type fakeDiskStorage struct {
	cpi.StorageManager

	disks    map[string]*pve.VMDisks
	readErrs map[string]error
	putErrs  map[string]error
	puts     []fakeDiskPut
}

// fakeDiskPut records one EnableDiskDiscard call.
type fakeDiskPut struct {
	VMID, Slot, Current, Digest, Wrote string
}

func (f *fakeDiskStorage) VMDisks(_ context.Context, instanceID string) (*pve.VMDisks, error) {
	if err := f.readErrs[instanceID]; err != nil {
		return nil, err
	}

	d, ok := f.disks[instanceID]
	if !ok {
		return nil, errors.New("fakeDiskStorage: no such VM " + instanceID)
	}

	return d, nil
}

func (f *fakeDiskStorage) EnableDiskDiscard(_ context.Context, instanceID, slot, current, digest string) (string, error) {
	put := fakeDiskPut{VMID: instanceID, Slot: slot, Current: current, Digest: digest}

	if err := f.putErrs[instanceID]; err != nil {
		f.puts = append(f.puts, put)

		return "", err
	}

	put.Wrote = strings.TrimSuffix(current, ",discard=ignore") + ",discard=on"
	f.puts = append(f.puts, put)

	return put.Wrote, nil
}

// recordingCompute counts every power-state call so the tests can assert the
// disk check never stops, starts, or reboots anything.
type recordingCompute struct {
	stubCompute

	powerCalls []string
}

func (c *recordingCompute) GetInstance(_ context.Context, id string) (*cpi.Instance, error) {
	c.powerCalls = append(c.powerCalls, "status "+id)

	return &cpi.Instance{ID: id, State: cpi.ResourceStateActive}, nil
}

func (c *recordingCompute) StartInstance(_ context.Context, id string) error {
	c.powerCalls = append(c.powerCalls, "start "+id)

	return nil
}

func (c *recordingCompute) StopInstance(_ context.Context, id string) error {
	c.powerCalls = append(c.powerCalls, "stop "+id)

	return nil
}

func (c *recordingCompute) RebootInstance(_ context.Context, id string) error {
	c.powerCalls = append(c.powerCalls, "reboot "+id)

	return nil
}

// diskTestProvider is a stubProvider whose storage manager is the disk fake.
type diskTestProvider struct {
	stubProvider

	storage cpi.StorageManager
}

//nolint:ireturn
func (p *diskTestProvider) StorageManager() cpi.StorageManager { return p.storage }

// thinDisk builds the view of one disk on an lvmthin pool from its value.
func thinDisk(slot, value, discard, pending string) pve.VMDiskSlot {
	volume, _, _ := strings.Cut(value, ",")
	pool, _, _ := strings.Cut(volume, ":")

	return pve.VMDiskSlot{
		Slot: slot, Value: value, VolumeID: volume, Pool: pool,
		PoolType: "lvmthin", Provisioning: pve.ProvisioningThin,
		Discard: discard, PendingValue: pending,
	}
}

// diskTestFixture is a bloc with an artifacts VM (105) and a bastion (102)
// recorded in a temp-dir state, with disks served by the fake.
type diskTestFixture struct {
	ac      *artifactsContext
	storage *fakeDiskStorage
	compute *recordingCompute
}

func newDiskTestFixture(t *testing.T, disks map[string]*pve.VMDisks) *diskTestFixture {
	t.Helper()

	const bloc = "dev"

	sm, err := state.NewManager(filepath.Join(t.TempDir(), ".state"))
	if err != nil {
		t.Fatalf("state.NewManager: %v", err)
	}

	if _, err := sm.Load(bloc); err != nil {
		t.Fatalf("sm.Load: %v", err)
	}

	for _, res := range []*state.Resource{
		{
			ID: "dev-artifacts", Type: artifacts.ResourceType, Name: "dev-artifacts", State: "active",
			Properties: map[string]interface{}{
				"vm_id":          "105",
				"data_volume_id": "local-lvm-data:vm-105-data",
			},
		},
		{ID: "102", Type: state.ResourceTypeInstance, Name: "dev-bastion", State: "active"},
		{
			ID: "local-lvm-data:vm-102-data", Type: state.ResourceTypeVolume, Name: "dev-bastion-data", State: "active",
			Properties: map[string]interface{}{"vm_id": "102", "role": "bastion-data"},
		},
	} {
		if err := sm.AddResource(res); err != nil {
			t.Fatalf("sm.AddResource(%s): %v", res.Name, err)
		}
	}

	storage := &fakeDiskStorage{disks: disks}
	compute := &recordingCompute{}
	provider := &diskTestProvider{stubProvider: stubProvider{compute: compute}, storage: storage}

	lookup, err := artifacts.Lookup(context.Background(), sm, provider, bloc)
	if err != nil || lookup == nil {
		t.Fatalf("artifacts.Lookup = %v, %v", lookup, err)
	}

	return &diskTestFixture{
		ac: &artifactsContext{
			parent:   context.Background(),
			blocName: bloc,
			cfg:      &config.Config{Name: bloc},
			provider: provider,
			state:    sm,
			lookup:   lookup,
		},
		storage: storage,
		compute: compute,
	}
}

// legacyBlocDisks is a bloc whose artifacts data disk predates discard=on and
// carries no serial, beside a bastion whose data disk is already correct. The
// artifacts VM also has a disk ocfp does not manage.
func legacyBlocDisks() map[string]*pve.VMDisks {
	return map[string]*pve.VMDisks{
		"105": {Node: "pve1", VMID: 105, Digest: "a105", Disks: []pve.VMDiskSlot{
			thinDisk("scsi0", "local-lvm-data:vm-105-disk-0,discard=on,size=32G,ssd=1", "on", ""),
			thinDisk("scsi1", "local-lvm-data:vm-105-data,backup=0,size=500G", "", ""),
			thinDisk("scsi2", "local-lvm-data:vm-105-scratch,size=8G", "", ""),
		}},
		"102": {Node: "pve1", VMID: 102, Digest: "b102", Disks: []pve.VMDiskSlot{
			thinDisk("scsi0", "local-lvm-data:vm-102-disk-0,discard=on,size=32G,ssd=1", "on", ""),
			thinDisk("scsi1", "local-lvm-data:vm-102-data,discard=on,serial=ocfpdata,size=64G", "on", ""),
		}},
	}
}

// TestArtifactsDisks_FlagsALegacyDataDiskAndExitsNonZero is the case the
// check exists for. The artifacts data disk sits on thin storage without
// discard=on and is found by its recorded volid alone, since it has no
// serial. The command reports it, prints the remedy, and fails.
func TestArtifactsDisks_FlagsALegacyDataDiskAndExitsNonZero(t *testing.T) {
	t.Parallel()

	fx := newDiskTestFixture(t, legacyBlocDisks())

	var out bytes.Buffer

	err := artifactsDisks(fx.ac, &out, false, false)
	if !errors.Is(err, ErrArtifactsDiskDrift) {
		t.Fatalf("error = %v, want ErrArtifactsDiskDrift", err)
	}

	text := out.String()

	for _, want := range []string{
		"dev-artifacts", "dev-bastion", "scsi1", "local-lvm-data", "lvmthin",
		"missing-on-thin", "ok", "discard=on",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "scsi2") || strings.Contains(text, "vm-105-scratch") {
		t.Errorf("output lists the unmanaged scsi2 disk:\n%s", text)
	}

	if len(fx.storage.puts) != 0 {
		t.Errorf("a check without --fix wrote %d disk(s)", len(fx.storage.puts))
	}

	if len(fx.compute.powerCalls) != 0 {
		t.Errorf("power calls = %v, want none", fx.compute.powerCalls)
	}
}

// TestArtifactsDisks_JSONReport pins the --json shape: one entry per managed
// disk with its role and verdict, and the drift count.
func TestArtifactsDisks_JSONReport(t *testing.T) {
	t.Parallel()

	fx := newDiskTestFixture(t, legacyBlocDisks())

	var out bytes.Buffer

	err := artifactsDisks(fx.ac, &out, false, true)
	if !errors.Is(err, ErrArtifactsDiskDrift) {
		t.Fatalf("error = %v, want ErrArtifactsDiskDrift", err)
	}

	var report artifactsDiskReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}

	if report.Bloc != "dev" || report.Drift != 1 {
		t.Errorf("bloc %q drift %d, want dev 1", report.Bloc, report.Drift)
	}

	type key struct{ vm, slot, role, verdict string }

	got := make([]key, 0, len(report.Disks))
	for _, d := range report.Disks {
		got = append(got, key{d.VM, d.Slot, d.Role, d.Verdict})
	}

	want := []key{
		{"dev-artifacts", "scsi0", "root", "ok"},
		{"dev-artifacts", "scsi1", "data", "missing-on-thin"},
		{"dev-bastion", "scsi0", "root", "ok"},
		{"dev-bastion", "scsi1", "data", "ok"},
	}

	if len(got) != len(want) {
		t.Fatalf("disks = %+v, want %+v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("disk %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if report.Remedy == "" {
		t.Error("JSON report has no remedy for the drifted disk")
	}
}

// TestArtifactsDisks_Verdicts covers each verdict a data disk can get and
// which of them fail the command.
func TestArtifactsDisks_Verdicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		disk        pve.VMDiskSlot
		wantVerdict string
		wantDrift   bool
	}{
		{
			name:        "thin with discard on",
			disk:        thinDisk("scsi1", "local-lvm-data:vm-105-data,discard=on,size=500G", "on", ""),
			wantVerdict: "ok",
		},
		{
			name:        "thin with discard ignore",
			disk:        thinDisk("scsi1", "local-lvm-data:vm-105-data,discard=ignore,size=500G", "ignore", ""),
			wantVerdict: "missing-on-thin",
			wantDrift:   true,
		},
		{
			name: "thin with discard only pending",
			disk: thinDisk("scsi1", "local-lvm-data:vm-105-data,size=500G", "",
				"local-lvm-data:vm-105-data,size=500G,discard=on"),
			wantVerdict: "pending-restart",
		},
		{
			name: "thick lvm",
			disk: pve.VMDiskSlot{
				Slot: "scsi1", Value: "thick:vm-105-data,size=500G", VolumeID: "thick:vm-105-data",
				Pool: "thick", PoolType: "lvm", Provisioning: pve.ProvisioningThick,
			},
			wantVerdict: "thick-not-needed",
		},
		{
			name: "raw file on nfs",
			disk: pve.VMDiskSlot{
				Slot: "scsi1", Value: "nfs-a:105/vm-105-data.raw,size=500G", VolumeID: "nfs-a:105/vm-105-data.raw",
				Pool: "nfs-a", PoolType: "nfs", Provisioning: pve.ProvisioningUnknown,
			},
			wantVerdict: "unknown-pool",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			disks := legacyBlocDisks()
			disks["105"].Disks[1] = tc.disk

			// The thick and file-backed volids differ from state, so the
			// disk is found by its serial instead.
			disks["105"].Disks[1].Value += ",serial=" + config.ArtifactsDataDiskSerial

			fx := newDiskTestFixture(t, disks)

			var out bytes.Buffer

			err := artifactsDisks(fx.ac, &out, false, true)

			if tc.wantDrift != errors.Is(err, ErrArtifactsDiskDrift) {
				t.Fatalf("error = %v, want drift %v", err, tc.wantDrift)
			}

			if !tc.wantDrift && err != nil {
				t.Fatalf("error = %v, want none", err)
			}

			var report artifactsDiskReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("output is not JSON: %v", err)
			}

			if got := report.Disks[1].Verdict; got != tc.wantVerdict {
				t.Errorf("verdict = %q, want %q", got, tc.wantVerdict)
			}
		})
	}
}

// TestArtifactsDisks_RootDiskIsInformational asserts a root disk lacking
// discard is shown but never fails the command.
func TestArtifactsDisks_RootDiskIsInformational(t *testing.T) {
	t.Parallel()

	disks := legacyBlocDisks()
	disks["105"].Disks[0] = thinDisk("scsi0", "local-lvm-data:vm-105-disk-0,size=32G", "", "")
	disks["105"].Disks[1] = thinDisk("scsi1", "local-lvm-data:vm-105-data,discard=on,size=500G", "on", "")

	fx := newDiskTestFixture(t, disks)

	var out bytes.Buffer

	if err := artifactsDisks(fx.ac, &out, false, false); err != nil {
		t.Fatalf("error = %v, want none: a root disk is informational", err)
	}

	if !strings.Contains(out.String(), "root") {
		t.Errorf("output does not show the root disk:\n%s", out.String())
	}
}

// TestArtifactsDisks_ReadFailureFailsTheCommand makes sure a VM whose disks
// cannot be read is reported and fails the command, rather than reading as
// "nothing to fix".
func TestArtifactsDisks_ReadFailureFailsTheCommand(t *testing.T) {
	t.Parallel()

	disks := legacyBlocDisks()
	disks["105"].Disks[1] = thinDisk("scsi1", "local-lvm-data:vm-105-data,discard=on,size=500G", "on", "")

	fx := newDiskTestFixture(t, disks)
	fx.storage.readErrs = map[string]error{"102": errors.New("pve unreachable")}

	var out bytes.Buffer

	err := artifactsDisks(fx.ac, &out, false, false)
	if !errors.Is(err, ErrArtifactsDiskCheckFailed) {
		t.Fatalf("error = %v, want ErrArtifactsDiskCheckFailed", err)
	}

	if !strings.Contains(out.String(), "pve unreachable") {
		t.Errorf("output does not name the read failure:\n%s", out.String())
	}
}

// TestArtifactsDisks_UnsupportedProvider covers a provider with no disk view.
func TestArtifactsDisks_UnsupportedProvider(t *testing.T) {
	t.Parallel()

	fx := newDiskTestFixture(t, legacyBlocDisks())
	fx.ac.provider = &diskTestProvider{stubProvider: stubProvider{compute: fx.compute}}

	var out bytes.Buffer

	if err := artifactsDisks(fx.ac, &out, false, false); !errors.Is(err, ErrArtifactsDisksUnsupported) {
		t.Errorf("error = %v, want ErrArtifactsDisksUnsupported", err)
	}
}

// TestArtifactsDisks_FixWritesOnlyTheDriftedDataDisk pins what --fix may
// touch. Of a root disk lacking discard, a thin data disk lacking it, and a
// thick data disk lacking it, only the thin data disk is written, once, with
// its full current value and the digest it was read under. No VM is stopped,
// started, rebooted, or even asked for its power state.
func TestArtifactsDisks_FixWritesOnlyTheDriftedDataDisk(t *testing.T) {
	t.Parallel()

	disks := legacyBlocDisks()
	disks["105"].Disks[0] = thinDisk("scsi0", "local-lvm-data:vm-105-disk-0,size=32G,ssd=1", "", "")
	disks["102"].Disks[1] = pve.VMDiskSlot{
		Slot: "scsi1", Value: "thick:vm-102-data,serial=ocfpdata,size=64G", VolumeID: "thick:vm-102-data",
		Pool: "thick", PoolType: "lvm", Provisioning: pve.ProvisioningThick,
	}

	fx := newDiskTestFixture(t, disks)

	var out bytes.Buffer

	if err := artifactsDisks(fx.ac, &out, true, false); err != nil {
		t.Fatalf("artifactsDisks --fix: %v\n%s", err, out.String())
	}

	want := fakeDiskPut{
		VMID:    "105",
		Slot:    "scsi1",
		Current: "local-lvm-data:vm-105-data,backup=0,size=500G",
		Digest:  "a105",
		Wrote:   "local-lvm-data:vm-105-data,backup=0,size=500G,discard=on",
	}

	if len(fx.storage.puts) != 1 || fx.storage.puts[0] != want {
		t.Fatalf("writes = %+v, want exactly %+v", fx.storage.puts, want)
	}

	if len(fx.compute.powerCalls) != 0 {
		t.Errorf("power calls = %v, want none", fx.compute.powerCalls)
	}

	text := out.String()

	for _, want := range []string{
		"cold stop and start",
		"guest reboot",
		"data is untouched",
		"ocfp artifacts provision",
		"fstrim.timer",
		"sudo fstrim -v",
		"pending-restart",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

// TestArtifactsDisks_FixJSONReportsTheWrittenValue pins the --fix --json
// shape: the fixed disk carries the value written and reads as
// pending-restart, and the guidance travels in next_steps.
func TestArtifactsDisks_FixJSONReportsTheWrittenValue(t *testing.T) {
	t.Parallel()

	fx := newDiskTestFixture(t, legacyBlocDisks())

	var out bytes.Buffer

	if err := artifactsDisks(fx.ac, &out, true, true); err != nil {
		t.Fatalf("artifactsDisks --fix --json: %v", err)
	}

	var report artifactsDiskReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}

	data := report.Disks[1]
	if !data.Fixed || data.Verdict != verdictPendingRestart ||
		data.NewValue != "local-lvm-data:vm-105-data,backup=0,size=500G,discard=on" {
		t.Errorf("fixed disk = %+v, want fixed, pending-restart, with the written value", data)
	}

	if report.Fixed != 1 || report.Drift != 0 || len(report.NextSteps) == 0 {
		t.Errorf("fixed %d drift %d next_steps %v, want 1, 0, and guidance", report.Fixed, report.Drift, report.NextSteps)
	}
}

// TestArtifactsDisks_FixLeavesEverythingElseAlone asserts --fix writes
// nothing when no data disk is missing-on-thin: a change already queued, an
// unknown pool, and a disk that is already correct.
func TestArtifactsDisks_FixLeavesEverythingElseAlone(t *testing.T) {
	t.Parallel()

	disks := legacyBlocDisks()
	disks["105"].Disks[1] = thinDisk("scsi1", "local-lvm-data:vm-105-data,size=500G", "",
		"local-lvm-data:vm-105-data,size=500G,discard=on")
	disks["102"].Disks[1] = pve.VMDiskSlot{
		Slot: "scsi1", Value: "nfs-a:102/vm-102-data.raw,serial=ocfpdata,size=64G", VolumeID: "nfs-a:102/vm-102-data.raw",
		Pool: "nfs-a", PoolType: "nfs", Provisioning: pve.ProvisioningUnknown,
	}

	fx := newDiskTestFixture(t, disks)

	var out bytes.Buffer

	if err := artifactsDisks(fx.ac, &out, true, false); err != nil {
		t.Fatalf("artifactsDisks --fix: %v", err)
	}

	if len(fx.storage.puts) != 0 {
		t.Errorf("writes = %+v, want none", fx.storage.puts)
	}

	if strings.Contains(out.String(), "fstrim") {
		t.Errorf("post-fix guidance printed although nothing was written:\n%s", out.String())
	}
}

// TestArtifactsDisks_FixReportsAFailedWrite makes sure a refused write is
// shown and fails the command, even when another disk was fixed in the same
// run. Partial success must never read as success.
func TestArtifactsDisks_FixReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	disks := legacyBlocDisks()
	disks["102"].Disks[1] = thinDisk("scsi1", "local-lvm-data:vm-102-data,serial=ocfpdata,size=64G", "", "")

	fx := newDiskTestFixture(t, disks)
	fx.storage.putErrs = map[string]error{"102": errors.New("config file has been modified by another user")}

	var out bytes.Buffer

	err := artifactsDisks(fx.ac, &out, true, false)
	if !errors.Is(err, ErrArtifactsDiskFixFailed) {
		t.Fatalf("error = %v, want ErrArtifactsDiskFixFailed", err)
	}

	if len(fx.storage.puts) != 2 {
		t.Errorf("writes = %+v, want an attempt on each drifted disk", fx.storage.puts)
	}

	text := out.String()

	if !strings.Contains(text, "config file has been modified by another user") {
		t.Errorf("output does not name the failed write:\n%s", text)
	}

	if !strings.Contains(text, "missing-on-thin") {
		t.Errorf("the disk whose write failed is not still shown as missing-on-thin:\n%s", text)
	}
}
