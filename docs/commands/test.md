# `ocfp test`

`ocfp test <suite>` runs validation suites against the bloc's Cloud Foundry foundation. Every suite drives the `cf` CLI and plain HTTP checks, so it runs from any machine that is logged in to the foundation. On our labs that machine is the bastion.

## What a run does

The command resolves the API endpoint from the bloc config first. It takes `fqdns.ocf.system` when that entry is set, falls back to `system.<fqdns.base>`, and falls back again to the legacy `dns` list. The `--api` flag overrides all three, and when the config has none of them the command keeps whatever `cf api` already targets. When the resolved endpoint differs from the current target, the command runs `cf api` for us. It passes `--skip-ssl-validation` whenever our existing cf session was created with it, so the HTTPS route checks use the same TLS choice as our login. The `--skip-ssl-validation` flag forces the choice either way.

The run must find a logged-in session, because the command never logs in on our behalf. It then creates `<bloc>-test-org` and `<bloc>-test-space`, targets them, and reads the org's default domain, the internal domain, and any TCP domain from the foundation itself. Every app and service instance it creates carries a random per-run suffix, so two runs can overlap without colliding. When the suites finish it deletes the test org, which removes everything the run created, unless `--skip-cleanup` is set.

Each check is named `<suite>/<check>` and ends as passed, failed, or skipped. A skipped check always carries a reason, and skips are counted as skips, never as passes. A check whose prerequisite did not pass is skipped rather than run, so one failed push produces one failure and a handful of clearly labelled skips instead of a cascade. Cleanup checks such as `delete_app` still run after a failure, and they are skipped only when the thing they clean up was never created. The command exits non-zero when any check fails.

## Suites

### smoke

The smoke suite generates a small `staticfile_buildpack` app in a temporary directory and pushes it with a route on the apps domain. It then fetches the route over HTTPS until it answers 200 with the app's marker in the body, runs `cf logs --recent` against the app and requires at least one application log line, and deletes the app.

The checks are `push_app`, `route_https_200`, `logs_recent`, and `delete_app`.

### c2c

The c2c suite pushes a static backend and a small Python frontend that proxies its `/proxy` path to the URL in its `BACKEND_URL` environment variable. It maps an internal route on the backend under the foundation's internal domain, which is `apps.internal` on a standard deployment, and points the frontend at that route on port 8080. Before any policy exists it confirms the frontend's `/proxy` path answers 502, which proves the policy is actually enforced. It then adds a network policy from the frontend to the backend on port 8080 and fetches `/proxy` until the backend's marker comes back through the internal route. The suite removes the policy and deletes both apps when it is done.

The checks are `find_internal_domain`, `push_backend`, `map_internal_route`, `push_frontend`, `denied_without_policy`, `add_network_policy`, `fetch_via_internal_route`, `remove_network_policy`, and `delete_apps`.

### blacksmith

The blacksmith suite reads the brokers, offerings, and plans from the marketplace and picks the first offering whose broker name contains `blacksmith` and that has an available plan. It prefers a public plan, and when the chosen plan is not public it enables access for the test org. The `--offering` and `--plan` flags pin the choice, and naming an offering or plan that does not exist is a failure rather than a skip. The suite pushes a test app, creates a service instance, and polls the instance until its asynchronous create succeeds or `--service-timeout` elapses. It creates a service key, fetches the key's details, and requires at least one credential, reporting the credential names but never their values. It binds the instance to the test app, then unbinds, deletes the key, deletes the instance, and waits for each removal to finish.

The checks are `find_offering`, `push_app`, `create_instance`, `create_service_key`, `bind_app`, `unbind_app`, `delete_service_key`, `delete_instance`, and `delete_app`.

### tcp

The tcp suite looks for a domain that routes TCP, or the one named by `--tcp-domain`, and skips with a clear reason when the foundation has none. It pushes the static app, maps a TCP route with a random port through `cf map-route`, reads the assigned port back from the app's routes, and opens a raw TCP connection to `<tcp-domain>:<port>` to send an HTTP request and read a 200 with the app's marker.

The checks are `find_tcp_domain`, `push_app`, `map_tcp_route`, `connect_tcp_route`, and `delete_app`.

### nfs and smb

The volume suites look for an offering named `nfs` or `smb`, or tagged that way, and skip with a clear reason when the marketplace has no volume broker. They also skip when the broker is present but we gave no share, because the broker cannot create an instance without one. With `--nfs-share host:/export` or `--smb-share //host/share` the suite pushes the static app, creates an instance for the share, and binds it. The bind must succeed and its details must carry at least one volume mount. The suite then restarts the app, which starts the container with the volume attached, and confirms the route still answers. For SMB, `--smb-username` and `--smb-password` are passed as bind parameters, and the password can come from `OCFP_TEST_SMB_PASSWORD` instead of the command line.

The checks are `find_broker`, `push_app`, `create_instance`, `bind_app`, `volume_mount_on_restart`, `unbind_app`, `delete_instance`, and `delete_app`.

### acceptance

The acceptance suite has two modes and prints which one it used at the top of the run and again in the summary. When a BOSH director is named by `--bosh-env` or `BOSH_ENVIRONMENT`, the `bosh` CLI is on the path, and `bosh -e <alias> env` answers, the suite runs the Cloud Foundry `smoke-tests` errand with `bosh -e <alias> -d <deployment> run-errand smoke-tests`. The deployment comes from `--bosh-deployment`, then `BOSH_DEPLOYMENT`, then `cf`. When no director is usable the suite runs smoke, c2c, and blacksmith through the cf CLI instead and states the reason, such as an unset `BOSH_ENVIRONMENT` or a director that did not answer.

### all

`all` runs smoke, c2c, blacksmith, nfs, smb, and tcp. With `--parallel` the suites run concurrently in the shared test org, since every suite names its own apps and instances.

## Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--parallel` | off | Run the suites of `all` or a fallback `acceptance` concurrently |
| `--timeout` | `30m` | Timeout for each check |
| `--verbose` | off | Print each check's output after its status line |
| `--output <file>` | none | Write a JUnit XML report, or JSON when the name ends in `.json` |
| `--skip-cleanup` | off | Leave the test org, apps, and service instances in place |
| `--retries` | `1` | Retries for a failed check |
| `--tags a,b` | none | Only run checks whose name contains one of the tags |
| `--exclude a,b` | none | Skip checks whose name contains one of the tags |
| `--api <url>` | from config | CF API endpoint |
| `--skip-ssl-validation` | from cf config | Skip TLS verification for `cf api` and the HTTPS checks |
| `--offering <name>` | first Blacksmith offering | Blacksmith offering to test |
| `--plan <name>` | first available plan | Blacksmith plan to test |
| `--bosh-env <alias>` | `$BOSH_ENVIRONMENT` | BOSH environment alias for the acceptance errand |
| `--bosh-deployment <name>` | `$BOSH_DEPLOYMENT`, then `cf` | Deployment that owns the `smoke-tests` errand |
| `--tcp-domain <domain>` | first TCP domain | TCP domain for the tcp suite |
| `--nfs-share host:/path` | none | NFS export for the nfs suite |
| `--smb-share //host/share` | none | SMB share for the smb suite |
| `--smb-username <user>` | none | SMB bind username |
| `--smb-password <pass>` | `$OCFP_TEST_SMB_PASSWORD` | SMB bind password |
| `--service-timeout` | `15m` | Bound for asynchronous service instance and binding operations |

Cleanup checks ignore `--tags` and `--exclude`, so a filtered run still deletes what it created. Because check names carry the suite prefix, `--exclude tcp,smb` drops whole suites from `all`, and `--tags smoke` keeps only the smoke checks.

## Output

Each check prints one line as it finishes, in the form `PASSED  smoke/route_https_200 (4.2s)`, with the reason appended for a skip and the first line of the error for a failure. The summary lists the counts, then every failed check with its error, then every skipped check with its reason. The JUnit report groups checks into one `testsuite` element per suite, records skips as `skipped` elements with the reason as the message, and puts each check's output in `system-out`.

## Examples

```bash
# Smoke test the foundation from the bastion
ocfp test smoke

# Everything, concurrently, with a JUnit report for CI
ocfp test all --parallel --output results.xml

# A specific Blacksmith offering and plan
ocfp test blacksmith --offering redis --plan standalone

# The CF smoke-tests errand through the director aliased "ocf"
ocfp test acceptance --bosh-env ocf

# NFS against an existing export, keeping the org for inspection
ocfp test nfs --nfs-share 10.0.0.5:/export/cf --skip-cleanup

# Drop the suites this foundation does not offer
ocfp test all --exclude tcp,smb
```
