# 10. Validation: Proof of Life

Fifteen green instances is a claim; an app taking traffic is proof. Cloud Foundry has exactly one purpose, which is `cf push`, and until that verb works end to end, we do not call the platform up. This chapter is short and deliberately unforgiving: push an app, hit its route, and shell into its container. Each test exercises a different slice of everything we built, and together they leave nowhere for a broken subsystem to hide.

## A place to stand

Orgs and spaces come first, and they take thirty seconds that double as a validation of the cloud controller and UAA, since every one of these calls round-trips through both:

```bash
cf create-org e2e && cf target -o e2e
cf create-space test && cf target -s test
```

## The push

A minimal app is the right instrument, because we are testing the platform and not the app:

```bash
mkdir e2e-test && cd e2e-test
echo 'ocfp e2e ok' > index.html
echo '{}' > Staticfile
cf push e2e-test --random-route -m 64M
```

This one command commits a lot of machinery. The CLI uploads the bits through the API, the blobstore write lands in RustFS, and a Diego cell stages the app with the staticfile buildpack (exercising the `/var/vcap/data` carve from chapter 9). The droplet goes back to RustFS, the cell runs it, and the router learns the route. When the output reads `running` and `1/1`, all of that just worked in sequence.

**Verify**, from the outside. Public DNS is still two chapters away, so we tell curl where the route lives instead of asking a resolver. The `--resolve` flag does that on its own and bypasses `/etc/hosts` entirely, which is why we do not need a second apps-domain line on the bastion:

```bash
cf app e2e-test        # note the random route
curl -k --resolve <route>:443:10.108.20.97 https://<route>
```

We get HTTP 200 back, and the body reads `ocfp e2e ok`. That response traversed HAProxy, the gorouter, and the cell, which is the entire data path a real user will take.

**Debug note**: staging failures point at the blobstore first, either at RustFS reads and writes or at `cf buildpacks` coming up empty, while `InsufficientResources` at staging is the Diego cell disk-carve issue from chapter 9. Scheduling failures trace back to the generated cloud config via `g @ocfp-lab-wayne-ocf:cf b task <id> --debug`.

## The shell

`cf ssh` is the operator's debug path into a running container. It validates a subsystem nothing else touches. That subsystem is the `ssh_proxy` job on the `scheduler` group that we confirmed in chapter 9's manifest, and we reach it through HAProxy's 2222. The CF CLI resolves the SSH endpoint through the system domain, and that one does need a bastion `/etc/hosts` line, so we add `10.108.20.97  ssh.system.ocf.wayne.lab.fivetwenty.io` alongside the others from chapter 9. Then we run the checks:

```bash
cf ssh-enabled e2e-test          # reports only; it does not change anything
cf enable-ssh e2e-test           # if the line above says disabled
cf restart e2e-test              # the app must restart to pick it up
cf ssh e2e-test -c "echo ok && hostname"
```

`cf ssh-enabled` is a query, despite reading like a switch. Enabling SSH for an app takes `cf enable-ssh`, and the app only honours it after a restart.

**Verify**: `ok` and a container hostname. An interactive `cf ssh e2e-test` should also drop us at a prompt inside the container, and `ps aux` there is a strange and pleasant view after ten chapters of building toward it.

**Debug note**: `cf ssh` failures are almost always the proxy path, so confirm that the `scheduler` group is running its `ssh_proxy` process and that port 2222 answers on HAProxy. On PVE specifically, confirm the kit's `ocfp/pve/ssh-proxy.yml` overlay was retained. Despite the name, it is not an ssh enabler but a link-provider fix for PVE's flat network, and dropping it breaks the deploy itself.

## Sign-off

Push, route, and shell give us three greens, and the bloc's primary arc is complete, running from the network to the bastion, on to the directors and Vault, and through the platform to a running app. We clean up our test artifacts (`cf delete e2e-test -f`, and the org too if we like), or keep them as a standing smoke test. `ocfp test smoke` run from the bastion is the automated version of this chapter. It pushes a static app into a throwaway org, confirms the HTTPS route answers 200, reads the app's recent logs, and deletes everything again. `ocfp test acceptance --bosh-env <alias>` runs the CF smoke-tests errand through the director instead, and `ocfp test c2c` covers container networking once we want more than the arc. Each check reports passed, failed, or skipped with a reason, so a skip never reads as a green. See [docs/commands/test.md](../../commands/test.md) for the suites.

What remains is turning a working platform into an operable one, which we take up in [11. Platform services](11-platform-services.md).
