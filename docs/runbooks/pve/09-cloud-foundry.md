# 9. Cloud Foundry: The Platform Itself

Eight chapters of scaffolding come down to this one deployment, the one the bloc exists to run. Cloud Foundry arrives through the cf Genesis kit like everything else, with `g @env:cf deploy`, but it is the largest deployment in the arc by an order of magnitude. Its env file has more to say than any other. We walk the file first, deploy second, and by the end the API answers over HAProxy.

## Reading the env file like a story

`cf/ocfp-lab-wayne-ocf.yml` compresses most of what we have built into a page. The features list is the plot summary:

```yaml
kit:
  name:    dev
  iaas:    pve
  scale:   dev
  features:
  - (( append ))
  - ocfp
  - haproxy
  - self-signed
  - pve-blobstore
  - cf-deployment-version-56.5.0
  - source-releases
```

Each line is a decision we can now read fluently. `ocfp` wires the kit into the bloc's Vault topology. `haproxy` deploys our front door at the reserved static `10.108.20.97`, and that address flows from the Vault topology (`net/subnets/ocfp-0/reserved-ips:haproxy_ip`) rather than from a hand-keyed literal that could drift.

`self-signed` mints HAProxy's TLS in-deployment, which gives us honest lab TLS until real certificates arrive, and it is the reason chapter 3 set `origin_no_tls_verify` for the tunnel. `pve-blobstore` points CF's packages and droplets at our RustFS store instead of internal WebDAV VMs.

The last two pick the cf-deployment lineage, and this is the pair to check before copying. The `cf-deployment-version-<ver>` feature tells the kit to fetch that version of upstream cf-deployment at render time and swap its tree in, in place of the one the kit vendors. When the runbook's validated run happened, the kit vendored v52.0.0, so naming v56.5.0 moved us forward onto a noble-default lineage, which expects the same Ubuntu 24.04 stemcell family PVE requires and dissolved a layer of stemcell-forcing workarounds.

The kit has since vendored v60.5.0, which is noble-default too. Against that kit the same feature line is a downgrade rather than an upgrade, and the noble argument no longer tells the two apart. So read the kit's `cf-deployment/cf-deployment.yml` for its `manifest_version` before deciding. Pin a version when we have a reason to sit on a specific lineage, and drop the feature entirely to take what the kit ships.

`source-releases` then chooses how we compile. Releases compile from source on the ocf director, with the blobs cached in RustFS. (The compiled-blob route, `vendored-compiled-releases`, is also validated and much faster on first deploy, though source is the lab's current choice while the long-term compiled strategy settles.)

Below the features, three stanzas carry lessons we have already earned, plus one new idea:

```yaml
genesis:
  bosh_env: ocfp-lab-wayne-ocf@/secret/exodus/
  entomb: true
```

The relative exodus reference is chapter 7's rule applied to the ocf director this time. And `entomb: true` is the new idea. Genesis copies the deployment's generated secrets into the ocf director's CredHub, so the manifest carries CredHub references instead of a couple hundred inline certificates. Our run entombed 204 of them.

```yaml
bosh-configs:
  cpi:
    pve_diego_cell_ram: 16384
  cloud:
    networks:
      ocf:
        allocation:
          size: 18
```

`bosh-configs` is the tuning surface chapter 8 promised. The allocation does not take the whole available band. It carves an eighteen-address window off the bottom of it, which for us means `10.108.20.96` through `10.108.20.113`. CF declares seventeen instance groups, but two of them are errands, so fifteen addresses go to standing VMs with HAProxy sitting on its own static at the front. Everything above that window stays reserved in the generated cloud config, which is where the compilation headroom lives. The Diego cell gets 16 GB. That brings us to the one genuinely PVE-shaped sizing rule in the stack. **PVE VMs get no separate ephemeral disk**, so the BOSH agent carves the root disk into root, swap (scaling with RAM), and `/var/vcap/data`.

On a Diego cell, `/var/vcap/data` is where apps stage and run. Size the disk too close to RAM-plus-5G and staging fails with `InsufficientResources` while memory looks fine. The kit's PVE vm-types now respect the carve rule, and `bosh-configs.cpi.pve_diego_cell_disk` is the override if a cell ever needs more room. The CPI's other settings, bridge and disk storage, resolve from the Vault config (`cpi/pve:*`, written by `ocfp vault populate`), with `bosh-configs.cpi.pve_*` as the per-env override.

Finally, the lab's FQDNs are set as literals under `meta.ocfp.cf.fqdns` (the defaults resolve from Terraform-written Vault paths this lab does not have). `skip_ssl_validation: true` keeps self-signed TLS honest between components.

## Secrets, render, deploy

Three commands, in a deliberate order. First come the secrets, because the kit knows every certificate, password, and key CF needs, and it generates them into Vault:

```bash
g @ocfp-lab-wayne-ocf:cf add-secrets
```

Our run generated 132 definitions: one RSA, 42 randoms, one SSH, and 88 x509. Then comes the render, which for this deployment is genuinely worth reading:

```bash
g @ocfp-lab-wayne-ocf:cf manifest
```

**Verify** (in the render, before anything deploys): the v56.5.0 lineage, `ubuntu-noble` as the stemcell throughout, the blobstore stanzas pointing at our RustFS endpoint, and an `ssh_proxy` job on the `scheduler` instance group, which chapter 10 depends on. Look for the job rather than a group of that name, because CF has not shipped a standalone `ssh_proxy` group for years. This read costs five minutes and has caught real kit bugs.

Then comes the commitment:

```bash
g @ocfp-lab-wayne-ocf:cf deploy -F -y
```

This is the long one. Under `source-releases`, compilation alone can run well past an hour on first deploy, which is what the tmux session from chapter 5 was for. The first deploy also uploads the generated cloud config (`ocfp-lab-wayne-ocf.cf`) chapter 8 described. Then the VMs come. Our validated run converged at fifteen persistent VMs, routers and cells and brains filling the window around HAProxy's static, with `smoke-tests` and `rotate-cc-database-key` appearing as errands rather than as standing machines.

Fifteen out of eighteen leaves three addresses, which is the real compilation headroom and less than the four concurrent compilation VMs a `source-releases` deploy would like. It does converge, because BOSH waits rather than failing, but that is the number to raise first if a first deploy stalls with nothing obviously wrong.

**Verify**:

```bash
g @ocfp-lab-wayne-ocf:cf b instances --ps   # every instance and process 'running'
```

Note the missing `--self` here. A deployment environment such as `cf` has no director of its own, so a bare `b` already talks to the director that deployed it, and Genesis rejects `--self` outright on anything that is not a `bosh` environment.

Then the API, front to back. One piece of lab honesty comes first. Nothing inside the bloc serves wildcard DNS for our system domain yet, and public resolution arrives with the tunnel in chapter 12. So, on the bastion, we point the system hostnames at HAProxy in `/etc/hosts`:

```
10.108.20.97  api.system.ocf.wayne.lab.fivetwenty.io uaa.system.ocf.wayne.lab.fivetwenty.io login.system.ocf.wayne.lab.fivetwenty.io doppler.system.ocf.wayne.lab.fivetwenty.io log-cache.system.ocf.wayne.lab.fivetwenty.io
```

And then the kit logs us in as admin, with credentials taken straight from Vault:

```bash
g @ocfp-lab-wayne-ocf:cf do login
cf api          # https://api.system.<base>, v3 API version reported
cf orgs         # answers — the cloud controller is alive end to end
```

**Rollback**: a failed CF deploy is a BOSH deployment like any other. We reach for `g @ocfp-lab-wayne-ocf:cf b task <id> --debug` on deploy-time errors, and for `~/.genesis/mylogs/last-trace` on render-time hook failures. Fix the cause and re-deploy, because BOSH converges the delta rather than starting over. The two classic first-run failures both live in the CPI settings. A wrong bridge name fails every VM at `qmstart`, and an undersized Diego cell surfaces later as the staging failure described above.

The platform is up. Before we celebrate, we make it prove itself in [10. Validation](10-validation.md).
