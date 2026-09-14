# 12. Stratos: The Front Door

Everything works, and almost nobody can see it. The platform answers only to operators with a tailnet login and a bastion full of `/etc/hosts` entries, which is fine for us and useless for the teammate we want to hand a URL. This closing chapter finishes the ingress story chapter 1 promised, reconciles public DNS, and pushes the Stratos console. That console is the browser-facing face of the bloc, and it runs *on* the platform it manages. When the canonical URL loads, the arc is over.

## The tunnel, at last

Recall the shape we committed to, with no inbound firewall holes and no DNAT. Public traffic enters through a Cloudflare tunnel, which means a `cloudflared` connector on the bastion dials *out* to Cloudflare and holds the connection open. Cloudflare routes our hostnames down it to HAProxy at `10.108.20.97`. The `cloudflare:` block we wrote in chapter 3 is the entire specification, and bootstrap is the tool that realizes it:

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --yes
```

This is the same idempotent command from chapter 4, doing its reconciliation duty. It creates the tunnel under our explicit `tunnel_name` and pushes the ingress rules (`*.apps` and `*.system` to the HAProxy origin, the SSH hostname to `:2222`, plus the per-service hostnames for SHIELD, Grafana, Doomsday, and Concourse that our config lists). Then it upserts the wildcard CNAMEs in the zone, persists the tunnel credentials to Vault, and delivers the connector token to the bastion. The bastion's firstboot machinery runs `cloudflared`.

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

That last command is quietly momentous, because it is the first time the platform has answered a request from the open internet. There is no `--resolve` and no hosts file, and the certificate chain at the edge is a real one. The chapter-9 and chapter-10 workarounds on the bastion are now legacy, and we should delete them so they never mask a real DNS problem.

**Debug note.** A tunnel that connects but 502s is almost always failing on origin TLS, because HAProxy's certificate is self-signed, which is exactly why chapter 3 set `origin_no_tls_verify: true`. A hostname that resolves but dead-ends means the ingress rules and the CNAMEs disagree, and re-running bootstrap reconciles both from the config.

**Rollback** does not work the way it looks, and getting this wrong leaves a live tunnel behind.

Setting `cloudflare.enabled: false` and re-running bootstrap removes nothing. Bootstrap has no delete path for Cloudflare at all, so the tunnel and the CNAMEs stay exactly where they are. Worse, `ocfp teardown` checks that same flag before it cleans Cloudflare up and returns immediately when it is false, so disabling the feature is what permanently prevents the only code that could remove the tunnel from ever running. The front door stays open while the config says it is shut.

Leave `cloudflare.enabled: true` and let `ocfp teardown` do the removal. To stop serving traffic without tearing the bloc down, disable the tunnel at Cloudflare's end, or stop the connector on the bastion, and leave the config telling the truth about what exists.

## The tailscale alternative

The tunnel above is what we get by leaving `ingress.provider` unset with `cloudflare.enabled: true`, because the config we wrote in chapter 3 resolves to `cloudflared` by default. An operator who already lives on a tailnet and would rather skip a tunnel connector entirely can write one more line instead:

```yaml
    ingress:
      provider: tailscale
```

Everything else in the chapter-3 `cloudflare:` block stays, because we still need the zone, the API token, and `origin`. Cloudflare is doing DNS management here rather than tunneling. Bootstrap's reconciliation shape does not change either, and we run the same `ocfp bootstrap --bloc ocfp-lab-wayne --yes`, but what it *does* underneath is different. No `cloudflared` connector gets installed on the bastion. Instead, a late "Configure Ingress DNS" step waits for the bastion to appear on the tailnet, then points `ocf.wayne.lab.fivetwenty.io` and `*.ocf.wayne.lab.fivetwenty.io` at its `100.x` tailnet IP (A records, TTL 60, and unproxied, because Cloudflare's edge cannot route to a tailnet address). On the bastion itself, an nftables table DNATs inbound 80/443 from `tailscale0` to the HAProxy origin and masquerades the reply so it routes back through the bastion rather than blackholing at the SDN gateway.

**Verify** the same way we did on the tunnel path, with the tailnet in the loop instead of `cloudflared`:

```bash
# The DNS record now resolves off-tailnet too — Cloudflare answers it even
# though only tailnet clients can actually route to the address.
dig +short api.system.ocf.wayne.lab.fivetwenty.io

# From a machine joined to the tailnet, the whole path answers.
curl https://api.system.ocf.wayne.lab.fivetwenty.io/v3/info

# On the bastion: confirm the forwarding table is present.
nft list table ip ocfp_ingress
```

**Rollback** has the same trap as the tunnel path, inverted. `ocfp teardown` removes the two A records only when `ingress.provider` still resolves to `tailscale`, so flipping it back to `cloudflared` first is precisely what makes the DNS cleanup skip itself. Tear down with the tailscale provider still set, and change the provider afterwards if we are keeping the bloc.

Note also that `ocfp teardown` is not a way to remove two DNS records. It destroys the bastion along with everything else, so if the records are all we want gone, delete them in Cloudflare.

A bastion that was already running before this config existed needs its template rebuilt or its watchdog script hand-patched, because the nftables rule only gets installed by firstboot or the watchdog, and it only takes effect at the bastion's next restart. Full detail, including the DNS-to-DNAT sequence diagram and the masquerade rationale, lives in [Ingress Providers](../../networking/ingress-providers.md); the tailnet join mechanics are in [Bastion Tailscale](../../init/bastion-tailscale.md).

## The console

Stratos is a web console for Cloud Foundry, and it is the piece that makes the bloc feel finished. We deploy it in the most satisfying way possible, as a CF app pushed to the very platform we just validated. The cf kit wraps the whole thing in an addon:

```bash
g @ocfp-lab-wayne-ocf:cf do stratos deploy
```

The addon fetches the Stratos release, provisions its database through the marketplace we stood up in chapter 11, renders an app manifest bound to the console route, and runs the `cf push`. Our canonical URL was named in the bloc config back in chapter 3 under `fqdns.ocf.stratos`, which is `console.apps.ocf.wayne.lab.fivetwenty.io`, and the addon reads it from the same Vault-backed config as everything else.

```bash
g @ocfp-lab-wayne-ocf:cf do stratos info    # status, version, URLs
g @ocfp-lab-wayne-ocf:cf do stratos open    # straight to the browser
```

**Verify** that the console loads at the canonical URL over the tunnel, from any browser, with no VPN. Logging in with the CF admin credentials shows our orgs, our spaces, and one small `e2e-test` app if we kept it. Under Applications, Stratos lists *itself*, which is the platform being self-hosting in the most literal way.

**Rollback** is easy, because Stratos is only a CF app. We run `cf delete stratos -f` (or `do stratos deploy --force` to redeploy clean), and the platform beneath it does not notice.

## The arc, closed

Look back at where chapter 1 started, with an empty PVE host and a `/20` on paper. Between there and here we gave the host an identity and a network, wrote one YAML document, and then let the machinery climb its own bootstrap ladder: bastion, inception vault, proto-director, real Vault, second director, Cloud Foundry, the operations constellation, and finally a console anyone can reach. Two gates guarded the way up, and every rung was verified before we trusted our weight to it.

Just as important is what we can do now. We can rebuild the bastion without losing anything, tear down and recreate the whole bloc from `~/.config/ocfp/config.yml` and the deployment repos, restore from SHIELD, and watch Doomsday count down our certificates. The runbooks end here, but the bloc's story is now ordinary operations, which was the goal all along.

When something drifts, the reflexes these chapters built are the debugging manual: `safe targets` when secrets misbehave, `~/.genesis/mylogs/last-trace` when hooks fail, `g @<env>:<dep> b task <id> --debug` when deploys do, `ocfp pve probe` when the substrate itself is in doubt. And when a chapter needs re-running, every one of them was written to be safe to enter twice.
