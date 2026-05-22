# Stratos Push — Wayne CF (★★ FINAL GATE)

Pushes the branded FiveTwenty Stratos console app to Wayne CF and verifies it
at `https://console.cf.wayne.pve.lab.fivetwenty.io`. This is the final
gate for the Wayne bloc bring-up.

**Run from:** operator Mac. All `cf` and `bun` commands run locally. The
cloudflared tunnel and HAProxy at `10.64.64.50` carry traffic from Cloudflare
to the CF routers — no direct IP access to the cluster is required.

---

## 1. Prerequisites

### 1.1 Prior runbooks complete

All of the following must be complete before this step:

- [CPI Account Setup](01-cpi-account-setup.md) — PVE CPI token in vault
- [Wayne Bastion Bringup](02-bastion-bringup.md) — bastion running on `10.64.64.3`
- 03-cloudflare-tunnel-bootstrap.md — `ocfp-wayne` tunnel configured on bastion
- 04-env-bosh-and-cf-deploy.md — CF deployed and healthy at
  `https://api.cf.wayne.pve.lab.fivetwenty.io`
- 04-dns-reconciliation.md — `*.cf.wayne.pve.lab.fivetwenty.io` DNS CNAME records
  pointing through the cloudflared tunnel to HAProxy `10.64.64.50`

Verify CF is reachable before proceeding:

```bash
curl -sk https://api.cf.wayne.pve.lab.fivetwenty.io/v2/info | jq .name
```

Expected: `"vcap"` (or similar CF version string). Any connection error means
a prerequisite step is incomplete — stop and resolve it first.

### 1.2 Stratos source code

The branded Stratos app lives at:

```
~/w/fivetwenty/studios/ocfp/src/apps/stratos/
```

Confirm the `develop` branch is checked out (develop carries the FiveTwenty
branding assets and the `prebuild-ui` pipeline):

```bash
cd ~/w/fivetwenty/studios/ocfp/src/apps/stratos
git branch --show-current
```

Expected: `develop`. If the branch is wrong, check it out before building:

```bash
git checkout develop && git pull
```

### 1.3 Tooling on operator Mac

Verify all required CLI tools are present:

```bash
cf version       # cf CLI v8+
bun --version    # bun runtime (used for bun install and build scripts)
safe --version   # safe CLI pointed at lab vault
jq --version     # jq for JSON parsing
```

Install `cf` CLI if missing:

```bash
brew install cloudfoundry/tap/cf-cli@8
```

Install `bun` if missing:

```bash
curl -fsSL https://bun.sh/install | bash
```

### 1.4 Vault connectivity

Confirm `safe` can reach the lab vault:

```bash
safe ping
```

Expected: `You are logged in successfully.` If not, run `safe auth` against the
lab vault first.

---

## 2. Target Wayne CF from operator Mac

```bash
cf api https://api.cf.wayne.pve.lab.fivetwenty.io --skip-ssl-validation
```

Log in as admin, pulling the password from vault:

```bash
cf login -u admin -p "$(safe get secret/exodus/ocfp-pve-wayne-cf:admin_password)"
```

Verify the session targets the correct foundation:

```bash
cf target
```

Expected output includes:

```
API endpoint:   https://api.cf.wayne.pve.lab.fivetwenty.io
User:           admin
```

---

## 3. Create system org and stratos space

The Stratos console runs in the `system` org under a `stratos` space. CF ships
with a `system` org in some distributions, but create it explicitly if missing:

```bash
cf create-org system
```

If the org already exists, `cf create-org` prints a notice and continues — this
is safe to run more than once.

Create the space:

```bash
cf create-space stratos -o system
```

Target it:

```bash
cf target -o system -s stratos
```

Verify:

```bash
cf target
```

Expected:

```
Org:   system
Space: stratos
```

---

## 4. Build the branded Stratos app

The build runs on the operator Mac. The Stratos repo uses `bun` for dependency
management; the Angular build is invoked through `bun run`.

```bash
cd ~/w/fivetwenty/studios/ocfp/src/apps/stratos
```

Install dependencies:

```bash
bun install
```

This reads `bun.lock` and installs all packages into `node_modules/`. The
`postinstall` hook runs `build/post-setup.cjs` to link devkit assets — this is
expected and normal.

Run the prebuild pipeline (builds Angular app and zips brand assets):

```bash
bun run prebuild-ui
```

This invokes two `package.json` scripts in sequence:

1. `bun run build` → `ng build stratos --configuration production` — compiles
   the Angular app into `dist/frontend/browser/`
2. `bun run prebuild-zip` → `node build/prebuild-zip.js` — packages brand
   assets into the output bundle

The prebuild step stamps brand images, fonts, and color tokens into the output.
Skipping it produces a default Stratos UI without FiveTwenty branding.

Expected on success: `Build complete.` and a non-zero `dist/frontend/browser/`
directory.

Verify build output:

```bash
ls dist/frontend/browser/ | head
```

Expected: HTML, JS, and CSS files. If the directory is empty or missing, the
build failed — check `bun run prebuild-ui` output for Angular compilation errors.

---

## 5. Verify the manifest

The `manifest.yml` at the root of the Stratos repo is pre-configured for the
Wayne CF environment. Review it before pushing:

```bash
cat manifest.yml
```

The manifest uses the Stratos buildpack and sets `UI_PATH` to point at the
compiled frontend. Key fields as shipped:

```yaml
applications:
  - name: console
    memory: 1512M
    disk_quota: 2047M
    host: console
    timeout: 180
    buildpack: https://github.com/cloudfoundry/stratos-buildpack#v5
    health-check-type: port
    env:
      DIAGNOSTICS_ENABLED: "true"
      NPM_CONFIG_LEGACY_PEER_DEPS: "true"
      UI_PATH: "./ui/frontend/browser"
```

The `host: console` field tells CF to use `console` as the hostname prefix,
producing `console.<default-domain>`. With `cf.wayne.pve.lab.fivetwenty.io` as
the default domain this resolves to `console.cf.wayne.pve.lab.fivetwenty.io` —
matching the DNS record created in the DNS reconciliation runbook.

If the default domain is not `cf.wayne.pve.lab.fivetwenty.io`, add an explicit
route:

```yaml
    routes:
      - route: console.cf.wayne.pve.lab.fivetwenty.io
```

Check the CF default domain:

```bash
cf domains | grep -i wayne
```

---

## 6. Push

```bash
cf push -f manifest.yml
```

The push uploads the working directory (filtered by `.cfignore`), stages it
using the Stratos buildpack, and starts the app. The Stratos buildpack
downloads Go backend dependencies during staging, so the first push takes
5–15 minutes depending on network speed.

Watch the staging stream for errors. A clean push ends with:

```
name:              console
requested state:   started
instances:         1/1
usage:             1.5G x 1 instances
routes:            console.cf.wayne.pve.lab.fivetwenty.io
```

---

## 7. Verify

### 7.1 CF app status

```bash
cf apps
```

Expected:

```
name      requested state   instances   memory   disk   urls
console   started           1/1         1.5G     2G     console.cf.wayne.pve.lab.fivetwenty.io
```

For detail:

```bash
cf app console
```

Check that `state` is `running` for the instance and health check is passing.

### 7.2 HTTP reachability

```bash
curl -sI https://console.cf.wayne.pve.lab.fivetwenty.io
```

Expected: `HTTP/2 200` (or `HTTP/1.1 200 OK`). A redirect to HTTPS is also
acceptable if the `--skip-ssl-validation` flag is needed for curl.

### 7.3 Browser smoke test

Open `https://console.cf.wayne.pve.lab.fivetwenty.io` in a browser.

Expected: FiveTwenty branded Stratos login page — FiveTwenty logo, brand
colors, and product name. If the default Stratos branding appears instead,
the `develop` branch was not checked out or `bun run prebuild-ui` was skipped.

### 7.4 Automated smoke test

```bash
bash ~/w/fivetwenty/studios/ocfp/src/clis/ocfp/scripts/smoke/05-stratos-smoke.sh
```

This script verifies HTTP 200, login form presence, and the branded page title.

---

## 8. Log in to Stratos UI

1. Open `https://console.cf.wayne.pve.lab.fivetwenty.io`
2. Enter credentials:

   - **Username:** `admin`
   - **Password:** `$(safe get secret/exodus/ocfp-pve-wayne-cf:admin_password)`

3. On first login Stratos prompts to configure a CF endpoint. Use:

   - **API endpoint:** `https://api.cf.wayne.pve.lab.fivetwenty.io`
   - **Skip SSL validation:** enabled (self-signed cert behind cloudflared tunnel)

4. After connecting, navigate to **Organizations → system → Spaces**. The
   `stratos` space must appear.

---

## 9. Troubleshooting

### 9.1 `cf push` fails during staging

Check staging logs and recent events:

```bash
cf logs console --recent
cf events console
```

Common causes:

- **Buildpack download timeout:** The Stratos buildpack fetches Go modules during
  staging. If the bastion or CF cells lack outbound internet access, staging
  stalls. Verify that CF cells can reach `https://github.com` and
  `https://proxy.golang.org`.
- **Out of disk:** The default `disk_quota: 2047M` is near the CF cell limit.
  If cells have less than 2 GB free, push fails. Check cell capacity:

  ```bash
  cf curl /v2/info | jq .
  cf curl /v2/apps?results-per-page=100
  ```

- **Memory quota exceeded:** The `system` org may have a quota set below 1.5 GB.
  Check and adjust:

  ```bash
  cf org system
  cf update-quota default -m 4G
  ```

### 9.2 DNS not resolving

Verify the Cloudflare DNS records from the DNS reconciliation runbook exist:

```bash
flarectl dns list --zone lab.fivetwenty.io | grep cf.wayne
```

Expected: CNAME records pointing `*.cf.wayne.pve.lab.fivetwenty.io` to the
cloudflared tunnel hostname (`<uuid>.cfargotunnel.com`).

Test DNS resolution locally:

```bash
dig console.cf.wayne.pve.lab.fivetwenty.io +short
```

Expected: an IP address (Cloudflare anycast), not `NXDOMAIN`.

### 9.3 SSL certificate errors in browser

The cloudflared tunnel provides a Cloudflare-issued certificate. The browser
should trust it by default (Cloudflare is a trusted CA).

If the browser shows an untrusted cert:

- Confirm cloudflared is running on the bastion: `ssh ubuntu@10.64.64.3 systemctl status cloudflared`
- Verify the tunnel config maps `console.cf.wayne.pve.lab.fivetwenty.io` to
  `http://10.64.64.50` (HAProxy, port 80), not to an HTTPS backend. Cloudflare
  handles TLS termination at its edge; the origin connection from cloudflared
  to HAProxy is plain HTTP inside the private network.

### 9.4 Stratos cannot connect to CF API

Stratos calls `https://api.cf.wayne.pve.lab.fivetwenty.io` from inside the CF
cell. The cell must be able to reach the Gorouter through the CF network. If
Stratos shows a connection error after login:

1. Verify the `api.cf.wayne.pve.lab.fivetwenty.io` DNS record exists:

   ```bash
   dig api.cf.wayne.pve.lab.fivetwenty.io +short
   ```

2. Verify the CF API responds to an in-cluster request (from the bastion):

   ```bash
   ssh ubuntu@10.64.64.3 curl -sk https://api.cf.wayne.pve.lab.fivetwenty.io/v2/info | jq .name
   ```

3. If the CF API cert is self-signed and Stratos rejects it, set
   `CF_API_FORCE_SECURE: false` in the manifest env block and re-push. This is a
   development environment so cert relaxation is acceptable.

### 9.5 Branding missing (default Stratos UI appears)

The brand assets are built by `bun run prebuild-ui` from the `develop` branch.
If default branding appears:

1. Confirm git branch:

   ```bash
   cd ~/w/fivetwenty/studios/ocfp/src/apps/stratos && git branch --show-current
   ```

   Must be `develop`. Switch and rebuild if not.

2. Confirm `prebuild-ui` ran successfully. Check for brand asset files:

   ```bash
   ls dist/frontend/browser/ | grep -i fivetwenty
   ```

   If missing, re-run the full build sequence (Step 4) and push again.

3. If the browser is serving a cached version, hard-refresh (`Ctrl+Shift+R` or
   `Cmd+Shift+R`) before concluding branding is absent.

---

## 10. Summary of key values

| Item | Value |
|------|-------|
| CF API | `https://api.cf.wayne.pve.lab.fivetwenty.io` |
| Stratos URL | `https://console.cf.wayne.pve.lab.fivetwenty.io` |
| CF org / space | `system` / `stratos` |
| App name | `console` |
| CF admin password vault path | `secret/exodus/ocfp-pve-wayne-cf:admin_password` |
| Stratos source | `~/w/fivetwenty/studios/ocfp/src/apps/stratos` (branch: `develop`) |
| Build command | `bun install && bun run prebuild-ui` |
| HAProxy (CF ingress) | `10.64.64.50` |
| Cloudflared tunnel | `ocfp-wayne` (running on bastion `10.64.64.3`) |

---

## 11. Next steps

With Stratos live, the Wayne bloc bring-up is complete. Final state:

- PVE CPI service account and token in vault
- Bastion running at `10.64.64.3`, provisioned with all operator tooling
- Cloudflared tunnel `ocfp-wayne` routing public traffic to the CF cluster
- DNS records for `*.cf.wayne.pve.lab.fivetwenty.io` live in Cloudflare
- CF foundation healthy (BOSH director + CF deployment)
- Stratos console deployed and accessible with FiveTwenty branding

Record completion in the operator log and advance any tracking tickets to done.
