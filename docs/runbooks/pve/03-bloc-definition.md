# 3. Bloc Definition — Writing It All Down

We now hold every fact the automation needs: an address plan from chapter 1
and an API credential from chapter 2. This chapter turns those facts into a
bloc (one entry in `~/.config/ocfp/config.yml`), and that single document drives
everything the `ocfp` CLI does from here on. Nothing gets created yet; we are
writing the score before the orchestra plays.

## The shape of the file

The config has three tiers. Top-level knobs (`debug`, `verbose`) come first.
Then provider-wide defaults — a `pve:` section for shared credentials, a
`tailscale:` section for how bastions join our tailnet, a `cloudflare:`
section for public ingress. Finally `blocs:`, a map of named blocs, each of
which may override any of the defaults. In a lab that runs several blocs
against one cluster, we keep the shared facts in a YAML anchor and let each
bloc state only what makes it distinct.

Bloc names must match `^ocfp-[a-z0-9-]+$`. Ours follows the lab convention
`ocfp-lab-<user>`, so `ocfp-lab-wayne`. The name matters more than it looks:
every VM, security group, and Vault path we create will be prefixed with it.

## The worked example, annotated

Here is our bloc, section by section, with the reasoning attached to each
choice. The full authoritative field reference lives in
[`docs/config.pve.example.yml`](../../config.pve.example.yml); what follows
is the subset a fresh bring-up actually needs.

### Provider and identity

```yaml
blocs:
  ocfp-lab-wayne:
    provider: pve

    api_endpoint: "https://lab-wayne:8006"
    region: lab-wayne
    nodes: [lab-wayne]

    auth_token:   "ocfp-cpi@pve!cpi"
    token_secret: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
    verify_ssl: false
```

`region` names the PVE node used for placement, and `nodes` lists every
cluster node — one and the same in a single-node lab. But each entry in
`nodes` becomes an availability-zone record in Vault later, so a three-node
cluster lists all three. The token pair is the two-halves credential from
chapter 2: ID in `auth_token`, secret UUID in `token_secret`, never the
joined string. And `verify_ssl: false` is the honest setting for a PVE host
with its self-signed certificate; we set it `true` the day the host carries a
CA-signed cert.

One more note on the secret: this file now contains a live credential, so it
stays out of every repository, with the file mode tightened to `600`. When we
want the secret out of the file entirely, Vault-path indirection is available
for the Tailscale and Cloudflare tokens below — worth adopting once the real
Vault exists in chapter 7.

### Storage and templates

```yaml
    vm_storage:   local-lvm-data
    disk_storage: local-lvm-data
    iso_storage:  local
    template_bridge: ocfp
    # template_seed_ip: 10.108.16.2/20
    # template_seed_gateway: 10.108.16.1
    # template_seed_dns:
    #   - 10.97.160.160
    #   - 10.97.160.161
    # template_seed_searchdomain: ldschurch.org
```

The first four lines aim the CPI at the pools we validated in chapter 2: VM
disks and persistent disks on the LVM-thin pool, images on `local`. The
`iso_storage` pool must advertise the right content types
(`pmx pve storage set local --content vztmpl,iso,import,backup,snippets` —
natively `pvesm set local --content vztmpl,iso,import,backup,snippets`).
The failure mode for forgetting is nastily quiet: OCFP falls back to
PVE's default cloud-init, and the bastion's Tailscale setup is silently
skipped.
`template_bridge` tells template auto-provisioning where seed VMs get DHCP
and internet — our SDN's infra subnet provides both, so the `ocfp` vnet
serves double duty. See below for what to set instead when the template
bridge has no DHCP.

The remaining lines, commented out above because our own `ocfp` vnet already has DHCP, show the shape to use when the template bridge does not. Uncomment `template_seed_ip` and set the four `template_seed_*` keys so the seed VM gets a static network identity instead of DHCP. These four keys are all-or-nothing: uncommenting `template_seed_gateway`, `template_seed_dns`, or `template_seed_searchdomain` without also setting `template_seed_ip` is a hard config load error.

The address must be a dedicated, otherwise unused, host address in the bridge's subnet, recorded in the bloc's reserved-IP plan. The example above borrows the offset-2 convention from our own reserved-IP table (`.16.1` gateway, `.16.3` bastion), so substitute a spare address from your own template bridge's subnet, not this one, since the template bridge is commonly a different network than the bloc's own bridge. The gateway must be on-link within the given prefix.

Resolvers try `template_seed_dns` first, then fall back to `network.dns_servers`, then, if neither is set, to a hardcoded public default (`1.1.1.1 8.8.8.8`), worth setting explicitly on an air-gapped or egress-filtered lab, where that public default cannot resolve anything.

### Network

```yaml
    default_bridge: ocfp
    network:
      name: ocfp
      network_cidr: 10.108.16.0/20
      available_ip_start: 10.108.16.20
      available_ip_end:   10.108.16.50
```

This is chapter 1 transcribed. The CIDR is the whole supernet (bootstrap
carves it into the four `/22`s for us). The available band is the slice of
*infra* that BOSH may allocate dynamically, deliberately clear of our
reserved statics (`.16.1` gateway, `.16.3` bastion, `.16.4` mgmt director,
`.16.11` artifacts). The workload subnets get their own reserved statics and
bands written to Vault during bootstrap, derived from the same plan.

### Bastion and Genesis

```yaml
    bastion:
      image: ubuntu-noble-bastion-template
      ssh_user: ubuntu
      genesis:
        enabled: true
        branch: v3.2.x-dev
        versionPrefix: "3.2.0"
      keys:
        wayne: "github.com/wayneeseguin"

    deployments:
      url: git@github.com:fivetwenty-io/fivetwenty-ocfp-deployments

    bastion_ip: ocfp-lab-wayne-bastion
```

The bastion image names the pre-baked catalog template (required for
Tailscale-enabled bastions, as chapter 2 explained). The `genesis` block pins
the branch our kits track; the lab currently rides `v3.2.x-dev`. SSH keys
take the `github.com/<username>` form and cloud-init fetches them at boot —
one line per teammate who should reach the box. `deployments.url` is the git
home of our environment files; chapter 5 clones it onto the bastion.

`bastion_ip` earns its comment in our real config: the bastion's SDN address
(`10.108.16.3`) is unreachable from our workstation, so operator-side
commands reach it over Tailscale instead. We use the MagicDNS name (stable
as `<bloc>-bastion`) rather than a raw `100.x` address, which churns on
every bastion rebuild and silently breaks operator hops.

### Artifacts

```yaml
    artifacts:
      enabled: true
      template: ubuntu-noble-template
      data:
        storage_pool: local-lvm-data
      tls:
        mode: internal-ca
```

This opts us into the RustFS artifacts VM — the S3-compatible blobstore that
BOSH and Cloud Foundry will lean on, since PVE ships no native object store.
`internal-ca` is the default TLS mode for good reason: the certificate is
issued from the bloc's own CA in Vault, and the bastion trust store, `aws`
CLI, and kits all pick it up with zero extra configuration.

### Reaching in: Tailscale and Cloudflare

```yaml
tailscale:
  auth_key_vault_path: "secret/ocfp/wayne/tailscale:auth_key"
  tags: ["tag:ocfp", "tag:bastion"]
  accept_dns: true
  accept_routes: true
  ssh: true
```

The global `tailscale:` section (per-bloc override available) is how the
bastion joins our tailnet at first boot. The auth key can be a literal
`auth_key` or a Vault path (the two are mutually exclusive), and the tags
must exist in our Tailscale ACL before bootstrap runs.

```yaml
    cloudflare:
      enabled: true
      api_token_vault_path: secret/ocfp-lab-wayne/cloudflare:api_token
      zone: fivetwenty.io
      tunnel_name: ocfp-lab-wayne
      origin: https://10.108.20.13
      apps_domain: apps.ocf.wayne.lab.fivetwenty.io
      system_domain: system.ocf.wayne.lab.fivetwenty.io
      ssh_hostname: ssh.system.ocf.wayne.lab.fivetwenty.io
      ssh_origin: ssh://10.108.20.13:2222
      origin_no_tls_verify: true
```

Cloudflare is our public front door — an outbound tunnel from the bastion to
HAProxy's static at `10.108.20.13`, exactly as chapter 1 promised, with no
inbound firewall holes. Two footguns hide here. First, always set
`tunnel_name` explicitly: the CLI's default prepends `ocfp-lab-` to the bloc
name, which doubles the prefix for blocs already named that way. Second,
`origin_no_tls_verify: true` is required while HAProxy runs the
`self-signed` feature — without it, `cloudflared` refuses the origin
certificate and every request 502s. Chapter 12 finishes this story; writing
the block now costs nothing and bootstrap will use it.

### Domains

```yaml
    fqdns:
      base: ocf.wayne.lab.fivetwenty.io
      ocf:
        apps:    apps.ocf.wayne.lab.fivetwenty.io
        system:  system.ocf.wayne.lab.fivetwenty.io
        stratos: console.apps.ocf.wayne.lab.fivetwenty.io
```

One base domain per bloc, with the CF system and apps domains beneath it and
the Stratos console named up front — chapter 12 will be glad we did. These
land in Vault at bootstrap and every kit reads them from there, so this is
the only place we ever type them.

## Proving the document before it acts

A YAML file this consequential deserves a dry check. The CLI validates the
bloc on every invocation, so the cheapest proof is a read-only command:

```bash
ocfp --version
ocfp bootstrap --bloc ocfp-lab-wayne --dry-run
```

**Verify**: the dry run parses the bloc, reaches the API with our token,
and prints the full plan — network figures, subnets, security groups,
bastion — without creating anything. Every complaint it prints now is a
bootstrap failure we just avoided. (`ocfp pve probe <bloc>` exists too, but
it is a *pre-deploy* health probe that expects a bastion and a director;
it earns its keep from chapter 6 onward, not here.)

**Rollback**: it is a text file. Edit it.

With the bloc written and the probe green, we have finished planning.
Everything from here on creates real things:
[4. Bootstrap](04-bootstrap.md).
