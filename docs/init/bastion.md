# Bastion Initialization

This document describes the `ocfp init bastion` command, which provisions a bastion host with all necessary tools and configurations for Cloud Foundry operations.

## Overview

The `init bastion` command automates the complete setup of a bastion host by:
- Installing system packages and dependencies
- Setting up CloudFoundry tools (BOSH, CF CLI, Vault, etc.)
- Installing Genesis deployment tools
- Configuring provider-specific CLIs (AWS, STACKIT, Azure, GCP, etc.)
- Installing CF plugins for enhanced functionality
- Setting up the OCFP directory structure and configuration

## Command Usage

```bash
ocfp --bloc <bloc-name> init bastion [flags]
```

### Flags
- `--dry-run`: Show what would be installed without making changes
- `--force`: Force re-initialization even if already provisioned
- `--skip-verification`: Skip post-installation verification steps

## Installation Phases

The bastion initialization proceeds through these phases:

1. **prerequisite_check**: Verify system requirements and connectivity
2. **system_setup**: Configure hostname, timezone, and system settings
3. **directories**: Create OCFP directory structure
4. **config_files**: Generate configuration files
5. **ocfp_directories**: Set up OCFP-specific directories
6. **configuration_files**: Deploy environment configurations
7. **repositories**: Add and configure package repositories
8. **packages**: Install system packages via APT
9. **binary_tools**: Install Genesis and CloudFoundry binary tools
10. **snap_packages**: Install tools via Snap (go, kubectl, helm)
11. **cpan_modules**: Install Perl modules for OCFP integration
12. **cf_plugins**: Install CloudFoundry CLI plugins
13. **advanced_tools**: Install additional development tools
14. **environment**: Configure shell and system environment
15. **verification**: Verify all installations

## Installed Components

### Core System Packages
- **Build Tools**: gcc, make, build-essential, libtool
- **Version Control**: git, tig
- **Text Processing**: gawk, sed, grep, ripgrep, ack-grep
- **Network Tools**: curl, wget, rsync, s3cmd
- **Compression**: tar, gzip, unzip
- **Development**: python3, python3-pip, perl, cpanminus, ruby
- **Editors**: vim, vim-common

### CloudFoundry Tools
- **BOSH CLI** (v7+): Infrastructure orchestration
- **CF CLI** (v8): CloudFoundry command-line interface
- **CredHub CLI** (v2.9+): Credential management
- **UAA CLI**: User Account and Authentication
- **Genesis**: Deployment kit manager
- **Safe**: Vault CLI wrapper
- **Spruce**: YAML templating tool
- **Vault**: Secret management

### CloudFoundry Plugins
- **targets**: Switch between multiple CF targets
- **top**: Monitor CF applications and resource usage
- **logs**: Enhanced log streaming and filtering
- **app-autoscaler-plugin**: Manage application auto-scaling policies

### Provider-Specific Tools
- **STACKIT CLI**: STACKIT cloud provider management
- **AWS CLI v2**: Amazon Web Services management
- **Azure CLI**: Microsoft Azure management
- **GCP SDK**: Google Cloud Platform management
- **OpenStack CLI**: OpenStack cloud management

### Development Tools
- **Node.js** (via NVM): JavaScript runtime
- **Bun**: Fast JavaScript runtime and toolkit
- **Neovim**: Modern text editor
- **Snap Tools**: go, kubectl, helm
- **yq**: YAML processor
- **fly**: Concourse CI/CD
- **graft**: Binary deployment tool

## Recent Fixes and Updates

### Idempotency and Performance Optimizations (2025-10-22)
**Fixed**: Time reporting accuracy and added intelligent idempotency checks

The bastion initialization now includes three major improvements:

1. **Accurate Time Reporting**
   - Fixed critical bug where start time was never set, causing wildly incorrect duration reporting
   - Duration now accurately reflects actual provisioning time
   - Progress ETA calculations are now correct

2. **Fast Re-initialization**
   - Added early exit check via `~/.ocfp/provisioned` completion marker
   - Re-running `init bastion` on already-provisioned system now completes in <1 second
   - Previous behavior: Re-run took 5-12+ minutes even with idempotency checks
   - New behavior: Immediate exit with confirmation message

3. **Optimized Script Execution**
   - Provisioning script checks for completion marker before running any checks
   - Eliminates overhead of checking already-installed packages/tools
   - Each provisioning step remains idempotent for partial re-runs

**Behavior Changes**:
- First run: Normal provisioning (5-12 minutes depending on system)
- Subsequent runs: Instant exit (~0.5 seconds)
- Partial re-provisioning: Remove `~/.ocfp/provisioned` marker to force re-run

**Example Output** (Already Provisioned):
```bash
$ ocfp --bloc 520-aws-wayne init bastion
✓ Bastion already fully provisioned - skipping initialization
  Provisioned at: 2025-10-22 14:32:15
```

**Force Re-provisioning**:
```bash
# Remove completion marker on remote bastion
ssh bastion-host 'rm ~/.ocfp/provisioned'

# Re-run initialization
ocfp --bloc 520-aws-wayne init bastion
```

### CredHub CLI Download (v2.9.50+)
**Fixed**: Repository migration and URL format change

The CredHub CLI repository moved from `cloudfoundry-incubator` to `cloudfoundry` organization, and the binary naming convention changed to include architecture.

- **Old URL**: `https://github.com/cloudfoundry-incubator/credhub-cli/releases/download/${VERSION}/credhub-linux-${VERSION}.tgz`
- **New URL**: `https://github.com/cloudfoundry/credhub-cli/releases/download/${VERSION}/credhub-linux-amd64-${VERSION}.tgz`

This fix resolves the `curl: (22) The requested URL returned error: 404` error during the repositories phase.

### App-Autoscaler Plugin (v4.1.2+)
**Added**: CF plugin for application auto-scaling

The app-autoscaler-plugin is now automatically installed from the CF-Community repository, enabling:
- Dynamic application scaling based on custom metrics
- Schedule-based scaling policies
- Resource threshold-based scaling
- Integration with CF Application Runtime

Installation method:
```bash
cf install-plugin -r CF-Community app-autoscaler-plugin
```

The CF-Community repository (`https://plugins.cloudfoundry.org`) is automatically added during initialization if not already present.

## Configuration

### Bloc Configuration

Customize bastion provisioning in your bloc configuration file (e.g., `~/.config/ocfp/520-aws-wayne.yml`):

```yaml
bastion:
  # Genesis configuration
  genesis:
    enabled: true
    repo: "git@github.com:genesis-community/genesis.git"
    branch: "v3.1.x-dev"

  # Git configuration
  git:
    user:
      name: "Your Name"
      email: "your.email@example.com"

  # Tool overrides
  tools:
    enable: []   # Enable additional tools
    disable: []  # Disable specific tools

  # Binary tool overrides
  toolOverrides:
    vault:
      version: "1.20.4"  # Pin specific version
    bosh:
      version: "7.5.2"

  # CF Plugin configuration
  cfPlugins:
    enable: ["app-autoscaler-plugin"]
    disable: []  # Disable specific plugins

  # CF Plugin overrides
  cfPluginOverrides:
    app-autoscaler-plugin:
      force: true        # Force reinstall
      version: "4.1.2"   # Pin specific version
    targets:
      githubRepo: "cloudfoundry-community/cf-targets-plugin"
      version: "latest"
```

### Bastion SSH Keys (`bastion.keys`)

Inject SSH public keys into the bastion's `~/.ssh/authorized_keys` during `ocfp init bastion`. This allows additional users to SSH directly into the bastion host.

```yaml
bastion:
  keys:
    alice: "ssh-ed25519 AAAA... alice@example.com"
    bob: "github/bob-github-username"
    carol: "gitlab/carol-gitlab-username"
```

Each entry maps a label (used as a comment in `authorized_keys`) to a key spec.

Key spec formats:

- **Direct public key** — an SSH public key string starting with `ssh-rsa`, `ssh-ed25519`, or `ecdsa-sha2-*`

- **`github/<username>`** — fetches all public keys from `https://github.com/<username>.keys`

- **`gitlab/<username>`** — fetches all public keys from `https://gitlab.com/<username>.keys`

Keys already present in `authorized_keys` are skipped (deduplicated). Failed lookups log a warning and continue processing other entries.

### Jumpbox Users (`jumpbox.users`)

Store SSH keys in Vault for jumpbox user accounts. These are written to the Vault jumpbox users path and used by the jumpbox BOSH release.

```yaml
jumpbox:
  users:
    alice: "ssh-ed25519 AAAA... alice@example.com"
    bob: "github/bob-github-username"
    carol: "gitlab/carol-gitlab-username"
```

The same key spec formats are supported (direct key, `github/<username>`, `gitlab/<username>`).

**Deprecation notice:** The old top-level `users:` field on the bloc config is deprecated. Move entries under `jumpbox.users` instead. When `jumpbox.users` is empty, the old `users` field is used as a fallback and a deprecation warning is logged.

### Environment Variables

Provider-specific environment variables are automatically configured:

**AWS**:
```bash
AWS_ACCESS_KEY_ID=<from-config>
AWS_SECRET_ACCESS_KEY=<from-config>
AWS_DEFAULT_REGION=<from-config>
```

**STACKIT**:
```bash
STACKIT_PROJECT_ID=<from-config>
STACKIT_ORG_ID=<from-config>
STACKIT_REGION=<from-config>
```

**Azure**:
```bash
AZURE_SUBSCRIPTION_ID=<from-config>
AZURE_TENANT_ID=<from-config>
AZURE_CLIENT_ID=<from-config>
AZURE_CLIENT_SECRET=<from-config>
```

## Directory Structure

After initialization, the bastion will have this structure:

```
$HOME/
├── .ocfp/
│   ├── config/              # OCFP configuration files
│   ├── logs/
│   │   └── provision/       # Initialization logs
│   └── provisioned          # Completion marker
├── ocfp/
│   ├── cli/                 # OCFP CLI binaries
│   └── deployments/         # Genesis deployments
├── deployments/             # Deployment manifests
├── bin/                     # User binaries
└── .ssh/                    # SSH keys and config
```

## Verification

The initialization process includes comprehensive verification:

### Tool Verification
```bash
# CloudFoundry tools
cf --version
bosh --version
credhub --version
vault --version
safe --version
spruce --version

# Genesis
genesis --version

# Provider tools (as applicable)
aws --version
stackit --version
az --version
gcloud --version

# Operator helper scripts (~/bin)
blobstores help
```

### Operator helper scripts

The `helper_scripts` phase installs self-contained bash tools from the OCFP
CLI repo's `scripts/` tree into `~/bin` on the bastion. They are pure shell
wrappers around already-installed CLIs (`safe`, `aws`, `curl`) — re-runs of
`init bastion` overwrite them with the operator-host source.

| Script | Purpose |
|--------|---------|
| `blobstores` | Validate the bloc's ocfp-artifacts RustFS endpoint (reachability + bucket round-trip sweep) |

Operator host search order for each helper:

1. `$OCFP_HELPER_SCRIPTS_DIR/<name>`
2. `./scripts/<name>` (when running ocfp from the CLI repo)
3. `<exe-dir>/../scripts/<name>` and `<exe-dir>/scripts/<name>`
4. `~/w/fivetwenty/studios/ocfp/src/clis/ocfp/scripts/<name>`

A missing source is logged and skipped — old operator checkouts still complete `init bastion`.

### Plugin Verification
```bash
# List installed CF plugins
cf plugins

# Verify app-autoscaler plugin
cf autoscaling-apps
cf autoscaling-policy --help
```

### Log File
Detailed logs are saved to:
```
~/.local/state/ocfp/logs/provision/bastion-init-YYYYMMDD-HHMMSS.log
```

## Troubleshooting

### Common Issues

#### 1. Package Repository 404 Errors
**Symptom**: `curl: (22) The requested URL returned error: 404`

**Causes**:
- Repository moved or renamed
- Binary naming convention changed
- Version API endpoint changed

**Resolution**: Check the tool's GitHub releases page and update configuration URLs.

#### 2. debconf Warnings
**Symptom**:
```
debconf: unable to initialize frontend: Dialog
debconf: falling back to frontend: Teletype
```

**Impact**: Harmless warnings that occur during non-interactive package installation over SSH.

**Resolution**: No action required - packages install correctly despite warnings.

#### 3. Snap Package Installation Slow
**Symptom**: Snap package installation takes 30+ seconds per package

**Cause**: Snap daemon initialization and package download

**Resolution**: This is expected behavior - snap packages are larger and require daemon setup.

#### 4. CF Plugin Installation Fails
**Symptom**: `Error: Plugin <name> does not exist in repository`

**Causes**:
- CF-Community repository not added
- Plugin name mismatch
- Network connectivity issues

**Resolution**:
```bash
# Manually add CF-Community repository
cf add-plugin-repo CF-Community https://plugins.cloudfoundry.org

# List available plugins
cf repo-plugins -r CF-Community

# Install plugin manually
cf install-plugin -r CF-Community app-autoscaler-plugin
```

#### 5. Genesis Tool Download Failures
**Symptom**: Failed to download genesis/safe/spruce tools

**Cause**: GitHub API rate limiting or network issues

**Resolution**: Wait a few minutes and retry initialization with `--force` flag.

### Phase-Specific Debugging

If initialization fails at a specific phase, you can examine the generated provisioning script:

```bash
# View the generated script
cat /tmp/provision-bastion.sh

# Check the phase that failed
grep -A 20 "Phase failed: <phase-name>" ~/.local/state/ocfp/logs/provision/bastion-init-*.log

# Manually execute specific sections
ssh bastion-host 'bash -s' < /tmp/provision-bastion.sh
```

### Re-initialization

To force re-initialization:

```bash
# Remove provisioned marker
rm ~/.ocfp/provisioned

# Run initialization with force flag
ocfp --bloc <bloc-name> init bastion --force
```

## Examples

### Basic Initialization
```bash
# Initialize bastion for AWS bloc
ocfp --bloc 520-aws-wayne init bastion

# Expected output:
# [1/15] Starting phase: prerequisite_check
# ✓ Phase completed: prerequisite_check (2.1s)
# [2/15] Starting phase: system_setup
# ✓ Phase completed: system_setup (12.2s)
# ...
# ✓ Bastion provisioning completed successfully
```

### Dry Run
```bash
# See what would be installed without making changes
ocfp --bloc 520-aws-wayne init bastion --dry-run
```

### Force Re-initialization
```bash
# Completely re-provision bastion
ocfp --bloc 520-aws-wayne init bastion --force
```

### Selective Re-provisioning
For manual selective re-installation, SSH to the bastion and run specific sections:

```bash
# Re-install CF plugins only
ssh bastion-host
cf install-plugin -r CF-Community app-autoscaler-plugin -f

# Update a specific tool
curl -fsSL "https://github.com/cloudfoundry/credhub-cli/releases/download/2.9.50/credhub-linux-amd64-2.9.50.tgz" -o /tmp/credhub.tgz
tar -xzf /tmp/credhub.tgz -C /tmp
sudo install /tmp/credhub /usr/local/bin/credhub
credhub --version
```

## Performance Considerations

### Initial Provisioning Times
Typical initialization times for **first-time provisioning**:
- **Small systems** (2 vCPU, 4GB RAM): 8-12 minutes
- **Medium systems** (4 vCPU, 8GB RAM): 5-8 minutes
- **Large systems** (8+ vCPU, 16GB+ RAM): 3-5 minutes

### Re-initialization Performance (NEW)
**Already provisioned systems**: < 1 second

The initialization now includes intelligent completion detection:
- Checks for `~/.ocfp/provisioned` marker before any provisioning steps
- Exits immediately if bastion is already fully provisioned
- Eliminates overhead from checking already-installed packages

### Phase Duration Breakdown
Phases with longest duration (first-time provisioning only):
1. **packages**: APT package installation (2-4 minutes)
2. **snap_packages**: Snap daemon and package installation (1-3 minutes)
3. **advanced_tools**: Binary tool downloads and compilation (1-2 minutes)
4. **cf_plugins**: CF plugin installation (30-90 seconds)

**Note**: Re-running init on already-provisioned systems bypasses all phases and completes instantly.

## Security Considerations

### Credential Management
- Never commit credentials to configuration files
- Use environment variables or secure vaults for sensitive data
- Rotate credentials regularly

### SSH Keys
- The initialization process respects existing SSH keys
- New SSH keys are generated only if none exist
- Private keys are set to mode 0600 automatically

### Package Verification
- All binary tools are downloaded from official GitHub releases
- Package signatures are verified where available
- APT repositories use GPG key verification

### Network Security
- Tools are downloaded over HTTPS
- APT repositories use signed packages
- CF-Community plugin repository uses HTTPS

## Integration with OCFP Workflows

### Post-Initialization Steps

After bastion initialization, typical next steps:

1. **Initialize Vault**:
```bash
ocfp vault init
```

2. **Bootstrap Infrastructure**:
```bash
ocfp bootstrap
```

3. **Configure Genesis Deployments**:
```bash
cd ~/deployments/vault
genesis init -k vault
```

4. **Verify CF Connectivity**:
```bash
cf login -a https://api.system.example.com
cf apps
```

5. **Test App-Autoscaler Plugin**:
```bash
cf autoscaling-apps
cf autoscaling-policy my-app
```

### Continuous Maintenance

Keep bastion tools updated:

```bash
# Update APT packages
sudo apt update && sudo apt upgrade -y

# Update CF CLI
sudo apt install --only-upgrade cf8-cli

# Update CF plugins
cf install-plugin -r CF-Community app-autoscaler-plugin -f

# Update Genesis tools
genesis update
```

## See Also

- [Bastion Tailscale](bastion-tailscale.md) for joining the bastion to a tailnet at first boot (PVE and other no-public-IP providers)
- [Security Groups](../networking/security-groups.md) for the 7 default security groups created during bootstrap
- [Networking Overview](../networking/README.md) for the full networking bootstrap flow
- [CF App-Autoscaler Documentation](https://github.com/cloudfoundry/app-autoscaler-cli-plugin)
- [Genesis Documentation](https://genesisproject.io)
- [BOSH CLI Documentation](https://bosh.io/docs/cli-v2/)
- [CloudFoundry CLI Documentation](https://docs.cloudfoundry.org/cf-cli/)
- [CredHub Documentation](https://docs.cloudfoundry.org/credhub/)
