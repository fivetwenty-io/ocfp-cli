# 2. PVE Foundation — The Host Makes Us Welcome

Every cloud provider asks us to establish an identity before it will take our
API calls, and Proxmox is no different. In this chapter we create the service
account that all OCFP automation will act as, mint its API token, and confirm
the host's storage and template arrangements. It is the only chapter that
requires root's credentials — though no longer a root shell on the hypervisor
itself — and everything after this flows through the token we mint here.

## Why this one step stays manual

The PVE user database lives on the host, behind a root login, and the account
we need does not exist yet — there is nothing for automation to authenticate
*as*. That circularity is a security feature, not an oversight: only a human
holding root credentials can bootstrap the initial credential. We do it once
per host, and the token we mint here carries every future action. With pmx we
can even do it without leaving the workstation: a temporary context
authenticated as `root@pam` performs the bootstrap, and the moment the CPI
token exists, root retires from daily use.

## Creating the CPI service account

First we point pmx at the host and authenticate as root — a session ticket,
held only for the minutes this chapter takes. The `${PVE_ROOT_PASSWORD}`
reference is resolved from our environment at login time, so the password
never sits in the config file:

```bash
pmx context add <context> --host <node> --product pve \
  --auth-type password --username root --realm pam \
  --secret '${PVE_ROOT_PASSWORD}'
pmx auth login --context <context>
```

**On the host (native):** or we open a root session — over Tailscale in our
lab — and stay there for the next three steps:

```bash
ssh root@<node>
```

First the user. The `@pve` realm is PVE's built-in authentication; no LDAP or
PAM wiring required:

```bash
pmx pve access user create ocfp-cpi@pve --comment "OCFP CPI service account"
```

**On the host (native):**

```bash
pveum user add ocfp-cpi@pve --comment "OCFP CPI service account"
```

Next the role. The CPI needs to clone, configure, start, stop, and destroy
VMs; allocate disks; and attach to the SDN. We grant exactly that:

```bash
pmx pve access role create OCFPCpi --privs \
"Datastore.AllocateSpace,Datastore.Audit,Pool.Allocate,SDN.Use,\
Sys.Audit,Sys.Console,Sys.Modify,VM.Allocate,VM.Audit,VM.Clone,\
VM.Config.CDROM,VM.Config.Cloudinit,VM.Config.CPU,VM.Config.Disk,\
VM.Config.HWType,VM.Config.Memory,VM.Config.Network,VM.Config.Options,\
VM.Migrate,VM.Monitor,VM.PowerMgmt"
```

**On the host (native):**

```bash
pveum role add OCFPCpi --privs \
"Datastore.AllocateSpace,Datastore.Audit,Pool.Allocate,SDN.Use,\
Sys.Audit,Sys.Console,Sys.Modify,VM.Allocate,VM.Audit,VM.Clone,\
VM.Config.CDROM,VM.Config.Cloudinit,VM.Config.CPU,VM.Config.Disk,\
VM.Config.HWType,VM.Config.Memory,VM.Config.Network,VM.Config.Options,\
VM.Migrate,VM.Monitor,VM.PowerMgmt"
```

Then the grants. We bind the role at the root path so the account reaches
VMs, pools, and the SDN. We also add explicit grants on the storage pools
that will hold disks and images: in our lab, the LVM-thin pool
`local-lvm-data` for VM and persistent disks, and `local` for ISOs and
stemcell images:

```bash
pmx pve access acl set --path / --users ocfp-cpi@pve --roles OCFPCpi
pmx pve access acl set --path /storage/local-lvm-data --users ocfp-cpi@pve --roles OCFPCpi
pmx pve access acl set --path /storage/local --users ocfp-cpi@pve --roles OCFPCpi
```

**On the host (native):**

```bash
pveum acl modify / --users ocfp-cpi@pve --roles OCFPCpi
pveum acl modify /storage/local-lvm-data --users ocfp-cpi@pve --roles OCFPCpi
pveum acl modify /storage/local --users ocfp-cpi@pve --roles OCFPCpi
```

**Verify**: `pmx pve access user list` shows the user and
`pmx pve access acl list --path /` shows the grants (natively:
`pveum user list | grep ocfp-cpi` and `pveum acl list | grep ocfp-cpi`).

## Minting the token

The token name scopes the credential to our bloc, which makes rotation and
revocation surgical when we run several blocs against one host:

```bash
pmx pve access user token create ocfp-cpi@pve <bloc-token-name> --privsep=false
```

**On the host (native):**

```bash
pveum user token add ocfp-cpi@pve <bloc-token-name> --privsep 0
```

Note the flag inversion: pmx spells it `--privsep=false` where pveum spells
it `--privsep 0` — the same switch. Either way the token inherits the user's
full privileges — required, since the CPI operates without per-privilege
token grants. The output prints the token exactly once:

- The token ID, `ocfp-cpi@pve!<bloc-token-name>` — this becomes `auth_token`
  in the bloc config.

- The secret UUID — this becomes `token_secret`. Copy it now; PVE will never
  show it again.

Now we prove the token works from the workstation — the vantage point the
CPI will actually use. We rewire our context from the root session to the
new token and ask the API who we are:

```bash
pmx auth set-token --context <context> \
  --token-id 'ocfp-cpi@pve!<bloc-token-name>' \
  --secret '${OCFP_TOKEN_SECRET}'
pmx auth whoami --context <context>
pmx version --context <context>
```

The `${OCFP_TOKEN_SECRET}` reference is deliberate: pmx resolves it from the
environment on every call, so the secret never lands in the config file in
cleartext.

**On the host (native):** the same proof with nothing but curl:

```bash
curl -sk \
  -H "Authorization: PVEAPIToken=ocfp-cpi@pve!<bloc-token-name>=<secret-uuid>" \
  https://localhost:8006/api2/json/version | python3 -m json.tool
```

**Verify**: `pmx auth whoami` reports the identity
`ocfp-cpi@pve!<bloc-token-name>` and `pmx version` reports the server's PVE
version — end-to-end, over the network we will actually use. Natively, the
`curl` returns a JSON payload with the PVE version.

**Rollback**: `pmx pve access user delete ocfp-cpi@pve --yes` (natively
`pveum user delete ocfp-cpi@pve`) removes the user, all tokens, and every
ACL entry in one stroke.

Two halves, two fields — worth engraving now because it is the most common
first-run stumble: `auth_token` is the ID only, `token_secret` is the UUID
only, and the client joins them with `=` on our behalf. Pasting the combined
`id=uuid` string into either field earns a 401.

## Storage, honestly assessed

Our nested lab keeps storage simple, and the simplicity is a rule rather
than a shortcut. Inside a nested PVE guest we use **LVM-thin only** — running
ZFS or Ceph on top of the outer host's copy-on-write storage compounds write
amplification for no benefit. So the pools we point OCFP at are:

| Pool | Content | Role |
|------|---------|------|
| `local-lvm-data` | VM disks, persistent disks | Everything the CPI allocates |
| `local` | ISO images, snippets, stemcell images | Template and stemcell staging |

Two mechanical checks save us grief later. The stemcell and ISO pool must
actually advertise the content types we will push at it
(`pmx pve storage node-list --node <node>` — natively `pvesm status` — and
the Datacenter → Storage panel confirm). Thin provisioning must be real,
too — a pool of thick-provisioned volumes fills by *reservation* long before
it fills with data.

One more switch while we are here: the datacenter firewall. OCFP's bootstrap
creates PVE security groups for the VMs it manages, which requires
**Datacenter → Firewall → Options → Firewall: Yes**. Enabling the datacenter
firewall does not, by itself, filter anything — rules attach per-VM — so this
is safe to flip now.

## Templates — the part we no longer do by hand

Earlier iterations of these runbooks had us downloading cloud images,
running a ritual of `qm create` and `qm importdisk`, and hand-authoring
cloud-init snippets. That era is over, and it is worth saying why so nobody
resurrects it: PVE 9.x rejects snippet uploads through the storage API
entirely, so the CLI moved to a fully API-driven flow. When the template a
bloc names is missing, OCFP downloads the Ubuntu cloud image *on the PVE
node*, builds the VM, and converts it to a template. VMIDs come from the
9000+ range — no SSH to the host, no snippets anywhere. Per-VM configuration
rides in SMBIOS fields the guest reads at first boot.

The catalog names are case-sensitive, and we choose by role:

- `ubuntu-noble-template`
  Ubuntu 24.04, vanilla — for artifacts, jumpbox-style VMs, and anything that
  needs no first-boot magic.

- `ubuntu-noble-bastion-template`
  The same image with the OCFP firstboot and watchdog units pre-baked —
  required for Tailscale-enabled bastions. Its first build on a cluster takes
  a few extra minutes while OCFP drives the serial console to prepare it;
  every later bastion clones in about thirty seconds.

We do nothing now except make sure our bloc config (next chapter) names a
catalog entry. The provisioning happens lazily, on first use, and it is
idempotent.

## What we carry forward

Three facts leave this chapter with us, destined for the bloc config: the API
endpoint `https://<node>:8006`, the token ID, and the token secret. They now
live in two places — the bloc config that OCFP reads, and the pmx context we
rewired above — the same credential wearing two coats. The secret is a
password — it travels through a password manager or Vault, never through a
repo.

**Verify**, once more from the workstation, because this is the last moment a
failure is purely about credentials:

```bash
pmx pve node list --context <context>
```

**On the host (native):** the same list with curl — though this one names
`<node>` rather than `localhost`, so it proves the path from anywhere the
API is reachable:

```bash
curl -sk \
  -H "Authorization: PVEAPIToken=<auth_token>=<token_secret>" \
  https://<node>:8006/api2/json/nodes | python3 -m json.tool
```

A node list means the host is ready for us. Now we write the bloc:
[3. Bloc definition](03-bloc-definition.md).
