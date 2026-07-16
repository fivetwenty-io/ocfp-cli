# 12. Stratos — The Front Door

Everything works, and almost nobody can see it. The platform answers only to
operators with a tailnet login and a bastion full of `/etc/hosts` entries —
fine for us, useless for the teammate we want to hand a URL. This closing
chapter finishes the ingress story chapter 1 promised, reconciles public
DNS, and pushes the Stratos console: the browser-facing face of the bloc,
running *on* the platform it manages. When the canonical URL loads, the arc
is over.

## The tunnel, at last

Recall the shape we committed to: no inbound firewall holes, no DNAT. Public
traffic enters through a Cloudflare tunnel: a `cloudflared` connector on
the bastion dials *out* to Cloudflare and holds the connection open.
Cloudflare routes our hostnames down it to HAProxy at `10.108.20.13`. The
`cloudflare:` block we wrote in chapter 3 is the entire specification, and
bootstrap is the tool that realizes it:

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --yes
```

The same idempotent command from chapter 4, doing its reconciliation duty:
it creates the tunnel under our explicit `tunnel_name` and pushes the
ingress rules (`*.apps` and `*.system` to the HAProxy origin, the SSH
hostname to `:2222`, plus the per-service hostnames for SHIELD, Grafana,
Doomsday, and Concourse that our config lists). Then it upserts the
wildcard CNAMEs in the zone, persists the tunnel credentials to Vault, and
delivers the connector token to the bastion. The bastion's firstboot
machinery runs `cloudflared`.

**Verify**, from the inside out:

```bash
# The connector is up and connected (on the bastion).
systemctl status cloudflared

# Public DNS resolves our wildcards — from any machine, no tailnet.
dig +short api.system.ocf.wayne.lab.fivetwenty.io
dig +short anything.apps.ocf.wayne.lab.fivetwenty.io

# And the whole path answers: internet -> tunnel -> HAProxy -> gorouter.
curl https://api.system.ocf.wayne.lab.fivetwenty.io/v3/info
```

That last command is quietly momentous: it is the first time the platform
has answered a request from the open internet, no `--resolve`, no hosts
file, real certificate chain at the edge. The chapter-9 and chapter-10
workarounds on the bastion are now legacy — worth deleting so they never
mask a real DNS problem.

**Debug note**: a tunnel that connects but 502s is almost always origin TLS
— HAProxy's certificate is self-signed, which is exactly why chapter 3 set
`origin_no_tls_verify: true`. A hostname that resolves but dead-ends means
the ingress rules and the CNAMEs disagree; re-running bootstrap reconciles
both from the config.

**Rollback**: disable is one config line (`cloudflare.enabled: false`) and a
bootstrap re-run. The tunnel and CNAMEs are removed, and the platform
returns to tailnet-only reachability, fully functional for operators.

## The console

Stratos is the piece that makes the bloc feel finished — a web console for
Cloud Foundry. We deploy it the most satisfying way possible: as a CF
app, pushed to the very platform we just validated. The cf kit wraps the
whole thing in an addon:

```bash
g @ocfp-lab-wayne-ocf:cf do stratos deploy
```

The addon fetches the Stratos release, provisions its database through the
marketplace we stood up in chapter 11, renders an app manifest bound to the
console route, and runs the `cf push`. Our canonical URL was named in the
bloc config back in chapter 3 — `fqdns.ocf.stratos`, which is
`console.apps.ocf.wayne.lab.fivetwenty.io` — and the addon reads it from
the same Vault-backed config as everything else.

```bash
g @ocfp-lab-wayne-ocf:cf do stratos info    # status, version, URLs
g @ocfp-lab-wayne-ocf:cf do stratos open    # straight to the browser
```

**Verify**: the console loads at the canonical URL — over the tunnel, from
any browser, no VPN — and logging in with the CF admin credentials shows our
orgs, our spaces, and one small `e2e-test` app if we kept it. Under
Applications, Stratos lists *itself*, which is the platform being
self-hosting in the most literal way.

**Rollback**: it is a CF app. `cf delete stratos -f` (or
`do stratos deploy --force` to redeploy clean), and the platform beneath it
does not notice.

## The arc, closed

Look back at where chapter 1 started: an empty PVE host and a `/20` on
paper. Between there and here we gave the host an identity and a network,
wrote one YAML document, and then let the machinery climb its own
bootstrap ladder — bastion, inception vault, proto-director, real Vault,
second director, Cloud Foundry, the operations constellation, and finally a
console anyone can reach. Two gates guarded the way up, and every rung was
verified before we trusted our weight to it.

Just as important is what we can do now: rebuild the bastion without losing
anything, tear down and recreate the whole bloc from `~/.ocfp/config.yml`
and the deployment repos, restore from SHIELD, and watch Doomsday count
down our certificates. The runbooks end here, but the bloc's story is now
ordinary operations — which was the goal all along.

When something drifts, the reflexes these chapters built are the debugging
manual: `safe targets` when secrets misbehave, `~/.genesis/mylogs/last-trace`
when hooks fail, `bosh task --debug` when deploys do, `ocfp pve probe` when
the substrate itself is in doubt. And when a chapter needs re-running, every
one of them was written to be safe to enter twice.
