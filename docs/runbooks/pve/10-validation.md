# 10. Validation — Proof of Life

Seventeen green instances is a claim; an app taking traffic is proof. Cloud
Foundry has exactly one purpose — `cf push` — and until that verb works end
to end, we do not call the platform up. This chapter is short and
deliberately unforgiving: push an app, hit its route, shell into its
container. Each test exercises a different slice of everything we built,
and together they leave nowhere for a broken subsystem to hide.

## A place to stand

Orgs and spaces first — thirty seconds that double as a validation of the
cloud controller and UAA, since every one of these calls round-trips through
both:

```bash
cf create-org e2e && cf target -o e2e
cf create-space test && cf target -s test
```

## The push

A minimal app is the right instrument — we are testing the platform, not the
app:

```bash
mkdir e2e-test && cd e2e-test
echo 'ocfp e2e ok' > index.html
echo '{}' > Staticfile
cf push e2e-test --random-route -m 64M
```

This one command commits a lot of machinery: the CLI uploads the bits
through the API, the blobstore write lands in RustFS, and a Diego cell
stages the app with the staticfile buildpack (exercising the
`/var/vcap/data` carve from chapter 9). The droplet goes back to RustFS,
the cell runs it, and the router learns the route. When the output reads
`running` and `1/1`, all of that just worked in sequence.

**Verify**, from the outside — first with the bastion's `/etc/hosts` doing
apps-domain duty via `--resolve`, since public DNS is still two chapters
away:

```bash
cf app e2e-test        # note the random route
curl -k --resolve <route>:443:10.108.20.13 https://<route>
```

HTTP 200 and `ocfp e2e ok`. That response traversed HAProxy, the gorouter,
and the cell — the entire data path a real user will take.

**Debug note**: staging failures point at the blobstore first — RustFS
reads/writes, or `cf buildpacks` coming up empty — while
`InsufficientResources` at staging is the Diego cell disk-carve issue from
chapter 9. Scheduling failures trace back to the generated cloud config via
`bosh -e ocf task <id> --debug`.

## The shell

`cf ssh` is the operator's debug path into a running container. It
validates a subsystem nothing else touches: the `ssh_proxy` instance group
we confirmed in chapter 9's manifest, reached through HAProxy's 2222. One
more `/etc/hosts` line on the bastion covers the SSH endpoint —
`10.108.20.13  ssh.system.ocf.wayne.lab.fivetwenty.io` — and then:

```bash
cf ssh-enabled e2e-test          # enable + restart if it reports disabled
cf ssh e2e-test -c "echo ok && hostname"
```

**Verify**: `ok` and a container hostname. An interactive `cf ssh e2e-test`
should also drop us at a prompt inside the container — `ps aux` there is
a strange and pleasant view after ten chapters of building toward it.

**Debug note**: `cf ssh` failures are almost always the proxy path — confirm
the `ssh_proxy` group is deployed and port 2222 answers on HAProxy. On PVE
specifically, confirm the kit's `ocfp/pve/ssh-proxy.yml` overlay was
retained. Despite the name, it is not an ssh enabler but a link-provider fix
for PVE's flat network — and dropping it breaks the deploy itself.

## Sign-off

Push, route, shell: three greens, and the bloc's primary arc is complete —
network to bastion to directors to Vault to platform to running app. We
clean up our test artifacts (`cf delete e2e-test -f`, and the org too if we
like), or keep them as a standing smoke test. `ocfp test smoke` and the CF
smoke-test errand offer the automated version of this chapter when we want
it on a schedule.

What remains is turning a working platform into an operable one:
[11. Platform services](11-platform-services.md).
