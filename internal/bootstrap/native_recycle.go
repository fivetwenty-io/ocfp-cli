package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/recycle"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const nativeRecycleJournalType = "native_boot_refresh"

var errNativeIdentity = errors.New("native recycle identity does not match recorded bastion/data disk")

// NativeBastionRecycle replaces only the boot disk of a cloud bastion.
type NativeBastionRecycle struct {
	mgr        *Manager
	compute    cpi.ComputeManager
	boot       cpi.BootRefreshManager
	guestCheck func(context.Context, *cpi.Instance, *recycle.NativeJournal) error
}

func NewNativeBastionRecycle(mgr *Manager) (*NativeBastionRecycle, error) {
	compute := mgr.provider.ComputeManager()

	boot, ok := compute.(cpi.BootRefreshManager)
	if !ok {
		return nil, errors.New("provider lacks native boot refresh")
	}

	return &NativeBastionRecycle{mgr: mgr, compute: compute, boot: boot}, nil
}

// NativeRecycle reports whether this manager uses the native cloud runner.
func (m *Manager) NativeRecycle() bool {
	return strings.EqualFold(m.options.Provider, "aws") || strings.EqualFold(m.options.Provider, "gcp")
}
func (c *NativeBastionRecycle) name() string { return c.mgr.options.BlocName + "-bastion" }

// Run locks before reloading authoritative state, preventing concurrent recycle mutations.
func (c *NativeBastionRecycle) Run(ctx context.Context, abort, pause bool) error {
	if err := c.mgr.stateManager.Lock(c.mgr.options.BlocName); err != nil {
		return err
	}
	defer c.mgr.stateManager.Unlock(c.mgr.options.BlocName) //nolint:errcheck // lock release cannot repair an operation failure

	if _, err := c.mgr.stateManager.Load(c.mgr.options.BlocName); err != nil {
		return err
	}

	engine := recycle.NewNative(c, pause)
	if abort {
		return engine.Abort(ctx)
	}

	return engine.Run(ctx)
}

func (c *NativeBastionRecycle) Plan(ctx context.Context) (*recycle.NativeJournal, error) {
	recorded, err := c.inspectRecorded(ctx)
	if err != nil {
		return nil, err
	}

	old, inst, identity := recorded.old, recorded.instance, recorded.identity

	if err = c.validateFlavor(ctx, inst.Flavor); err != nil {
		return nil, err
	}

	if !c.mgr.config.Bastion.Data.Enabled {
		return nil, errors.New("native recycle requires enabled preserved bastion data")
	}

	image := c.mgr.config.Bastion.Image
	if image == "" {
		return nil, errors.New("replacement image is required")
	}

	if strings.EqualFold(c.mgr.options.Provider, "aws") {
		var err error

		image, err = c.mgr.resolveImageID(ctx, image)
		if err != nil {
			return nil, err
		}
	}
	// Provider-native GetImage resolves qualified image IDs.
	resolved, err := c.compute.GetImage(ctx, image)
	if err != nil {
		return nil, err
	}

	if resolved == nil || resolved.ID == "" {
		return nil, errors.New("replacement image was not resolved")
	}

	key, err := c.privateKey()
	if err != nil {
		return nil, err
	}

	publicKey := c.mgr.bastionPublicKey()
	if data, readErr := os.ReadFile(key + ".pub"); readErr == nil { // #nosec G304 -- selected local bastion key path from trusted operator configuration
		publicKey = strings.TrimSpace(string(data))
	}

	if publicKey == "" {
		return nil, errors.New("bastion public key unavailable")
	}

	if err = c.resolveCloudflareToken(); err != nil {
		return nil, err
	}

	req := c.mgr.buildInstanceRequest(c.name(), inst.Flavor, resolved.ID, inst.NetworkID, inst.SubnetID, inst.AvailabilityZone, "", generateBastionUserData(c.mgr.config), true, 0)
	req.PublicKey = publicKey
	req.DataDisk.AuthorizedKey = publicKey
	req.StaticPrivateIP = inst.PrivateIP
	req.StaticPrivateIPPrefix = c.mgr.bastionStaticIPPrefix()
	req.OnLinkRoutes = c.mgr.bastionOnLinkRoutes()
	req.Tailscale = c.mgr.bastionTailscaleSpec(c.name(), req.StaticPrivateIP, req.StaticPrivateIPPrefix)
	req.Cloudflare = c.mgr.bastionCloudflareSpec()

	req.Ingress = c.mgr.bastionIngressSpec()
	if err = c.mgr.prepareCloudBastion(req); err != nil {
		return nil, err
	}

	identity.ReplacementUserData = req.UserData

	mount := req.DataDisk.Mountpoint
	if mount == "" {
		mount = "/data"
	}

	j := &recycle.NativeJournal{Version: 1, RecordedInstanceID: old.ID, Token: strings.ToLower(rand.Text()), Identity: *identity, Image: resolved.ID, Tags: c.mgr.baseTags(), SSHUser: c.mgr.bastionDefaultUsername(), SSHKeyPath: key, VerificationCommand: nativeGuestVerificationForProvider(c.mgr.options.Provider, mount, req.DataDisk.HomeDir)}

	if err = c.checkGuest(ctx, inst, j); err != nil {
		return nil, fmt.Errorf("original guest/data readiness: %w", err)
	}

	return j, nil
}
func (c *NativeBastionRecycle) privateKey() (string, error) {
	dir := config.OcfpSSHKeyDir(c.mgr.options.BlocName)
	if c.mgr.config.Bastion.SSHKeyDir != "" {
		dir = c.mgr.config.Bastion.SSHKeyDir
	}

	names := []string{"id_ed25519", "id_rsa"}
	if c.mgr.config.Bastion.SSHKeyName != "" {
		names = []string{c.mgr.config.Bastion.SSHKeyName}
	}

	for _, name := range names {
		path := filepath.Join(dir, name)

		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}

	return "", errors.New("local bastion SSH private key unavailable")
}
func (c *NativeBastionRecycle) resolveCloudflareToken() error {
	if config.ResolveIngressProvider(c.mgr.config) != config.IngressProviderCloudflared {
		return nil
	}

	safe := c.mgr.tailscaleSafe()
	if safe == nil {
		return errors.New("vault unavailable for existing Cloudflare connector token")
	}

	token, err := safe.GetString("secret/config/"+c.mgr.options.BlocName+"/cloudflare", "tunnel_token")
	if err != nil {
		return errors.New("existing Cloudflare connector token unavailable")
	}

	if strings.TrimSpace(token) == "" {
		return errors.New("existing Cloudflare connector token empty")
	}

	c.mgr.cloudflareTunnelToken = strings.TrimSpace(token)

	return nil
}
func (c *NativeBastionRecycle) Load(_ context.Context) (*recycle.NativeJournal, error) {
	resources, err := c.mgr.stateManager.ListResources(nativeRecycleJournalType)
	if err != nil {
		return nil, err
	}

	for _, r := range resources {
		if r.Name != c.name() {
			continue
		}

		blob, ok := r.Properties["journal"].(string)
		if !ok || blob == "" {
			return nil, errors.New("empty native recycle journal")
		}

		var j recycle.NativeJournal
		if err = json.Unmarshal([]byte(blob), &j); err != nil {
			return nil, fmt.Errorf("invalid native journal: %w", err)
		}

		return &j, nil
	}

	return nil, nil //nolint:nilnil // no active journal
}
func (c *NativeBastionRecycle) Save(_ context.Context, j *recycle.NativeJournal) error {
	blob, err := json.Marshal(j)
	if err != nil {
		return err
	}

	if err = c.mgr.stateManager.AddResource(&state.Resource{ID: j.Token, Type: nativeRecycleJournalType, Name: c.name(), Provider: c.mgr.options.Provider, State: j.Phase, Properties: map[string]interface{}{"journal": string(blob)}}); err != nil {
		return err
	}

	return c.mgr.stateManager.Save()
}
func (c *NativeBastionRecycle) Prepare(ctx context.Context, j *recycle.NativeJournal) error {
	root, err := c.boot.PrepareBoot(ctx, &cpi.BootRefreshRequest{Identity: j.Identity, Image: j.Image, Token: j.Token, Tags: j.Tags})
	if root != nil && root.ID != "" {
		if j.ReplacementID != "" && j.ReplacementID != root.ID {
			return errNativeIdentity
		}

		if j.Identity.ReplacementFingerprint != "" && j.Identity.ReplacementFingerprint != root.Fingerprint {
			return errNativeIdentity
		}

		j.ReplacementID = root.ID

		j.Identity.ReplacementFingerprint = root.Fingerprint
		if root.Fingerprint == "" {
			return errors.Join(err, errors.New("replacement root fingerprint missing; recover before cleanup"))
		}
	}

	return err
}
func (c *NativeBastionRecycle) Stop(ctx context.Context, j *recycle.NativeJournal) error {
	inst, err := c.compute.GetInstance(ctx, j.Identity.InstanceID)
	if err != nil {
		return err
	}

	if inst == nil {
		return errNativeIdentity
	}

	if inst.State != cpi.ResourceStateStopped {
		actual, inspectErr := c.boot.InspectBoot(ctx, j.Identity.InstanceID)
		if inspectErr != nil {
			return inspectErr
		}

		if actual == nil || (actual.RootVolumeID != j.Identity.RootVolumeID && actual.RootVolumeID != j.ReplacementID) {
			return errNativeIdentity
		}

		if err = c.verifyIdentity(ctx, j, actual.RootVolumeID); err != nil {
			return err
		}

		if err = c.compute.StopInstance(ctx, j.Identity.InstanceID); err != nil {
			return err
		}
	}

	return c.waitPower(ctx, j.Identity.InstanceID, cpi.ResourceStateStopped)
}
func (c *NativeBastionRecycle) Start(ctx context.Context, j *recycle.NativeJournal) error {
	inst, err := c.compute.GetInstance(ctx, j.Identity.InstanceID)
	if err != nil {
		return err
	}

	if inst == nil {
		return errNativeIdentity
	}

	if inst.State != cpi.ResourceStateActive {
		root := j.ReplacementID
		if j.Phase == "abort" {
			root = j.Identity.RootVolumeID
		}

		if err = c.verifyIdentity(ctx, j, root); err != nil {
			return err
		}

		if err = c.compute.StartInstance(ctx, j.Identity.InstanceID); err != nil {
			return err
		}
	}

	if err = c.waitPower(ctx, j.Identity.InstanceID, cpi.ResourceStateActive); err != nil {
		return err
	}

	if j.Phase == "abort" {
		return c.Verify(ctx, j)
	}

	return nil
}
func (c *NativeBastionRecycle) waitPower(ctx context.Context, id string, want cpi.ResourceState) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	for {
		inst, err := c.compute.GetInstance(ctx, id)
		if err != nil {
			return err
		}

		if inst == nil {
			return errNativeIdentity
		}

		if inst.State == want {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
func (c *NativeBastionRecycle) Snapshot(ctx context.Context, j *recycle.NativeJournal) error {
	id, err := c.boot.SnapshotBoot(ctx, &j.Identity, j.Token)
	if id != "" {
		if j.SnapshotID != "" && j.SnapshotID != id {
			return errNativeIdentity
		}

		j.SnapshotID = id
	}

	return err
}
func (c *NativeBastionRecycle) Switch(ctx context.Context, j *recycle.NativeJournal) error {
	return c.boot.SwitchBoot(ctx, &j.Identity, j.Identity.RootVolumeID, j.ReplacementID)
}
func (c *NativeBastionRecycle) Restore(ctx context.Context, j *recycle.NativeJournal) error {
	if j.ReplacementID == "" {
		return errNativeIdentity
	}

	return c.boot.SwitchBoot(ctx, &j.Identity, j.ReplacementID, j.Identity.RootVolumeID)
}
func (c *NativeBastionRecycle) DeleteReplacement(ctx context.Context, j *recycle.NativeJournal) error {
	if j.ReplacementID == "" || j.Identity.ReplacementFingerprint == "" {
		root, err := c.boot.RecoverBoot(ctx, &cpi.BootRefreshRequest{Identity: j.Identity, Image: j.Image, Token: j.Token, Tags: j.Tags})
		if err != nil {
			return err
		}

		if root == nil {
			if j.ReplacementID != "" {
				return errNativeIdentity
			}

			return nil
		}

		if j.ReplacementID != "" && j.ReplacementID != root.ID {
			return errNativeIdentity
		}

		if root.ID == "" || root.Fingerprint == "" {
			return errNativeIdentity
		}

		j.ReplacementID = root.ID

		j.Identity.ReplacementFingerprint = root.Fingerprint
		if err = c.Save(ctx, j); err != nil {
			return err
		}
	}

	if j.ReplacementID == "" || j.ReplacementID == j.Identity.RootVolumeID {
		return errNativeIdentity
	}

	if err := c.verifyIdentity(ctx, j, j.Identity.RootVolumeID); err != nil {
		return err
	}

	return c.boot.DeleteBoot(ctx, j.ReplacementID, j.Identity.ReplacementFingerprint)
}
func (c *NativeBastionRecycle) Retire(ctx context.Context, j *recycle.NativeJournal) error {
	if j.ReplacementID == "" || j.ReplacementID == j.Identity.RootVolumeID {
		return errNativeIdentity
	}

	if err := c.verifyIdentity(ctx, j, j.ReplacementID); err != nil {
		return err
	}

	return c.boot.DeleteBoot(ctx, j.Identity.RootVolumeID, j.Identity.RootFingerprint)
}
func (c *NativeBastionRecycle) verifyIdentity(ctx context.Context, j *recycle.NativeJournal, root string) error {
	actual, err := c.boot.InspectBoot(ctx, j.Identity.InstanceID)
	if err != nil {
		return err
	}

	fingerprint := j.Identity.ReplacementFingerprint
	if root == j.Identity.RootVolumeID {
		fingerprint = j.Identity.RootFingerprint
	}

	if fingerprint == "" || actual == nil || actual.RootFingerprint != fingerprint {
		return errNativeIdentity
	}

	if actual == nil || actual.InstanceID != j.Identity.InstanceID || actual.RootVolumeID != root || (j.Identity.InstanceFingerprint != "" && actual.InstanceFingerprint != j.Identity.InstanceFingerprint) || !slices.Equal(actual.DataVolumeIDs, j.Identity.DataVolumeIDs) || !slices.Equal(actual.PrivateIPs, j.Identity.PrivateIPs) {
		return errNativeIdentity
	}

	return nil
}
func (c *NativeBastionRecycle) Verify(ctx context.Context, j *recycle.NativeJournal) error {
	root := j.ReplacementID
	if j.Phase == "abort" {
		root = j.Identity.RootVolumeID
	}

	if err := c.verifyIdentity(ctx, j, root); err != nil {
		return err
	}

	inst, err := c.compute.GetInstance(ctx, j.Identity.InstanceID)
	if err != nil {
		return err
	}

	if inst == nil || inst.State != cpi.ResourceStateActive {
		return errors.New("bastion not running")
	}

	return c.checkGuest(ctx, inst, j)
}
func (c *NativeBastionRecycle) checkGuest(ctx context.Context, inst *cpi.Instance, j *recycle.NativeJournal) error {
	if c.guestCheck != nil {
		return c.guestCheck(ctx, inst, j)
	}

	if j.SSHUser == "" || j.SSHKeyPath == "" || j.VerificationCommand == "" {
		return errors.New("guest verification connection or command missing")
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	client := ssh.NewClient(&ssh.ConnectionDetails{Host: bastionSSHHost(inst), Port: 22, User: j.SSHUser, PrivateKeyPath: j.SSHKeyPath}, &ssh.ProvisioningOptions{})
	defer client.Close() //nolint:errcheck // preserve readiness failure

	if err := client.Connect(ctx); err != nil {
		return err
	}

	for {
		result, err := client.ExecuteCommand(ctx, j.VerificationCommand)
		if err == nil && result != nil && result.ExitCode == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return errors.New("guest bootstrap/data readiness deadline exceeded")
		case <-time.After(5 * time.Second):
		}
	}
}
func nativeQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }
func nativeGuestVerificationForProvider(provider, mount, home string) string {
	script := `set -eu
cloud-init status --format json | jq -e '.status == "done"' >/dev/null
systemctl is-active --quiet ocfp-dataset.service ocfp-firstboot.service ocfp-bootstrap.service
test -f /var/lib/ocfp/bootstrap-done
test -s /etc/ssh/ssh_host_ed25519_key
device=$(/usr/local/sbin/ocfp-disk-resolve bastion)
test "$(blkid -s LABEL -o value "$device")" = ocfp-data
mountpoint -q -- ` + nativeQuote(mount) + `
mountpoint -q -- ` + nativeQuote(home) + `
test "$(lsblk -ndo MAJ:MIN "$device")" = "$(findmnt -n -o MAJ:MIN --mountpoint ` + nativeQuote(mount) + `)"
test "$(findmnt -n -o MAJ:MIN --mountpoint ` + nativeQuote(home) + `)" = "$(findmnt -n -o MAJ:MIN --mountpoint ` + nativeQuote(mount) + `)"
`

	if strings.EqualFold(provider, "gcp") {
		script = strings.Replace(script, `cloud-init status --format json | jq -e '.status == "done"' >/dev/null`, "", 1)
	}

	return "sudo bash -c " + nativeQuote(script)
}
func (c *NativeBastionRecycle) Complete(ctx context.Context, j *recycle.NativeJournal) error {
	root := j.ReplacementID
	if j.Phase == "abort" {
		root = j.Identity.RootVolumeID
	}

	if err := c.verifyIdentity(ctx, j, root); err != nil {
		return err
	}

	inst, err := c.compute.GetInstance(ctx, j.Identity.InstanceID)
	if err != nil {
		return err
	}

	if inst == nil {
		return errNativeIdentity
	}

	resource, err := c.mgr.stateManager.GetResource(state.ResourceTypeInstance, c.name())
	if err != nil {
		return err
	}

	if resource.ID != j.Identity.InstanceID && resource.ID != j.RecordedInstanceID {
		return errNativeIdentity
	}

	resource.ID = j.Identity.InstanceID
	if resource.Properties == nil {
		resource.Properties = map[string]interface{}{}
	}

	resource.Properties["root_volume_id"] = j.ReplacementID
	if j.Phase == "abort" {
		resource.Properties["root_volume_id"] = j.Identity.RootVolumeID
	} else {
		resource.Properties["image"] = j.Image
	}

	resource.Properties["private_ip"] = inst.PrivateIP

	resource.Properties["public_ip"] = inst.PublicIP
	if err = c.mgr.stateManager.AddResource(resource); err != nil {
		return err
	}

	recovery := &state.Resource{ID: j.Token, Type: "boot_refresh_recovery", Name: j.Token, Provider: c.mgr.options.Provider, State: j.Phase, Properties: map[string]interface{}{"instance_id": j.Identity.InstanceID, "original_root_id": j.Identity.RootVolumeID, "replacement_root_id": j.ReplacementID, "snapshot_id": j.SnapshotID, "image": j.Image}}
	if err = c.mgr.stateManager.AddResource(recovery); err != nil {
		return err
	}

	if err = c.saveNativeOutputs(inst, j); err != nil {
		return err
	}

	if err = c.mgr.stateManager.RemoveResource(nativeRecycleJournalType, c.name()); err != nil {
		return err
	}

	return c.mgr.stateManager.Save()
}

var _ recycle.NativeCluster = (*NativeBastionRecycle)(nil)

type nativeRecorded struct {
	old      *state.Resource
	instance *cpi.Instance
	identity *cpi.BootIdentity
}

func (c *NativeBastionRecycle) inspectRecorded(ctx context.Context) (*nativeRecorded, error) {
	old, err := c.mgr.stateManager.GetResource(state.ResourceTypeInstance, c.name())
	if err != nil {
		return nil, err
	}

	if old.ID == "" {
		return nil, ErrRecycleNoBastion
	}

	data, err := c.mgr.stateManager.GetResource(state.ResourceTypeVolume, c.mgr.bastionDataVolumeName())
	if err != nil {
		return nil, err
	}

	if !nativePreservedVolume(data) {
		return nil, ErrRecycleNoDataVolume
	}

	inst, err := c.compute.GetInstance(ctx, old.ID)
	if err != nil {
		return nil, err
	}

	if inst == nil || inst.Tags["bloc"] != c.mgr.options.BlocName || inst.Tags["role"] != "bastion" {
		return nil, errNativeIdentity
	}

	volume, err := c.compute.GetVolume(ctx, data.ID)
	if err != nil {
		return nil, err
	}

	if volume == nil || volume.ID == "" {
		return nil, ErrRecycleNoDataVolume
	}

	identity, err := c.boot.InspectBoot(ctx, inst.ID)
	if err != nil {
		return nil, err
	}

	if !nativeRecordedIdentity(identity, inst.ID, volume.ID) {
		return nil, errNativeIdentity
	}

	return &nativeRecorded{old: old, instance: inst, identity: identity}, nil
}

func nativePreservedVolume(data *state.Resource) bool {
	return data.ID != "" && (data.Properties["preserve"] == true || data.Properties["preserve"] == "true")
}

func nativeRecordedIdentity(identity *cpi.BootIdentity, instanceID, dataID string) bool {
	return identity != nil && identity.InstanceFingerprint != "" && identity.RootFingerprint != "" && identity.InstanceID == instanceID && slices.Contains(identity.DataVolumeIDs, dataID) && identity.RootVolumeID != dataID
}

func (c *NativeBastionRecycle) validateFlavor(ctx context.Context, current string) error {
	requested := strings.TrimSpace(c.mgr.config.Bastion.Flavor)
	if requested == "" {
		return nil
	}

	resolved, err := c.mgr.resolveFlavorID(ctx, requested)
	if err != nil {
		return err
	}

	if filepath.Base(resolved) != filepath.Base(current) {
		return errors.New("native boot refresh preserves instance flavor; resize the instance separately before recycling")
	}

	return nil
}

func (c *NativeBastionRecycle) saveNativeOutputs(inst *cpi.Instance, j *recycle.NativeJournal) error {
	c.mgr.saveBastionOutputs(inst)

	if err := c.mgr.stateManager.SetOutput("bastion_public_ip", inst.PublicIP); err != nil {
		return err
	}

	if j.SSHUser != "" && j.SSHKeyPath != "" {
		command := "ssh -i " + nativeQuote(j.SSHKeyPath) + " " + nativeQuote(j.SSHUser+"@"+bastionSSHHost(inst))
		if err := c.mgr.stateManager.SetOutput("bastion_ssh_command", command); err != nil {
			return err
		}
	}

	if j.Phase != "abort" {
		if err := c.mgr.stateManager.SetOutput("bastion_image", j.Image); err != nil {
			return err
		}
	}

	return nil
}
