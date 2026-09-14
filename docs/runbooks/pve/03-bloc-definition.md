# 3. Bloc Definition: Writing It All Down

We now hold every fact the automation needs: an address plan from chapter 1 and an API credential from chapter 2. This chapter turns those facts into a bloc (one entry in `~/.config/ocfp/config.yml`), and that single document drives everything the `ocfp` CLI does from here on. Nothing gets created yet; we are writing the score before the orchestra plays.

## The shape of the file

The config has three tiers. Top-level knobs (`debug`, `verbose`) come first. Then come the provider-wide defaults, a `pve:` section for shared credentials, a `tailscale:` section for how bastions join our tailnet, and a `cloudflare:` section for public ingress. Finally comes `blocs:`, a map of named blocs, each of which may override any of the defaults. In a lab that runs several blocs against one cluster, we keep the shared facts in a YAML anchor and let each bloc state only what makes it distinct.

Bloc names must match `^ocfp-[a-z0-9-]+$`. Ours follows the lab convention `ocfp-lab-<user>`, so `ocfp-lab-wayne`. The name matters more than it looks, because every VM, security group, and Vault path we create will be prefixed with it.

## The worked example, annotated

Here is our bloc, section by section, with the reasoning attached to each choice. The full authoritative field reference lives in [`docs/config.pve.example.yml`](../../config.pve.example.yml); what follows is the subset a fresh bring-up actually needs.

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

`region` names the PVE node used for placement, and `nodes` lists every cluster node. In a single-node lab those are one and the same. But each entry in `nodes` becomes an availability-zone record in Vault later, so a three-node cluster lists all three. The token pair is the two-halves credential from chapter 2, so the ID goes in `auth_token` and the secret UUID goes in `token_secret`, never the joined string. And `verify_ssl: false` is the honest setting for a PVE host with its self-signed certificate; we set it `true` the day the host carries a CA-signed cert.

This file now contains a live credential, so it stays out of every repository, and we tighten the file mode to `600`. When we want the secret out of the file entirely, Vault-path indirection is available for the Tailscale and Cloudflare tokens below, and it is worth adopting once the real Vault exists in chapter 7.

### Storage and templates

```yaml
    vm_storage:       local-lvm-data
    stemcell_storage: local-lvm-data
    disk_storage:     local-lvm-data
    # disk_storage_type: lvmthin
    iso_storage:      local
    template_bridge: ocfp
    # template_seed_ip: 10.108.16.2/20
    # template_seed_gateway: 10.108.16.1
    # template_seed_dns:
    #   - 10.97.160.160
    #   - 10.97.160.161
    # template_seed_searchdomain: ldschurch.org
```

The first five lines aim the CPI at the pools we validated in chapter 2. VM disks, stemcell templates, and persistent disks go on the LVM-thin pool, and images go on `local`. The `stemcell_storage` pool should match `vm_storage`, because the CPI creates each root disk as a linked clone of the stemcell template, and PVE only allows that when both live on the same pool. When we leave `stemcell_storage` out, `ocfp` derives it from `vm_storage` for us. The commented `disk_storage_type` line names the PVE storage type behind `disk_storage`, and we only need it when the pool name says nothing about its type, for example an NFS share named after the cluster. Without it, `ocfp` guesses the storage backend and disk format from the pool name alone. The `iso_storage` pool must advertise the right content types (`pmx pve storage set local --content vztmpl,iso,import,backup,snippets`). The failure mode for forgetting is nastily quiet, because OCFP falls back to PVE's default cloud-init and silently skips the bastion's Tailscale setup. `template_bridge` tells template auto-provisioning where seed VMs get DHCP and internet. Our SDN's infra subnet provides both, so the `ocfp` vnet serves double duty. See below for what to set instead when the template bridge has no DHCP.

The remaining lines are commented out because our own `ocfp` vnet already has DHCP. They show the shape to use when the template bridge does not. Uncomment `template_seed_ip` and the seed VM gets a static network identity instead.

Two of the four keys are coupled and two are not. `template_seed_ip` and `template_seed_gateway` come as a pair, so setting either one without the other is a hard config load error, and so is setting `template_seed_dns` or `template_seed_searchdomain` with no `template_seed_ip` underneath them. Those last two are optional once the pair is in place.

The address must be a dedicated, otherwise unused host address in the bridge's subnet, recorded in the bloc's reserved-IP plan. The example above borrows the offset-2 convention from our own reserved-IP table, where `.16.1` is the gateway and `.16.3` the bastion. Substitute a spare address from your own template bridge's subnet rather than this one, because the template bridge is commonly a different network from the bloc's own bridge. The gateway has to be on-link within the given prefix.

Resolvers try `template_seed_dns` first, then fall back to `network.dns_servers`, and then, if neither is set, to a hardcoded public default of `1.1.1.1` and `8.8.8.8`. That default is worth overriding explicitly on an air-gapped or egress-filtered lab, where it cannot resolve anything.

### Network

```yaml
    default_bridge: ocfp
    network:
      name: ocfp
      network_cidr: 10.108.16.0/20
      available_ip_start: 10.108.16.20
      available_ip_end:   10.108.16.50
```

This is chapter 1 transcribed. The CIDR is the whole supernet, and bootstrap carves it into the four `/22`s for us.

The two `available_ip_*` keys need a word of warning, because they do less than their name suggests. They are a rescue hatch rather than the normal path. When bootstrap has left state behind, which is every run after the first, the reserved statics and the available bands both come from the reserved-IP strategy described in chapter 1, and these two keys are ignored entirely. They take effect only when `ocfp vault populate` has to write a topology with no bootstrap state to read, and even then they apply to every tier alike rather than to `infra` alone. We set them here so that the degraded path lands somewhere sane, and we read the real bands back out of Vault rather than out of this file.

### Bastion and Genesis

```yaml
    bastion:
      image: ubuntu-resolute-bastion-template
      ssh_user: ubuntu
      genesis:
        enabled: true
        branch: v3.2.x-dev
        versionPrefix: "3.2.0"
      keys:
        wayne: "github/wayneeseguin"

    deployments:
      url: git@github.com:fivetwenty-io/fivetwenty-ocfp-deployments

    bastion_ip: ocfp-lab-wayne-bastion

    allowed_ingress_ips:
      - 100.64.0.0/10
```

The bastion image names the pre-baked catalog template, which Tailscale-enabled bastions require, as chapter 2 explained. We name the Resolute flavour because that is where the fleet is going and where our own lab bastion already sits. `ubuntu-noble-bastion-template` is still in the catalog and still works when a bloc needs to stay on 24.04. The `genesis` block pins the branch our kits track, and the lab currently rides `v3.2.x-dev`. `deployments.url` is the git home of our environment files, and chapter 5 clones it onto the bastion.

SSH keys take the form `github/<username>` or `gitlab/<username>`, one line per teammate who should reach the box, and a literal public key works too. The prefix is the whole of the syntax, so `github.com/wayneeseguin` is not a longer spelling of the same thing. A spec that matches neither prefix and is not itself a key gets dropped with a warning in the log and nothing else, so the teammate never gets access and nobody finds out until they try. These keys are not fetched by cloud-init at boot, either. `ocfp init bastion` resolves them in chapter 5 and appends them to `authorized_keys` over SSH.

`allowed_ingress_ips` is the one key here we cannot afford to leave out. Bootstrap builds the bastion's security group from it, and when the list is empty it falls back to opening SSH to `0.0.0.0/0` with a single warning line for company. We reach this lab over Tailscale, so the tailnet range `100.64.0.0/10` is the honest entry. A bloc reached from an office would list that office's egress addresses instead.

`bastion_ip` earns its comment in our real config. The bastion's SDN address (`10.108.16.3`) is unreachable from our workstation, so operator-side commands reach it over Tailscale instead. We use the MagicDNS name (stable as `<bloc>-bastion`) rather than a raw `100.x` address, which churns on every bastion rebuild and silently breaks operator hops.

### Artifacts

```yaml
    artifacts:
      enabled: true
      template: ubuntu-resolute-template
      data:
        storage_pool: local-lvm-data
        disk_size_gib: 200
      tls:
        mode: internal-ca
```

This opts us into the RustFS artifacts VM, the S3-compatible blobstore that BOSH and Cloud Foundry will lean on, since PVE ships no native object store. `internal-ca` is the default TLS mode for good reason, because the certificate is issued from the bloc's own CA in Vault, and the bastion trust store, the `aws` CLI, and the kits all pick it up with no extra configuration.

Two of these keys carry defaults worth knowing before we accept them. `template` defaults to `ubuntu-resolute-template`, which is what we name here, and we write it out rather than leaning on the default so the file says what it means. Naming Noble instead is a deliberate choice rather than a safer one. `disk_size_gib` defaults to 500, which lands as a half-terabyte volume on `local-lvm-data` the first time bootstrap runs. On a lab that is usually more than we want, and the size is fixed at creation, so it is cheaper to decide now than to resize later.

### Reaching in: Tailscale and Cloudflare

```yaml
tailscale:
  auth_key_vault_path: "secret/ocfp/wayne/tailscale:auth_key"
  tags: ["tag:ocfp", "tag:bastion"]
  accept_dns: true
  accept_routes: true
  ssh: true
```

The global `tailscale:` section, which a bloc can override, is how the bastion joins our tailnet at first boot. The auth key can be a literal `auth_key` or a Vault path (the two are mutually exclusive), and the tags must exist in our Tailscale ACL before bootstrap runs.

```yaml
    cloudflare:
      enabled: true
      api_token_vault_path: secret/ocfp-lab-wayne/cloudflare:api_token
      zone: fivetwenty.io
      tunnel_name: ocfp-lab-wayne
      origin: https://10.108.20.97
      apps_domain: apps.ocf.wayne.lab.fivetwenty.io
      system_domain: system.ocf.wayne.lab.fivetwenty.io
      ssh_hostname: ssh.system.ocf.wayne.lab.fivetwenty.io
      ssh_origin: ssh://10.108.20.97:2222
      origin_no_tls_verify: true
      services:
        - hostname: shield.system.ocf.wayne.lab.fivetwenty.io
          service: https://10.108.20.9
          no_tls_verify: true
        - hostname: concourse.system.ocf.wayne.lab.fivetwenty.io
          service: https://10.108.20.7
          no_tls_verify: true
        - hostname: grafana.system.ocf.wayne.lab.fivetwenty.io
          service: http://10.108.20.8:3000
        - hostname: blacksmith.system.ocf.wayne.lab.fivetwenty.io
          service: https://10.108.24.67
          no_tls_verify: true
        - hostname: doomsday.system.ocf.wayne.lab.fivetwenty.io
          service: https://10.108.24.18
          no_tls_verify: true
```

Cloudflare is our public front door. It runs an outbound tunnel from the bastion to HAProxy's static at `10.108.20.97`, exactly as chapter 1 promised, with no inbound firewall holes. Three footguns hide here.

First, always set `tunnel_name` explicitly. The CLI's default prepends `ocfp-lab-` to the bloc name, which doubles the prefix for blocs already named that way.

Second, `origin_no_tls_verify: true` is required while HAProxy runs the `self-signed` feature. Without it, `cloudflared` refuses the origin certificate and every request 502s.

Third, the `services` list is not optional decoration. Each entry becomes an explicit ingress rule ahead of the `*.apps` and `*.system` wildcards, plus its own proxied CNAME, and `cloudflared` takes the first rule that matches. Anything we leave out here falls through to the wildcard and lands on HAProxy, which knows nothing about it. These are the infra-service UIs that the gorouter never sees, so they have to be named one at a time. The addresses come straight from chapter 1's reserved-IP tables, which is why SHIELD and Grafana sit in the management scope's band while blacksmith and doomsday sit where their own deployments placed them.

Chapter 12 finishes this story; writing the block now costs nothing and bootstrap will use it.

### Domains

```yaml
    fqdns:
      base: ocf.wayne.lab.fivetwenty.io
      ocf:
        apps:    apps.ocf.wayne.lab.fivetwenty.io
        system:  system.ocf.wayne.lab.fivetwenty.io
        stratos: console.apps.ocf.wayne.lab.fivetwenty.io
```

We give each bloc one base domain, with the CF system and apps domains beneath it. We name the Stratos console up front as well, and chapter 12 will be glad we did. These land in Vault at bootstrap and every kit reads them from there, so this is the only place we ever type them.

## Proving the document before it acts

A YAML file this consequential deserves a dry check. The CLI validates the bloc on every invocation, so the cheapest proof is a read-only command:

```bash
ocfp --version
ocfp bootstrap --bloc ocfp-lab-wayne --dry-run
```

**Verify**: the dry run parses the bloc, reaches the API with our token, and prints the full plan (network figures, subnets, security groups, and the bastion) without creating anything. Every complaint it prints now is a bootstrap failure we just avoided. (`ocfp pve probe <bloc>` exists too, but it is a *pre-deploy* health probe that expects a bastion and a director; it earns its keep from chapter 6 onward, not here.)

**Rollback**: it is a text file. Edit it.

With the bloc written and the probe green, we have finished planning. Everything from here on creates real things: [4. Bootstrap](04-bootstrap.md).
