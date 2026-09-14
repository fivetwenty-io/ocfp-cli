# 2. PVE Foundation: Where the Host Makes Us Welcome

Every cloud provider asks us to establish an identity before it will take our API calls, and Proxmox is no different. In this chapter we create the service account that all OCFP automation will act as, mint its API token, and confirm the host's storage and template arrangements. It is the only chapter that requires root's credentials, though no longer a root shell on the hypervisor itself, and everything after this flows through the token we mint here.

## Why this one step stays manual

The PVE user database lives on the host, behind a root login, and the account we need does not exist yet, so there is nothing for automation to authenticate *as*. That circularity is a security feature, not an oversight, because only a human holding root credentials can bootstrap the initial credential. We do it once per host, and the token we mint here carries every future action. With pmx we can even do it without leaving the workstation. A temporary context authenticated as `root@pam` performs the bootstrap, and the moment the CPI token exists, root retires from daily use.

## Creating the CPI service account

First we point pmx at the host and authenticate as root, taking a session ticket that we hold only for the minutes this chapter takes. We export the root password into our shell first, as `PVE_ROOT_PASSWORD`. The `${PVE_ROOT_PASSWORD}` reference below is then resolved from our environment at login time, so the password never sits in the config file:

```bash
pmx context add <context> --host <node> --product pve \
  --auth-type password --username root --realm pam \
  --secret '${PVE_ROOT_PASSWORD}' --select --default-node <node> \
  --insecure --force
pmx auth login --context <context>
```

`--force` is there so the step is repeatable. Without it, `context add` refuses a name it already knows, and re-running the chapter from the top fails on a context we are perfectly happy to replace. The CLI's own equivalent of this step passes it for the same reason.

A fresh PVE host answers with the self-signed certificate it generated at install time, so without `--insecure` the login fails certificate verification before it ever reaches the password. Two better options exist once we care. `--tofu` pins whatever certificate the host presents on first connection and complains if it ever changes, and `--fingerprint` takes the hex SHA-256 we already trust. On a lab that we reach over Tailscale, `--insecure` is the honest choice, and it is what every other pmx command in these runbooks passes.

First the user. The `@pve` realm is PVE's built-in authentication, and it requires no LDAP or PAM wiring:

```bash
pmx pve access user create ocfp-cpi@pve --comment "OCFP CPI service account"
```

Next the role. The CPI needs to clone, configure, start, stop, and destroy VMs; allocate disks; and attach to the SDN. We grant exactly that:

```bash
pmx pve access role create OCFPCpi --privs \
"Datastore.Allocate,Datastore.AllocateSpace,Datastore.AllocateTemplate,\
Datastore.Audit,\
Pool.Allocate,Pool.Audit,SDN.Use,Sys.Audit,Sys.Console,Sys.Modify,\
VM.Allocate,VM.Audit,VM.Backup,VM.Clone,VM.Config.CDROM,\
VM.Config.Cloudinit,VM.Config.CPU,\
VM.Config.Disk,VM.Config.HWType,VM.Config.Memory,VM.Config.Network,\
VM.Config.Options,VM.Console,VM.GuestAgent.Audit,\
VM.GuestAgent.Unrestricted,VM.Migrate,VM.PowerMgmt,VM.Snapshot"
```

`VM.Backup` and `VM.Snapshot` are the two that get forgotten, and the omission bites late. Bootstrap needs neither, so a role without them looks healthy for the whole build and then refuses the archive step of `ocfp bastion recycle`, with the bastion already stopped. Grant them here rather than repairing the role under pressure. Adding a privilege later still works, with `pmx pve access role set OCFPCpi --privs "VM.Snapshot,VM.Backup" --append`. The `--append` is not optional, because without it the flag replaces every privilege the role holds.

Anyone porting an older runbook should know that PVE 9 removed the `VM.Monitor` privilege (its ground is now covered by `VM.Console` and the `VM.GuestAgent.*` family), and that the role gains `Datastore.AllocateTemplate` so the lazy template build in chapter 3 can upload images and convert the seed VM. On PVE 8 the old list still works, and the one above works on both.

Then the grants. We bind the role at the root path so the account reaches VMs, pools, and the SDN. We also add explicit grants on the storage pools that will hold disks and images, which in our lab are the LVM-thin pool `local-lvm-data` for VM and persistent disks, and `local` for ISOs and stemcell images:

```bash
pmx pve access acl set --path / --users ocfp-cpi@pve --roles OCFPCpi
pmx pve access acl set --path /storage/local-lvm-data --users ocfp-cpi@pve --roles OCFPCpi
pmx pve access acl set --path /storage/local --users ocfp-cpi@pve --roles OCFPCpi
```

**Verify**: `pmx pve access user list` shows the user and `pmx pve access acl list --path /` shows the grants.

## Minting the token

The token name scopes the credential to our bloc, which makes rotation and revocation surgical when we run several blocs against one host:

```bash
pmx pve access user token create ocfp-cpi@pve <bloc-token-name> --privsep=false
```

With `--privsep=false` the token inherits the user's full privileges, which the CPI needs because it operates without per-privilege token grants. The output prints the token exactly once:

- The token ID is `ocfp-cpi@pve!<bloc-token-name>`, and this becomes `auth_token` in the bloc config.

- The secret UUID becomes `token_secret`. Copy it now, and export it into our shell as `OCFP_TOKEN_SECRET`, so the next fence can resolve it, because PVE will never show the secret again.

Now we prove the token works from the workstation, which is the vantage point the CPI will actually use. We rewire our context from the root session to the new token and ask the API who we are:

```bash
pmx auth set-token --context <context> \
  --token-id 'ocfp-cpi@pve!<bloc-token-name>' \
  --secret '${OCFP_TOKEN_SECRET}'
pmx auth whoami --context <context>
pmx version --context <context>
```

The `${OCFP_TOKEN_SECRET}` reference is deliberate, because pmx resolves it from the environment on every call, so the secret never lands in the config file in cleartext.

**Verify**: `pmx auth whoami` reports the identity `ocfp-cpi@pve!<bloc-token-name>` and `pmx version` reports the server's PVE version, end-to-end, over the network we will actually use.

**Rollback**: `pmx pve access user delete ocfp-cpi@pve --yes` removes the user, all tokens, and every ACL entry in one stroke.

The token has two halves and the bloc config has two fields, and the pairing is worth engraving now because it is the most common first-run stumble. `auth_token` is the ID only, `token_secret` is the UUID only, and the client joins them with `=` on our behalf. Pasting the combined `id=uuid` string into either field earns a 401.

## Storage, honestly assessed

Our nested lab keeps storage simple, and the simplicity is a rule rather than a shortcut. Inside a nested PVE guest we use **LVM-thin only**, because running ZFS or Ceph on top of the outer host's copy-on-write storage compounds write amplification for no benefit. So the pools we point OCFP at are:

| Pool | Content | Role |
|------|---------|------|
| `local-lvm-data` | VM disks and persistent disks | Everything the CPI allocates |
| `local` | ISO images, snippets, and stemcell images | Template and stemcell staging |

Two mechanical checks save us grief later. The stemcell and ISO pool must actually advertise the content types we will push at it (`pmx pve storage node-list --node <node>` reports what the node sees, and `pmx pve storage get local` names the content types the definition allows). Thin provisioning must be real, too, because a pool of thick-provisioned volumes fills by *reservation* long before it fills with data.

While we are here, we throw one more switch, the datacenter firewall. OCFP's bootstrap creates PVE security groups for the VMs it manages, which requires the cluster firewall to be on:

```bash
pmx pve cluster firewall options set --enable 1
```

Enabling it does not filter *VM* traffic by itself (those rules attach per-VM), but it does arm the **host** firewall, whose default input policy drops traffic arriving from the SDN. The gateway address serves DNS, DHCP, and (in our labs) the PVE API to every VM, so the host needs explicit rules for its SDN-facing duties. Write them now, through the same API as everything else:

```bash
pmx pve node firewall options set --node <node> --enable

pmx pve node firewall rules create --node <node> --type in --action ACCEPT \
  --source <supernet> --proto udp --dport 53 --comment "SDN DNS via gateway"
pmx pve node firewall rules create --node <node> --type in --action ACCEPT \
  --source <supernet> --proto tcp --dport 53 --comment "SDN DNS via gateway"
pmx pve node firewall rules create --node <node> --type in --action ACCEPT \
  --proto udp --sport 68 --dport 67 --comment "SDN DHCP, deliberately unscoped"
pmx pve node firewall rules create --node <node> --type in --action ACCEPT \
  --source <supernet> --proto icmp --comment "gateway reachability"
pmx pve node firewall rules create --node <node> --type in --action ACCEPT \
  --source <supernet> --proto tcp --dport 8006 --comment "PVE API for the CPI"
```

These are all ACCEPT rules under a DROP default, so the order pmx leaves them in does not matter. Read them back with `pmx pve node firewall rules list --node <node>`.

The DHCP rule deliberately carries no `--source`, because a client asking for its first lease sends the DISCOVER from `0.0.0.0`, which no supernet-scoped rule will ever match. Scope it and every fresh VM boots addressless while renewals keep working, which is drift that only bites new machines.

The failure mode for skipping this is memorably misleading. DHCP still works (dnsmasq answers broadcasts), external ping and TCP still work (FORWARD is a different chain), but every DNS query to the gateway times out and the CPI can never reach the API through it. It looks exactly like a dnsmasq problem, and it is not.

## Templates, the part we no longer do by hand

Earlier iterations of these runbooks had us downloading cloud images, running a ritual of `qm create` and `qm importdisk`, and hand-authoring cloud-init snippets. That era is over, and it is worth saying why so nobody resurrects it. PVE 9.x rejects snippet uploads through the storage API entirely, so the CLI moved to a fully API-driven flow. When the template a bloc names is missing, OCFP downloads the Ubuntu cloud image *on the PVE node*, builds the VM, and converts it to a template. VMIDs come from the 9000+ range, with no SSH to the host and no snippets anywhere. Per-VM configuration rides in SMBIOS fields the guest reads at first boot.

The catalog names are case-sensitive, and we choose by role. Each image comes in a plain and a bastion flavour, and each flavour comes in Noble and Resolute:

- `ubuntu-noble-template` is vanilla Ubuntu 24.04, and it suits artifacts, jumpbox-style VMs, and anything that needs no first-boot magic.

- `ubuntu-noble-bastion-template` is the same image with the OCFP firstboot and watchdog units pre-baked, and it is required for Tailscale-enabled bastions. Its first build on a cluster takes a few extra minutes while OCFP drives the serial console to prepare it, and every later bastion clones in about thirty seconds. That seed boot uses DHCP by default, or a static network identity when the bloc config sets the `template_seed_*` keys, which we need when the template bridge itself has no DHCP (chapter 3).

- `ubuntu-resolute-template` is vanilla Ubuntu 26.04. This is the one the artifacts config reaches for when the bloc says nothing, so a bloc that leaves `artifacts.template` unset gets Resolute rather than Noble.

- `ubuntu-resolute-bastion-template` is Ubuntu 26.04 with the same firstboot and watchdog units. Our own lab bastion runs this one, having been cycled off Noble.

Nothing forces a bloc to pick one release for everything, and a mixed bloc is normal while we move a fleet across. What matters is that the name in the bloc config matches a catalog entry exactly, because a name the catalog does not know fails at image resolution rather than falling back to something close.

We do nothing now except make sure our bloc config (next chapter) names a catalog entry. The provisioning happens lazily, on first use, and it is idempotent.

## What we carry forward

Three facts leave this chapter with us, destined for the bloc config: the API endpoint `https://<node>:8006`, the token ID, and the token secret. They now live in two places, the bloc config that OCFP reads and the pmx context we rewired above, which is the same credential wearing two coats. The secret is a password, so it travels through a password manager or Vault, never through a repo.

**Verify**, once more from the workstation, because this is the last moment a failure is purely about credentials:

```bash
pmx pve node list --context <context>
```

A node list means the host is ready for us. Now we write the bloc in [3. Bloc definition](03-bloc-definition.md).
