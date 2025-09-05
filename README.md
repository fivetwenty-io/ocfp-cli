# OCFP CLI - Go Implementation

A modern, high-performance command-line interface for the Open Cloud Foundry Platform (OCFP), rewritten in Go for improved speed, cross-platform compatibility, and maintainability.

## Features

- **Multi-Cloud Support**: Works with STACKIT, OpenStack, AWS, Azure, GCP, and VMware
- **Infrastructure Management**: Bootstrap, configure, and teardown cloud infrastructure
- **SSH/SCP/Rsync**: Secure access and file transfer to bastion hosts
- **Environment Management**: Switch between multiple deployment environments
- **High Performance**: 2x faster than the original Perl implementation
- **Cross-Platform**: Native binaries for Linux, macOS, and Windows
- **Plugin Architecture**: Extensible provider system for adding new cloud platforms

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/ocfp/ocfp-cli-go.git
cd ocfp-cli-go/go

# Build the binary
make build

# Install to system (optional)
make install
```

### Pre-built Binaries

Download the latest release from the [releases page](https://github.com/ocfp/ocfp-cli-go/releases).

### Using Homebrew (macOS/Linux)

```bash
brew tap ocfp/tools
brew install ocfp-cli
```

## Quick Start

### 1. Configure your environment

Create a configuration file at `~/.ocfp/config.yml` (provider comes from bloc config):

```yaml
name: production
provider: stackit
region: eu01  # For STACKIT, defaults to eu01 if omitted
project_id: your-project-id
auth_token: your-auth-token
network:
  name: ocfp-network
  cidr: 10.0.0.0/16
bastion:
  flavor: m1.small
  image: ubuntu-22.04
  keypair: bastion-key
```

### 2. Bootstrap infrastructure

```bash
# Bootstrap cloud infrastructure
ocfp bootstrap --bloc production

# Configure networking and security
ocfp configure --bloc production
```

### 3. Access bastion host

```bash
# SSH to bastion
ocfp ssh --bloc production

# Copy files
ocfp scp local-file.txt bastion:/tmp/

# Sync directories
ocfp rsync --archive /local/dir/ bastion:/remote/dir/
```

### 4. Environment management

```bash
# List environments
ocfp env list

# Switch environment
ocfp env set production

# Show environment details
ocfp env show

# Export environment variables
eval $(ocfp env export)
```

### Unicode vs ASCII Tables

The CLI renders rich Unicode box-drawn tables by default. If your terminal/font does not display these cleanly, enable ASCII-only tables globally with either of:

- CLI flag: `--ascii` (e.g., `ocfp --ascii bootstrap --bloc production --dry-run`)
- Environment variable: `OCFP_ASCII=true`

### Global Flags

| Flag | Shorthand | Description | Env Var | Default |
|------|-----------|-------------|--------|---------|
| `--config` | `-f` | Config file path | `OCFP_CONFIG` | – |
| `--bloc` | – | Bloc/environment name (key under `blocs:`) | `OCFP_BLOC_NAME` | – |
| `--debug` | `-d` | Enable debug output | `OCFP_DEBUG` | `false` |
| `--verbose` | `-v` | Enable verbose output | `OCFP_VERBOSE` | `false` |
| `--trace` | – | Enable trace-level debugging | `OCFP_TRACE` | `false` |
| `--no-log` | – | Disable logging to `~/.ocfp/logs/` | `OCFP_NO_LOG` | `false` |
| `--region` | – | Cloud region | `OCFP_REGION` | – |
| `--debug-lookup` | – | Print bastion lookup strategy matches | `OCFP_DEBUG_LOOKUP` | `false` |
| `--ascii` | – | Use ASCII-only tables in output | `OCFP_ASCII` | `false` |

## Commands

### Core Commands

| Command | Description |
|---------|-------------|
| `bootstrap` | Provision cloud infrastructure (network, subnets, security groups, bastion) |
| `configure` | Apply configuration to provisioned resources |
| `teardown` | Remove cloud infrastructure (with dependency awareness) |
| `ssh` | Connect to bastion host via SSH |
| `scp` | Copy files to/from bastion |
| `rsync` | Synchronize files with bastion |
| `env` | Manage deployment environments |

### Management Commands

| Command | Description |
|---------|-------------|
| `init` | Initialize OCFP components (PostgreSQL, CF, BOSH) |
| `test` | Run platform tests |
| `vault` | Manage secrets in Vault |
| `lb` | Load balancer management (see go/docs/cmds/lb.md) |
| `scale` | Scale resources |
| `backup` | Backup configurations |
| `restore` | Restore from backup |

## Configuration

### Configuration File Structure

```yaml
# Basic configuration (provider is read from bloc config)
name: environment-name
provider: stackit
region: eu01  # For STACKIT, defaults to eu01 if omitted
project_id: project-123
org_id: org-456
auth_token: ${STACKIT_AUTH_TOKEN}

# Network configuration
network:
  name: ocfp-network
  cidr: 10.0.0.0/16
  dns:
    - 8.8.8.8
    - 8.8.4.4

# Subnet defaults (see go/docs/networking/subnets.md)

- Non-STACKIT providers: If `subnets` are omitted, bootstrap derives two subnets named `mgmt` (public) and `ocf` (private) by splitting the bloc network into two equal subnets (e.g., `/20` → two `/21`). Bastion prefers `mgmt`.
- STACKIT: No real provider subnets are created. A single virtual subnet `ocfp-0` equals the bloc network CIDR and is treated as public for placement and IP assignment semantics. Bastion uses the network directly and records a dependency on `subnet.<bloc>-ocfp-0` in state.

Subnet strategies (STACKIT) (see go/docs/networking/subnets.md)

- `subnet_strategy: ocfp-triple`: Splits the bloc network into 4 equal CIDRs and uses the last three for virtual subnets `ocfp-0..2` (e.g., `/20` → `10.4.4.0/22`, `10.4.8.0/22`, `10.4.12.0/22`).
- Reserved IPs: For each virtual subnet, state outputs include service IPs and ranges used by load balancers and kits, e.g.:
  - `reserved_<bloc>-ocfp-0_bastion_ip`, `reserved_<bloc>-ocfp-0_bosh_ip`, `reserved_<bloc>-ocfp-0_vault_ip`
  - `reserved_<bloc>-ocfp-1_doomsday_ip`, `reserved_<bloc>-ocfp-2_ocfp_ui_ip`
  - Ranges: `reserved_<subnet>_available_a/b` and `reserved_<subnet>_reserved_a/b[/c/d]`
  - Offsets mirror the Perl defaults (bastion .3, bosh .4, vault .5, jumpbox .6, concourse .7, prometheus .8, shield .9 on ocfp-0, doomsday .9 on ocfp-1, ocfp_ui .9 on ocfp-2; ranges reserved 0-10,30-> and available 11-29).

### Load balancer management (see go/docs/cmds/lb.md)

- Add a backend by IP:
  - `ocfp lb add-service cf-router 10.0.1.10 --port 8080 --target-port 80`
- STACKIT reserved IP integration:
  - `ocfp lb add-service ops-https reserved:vault_ip`
  - `ocfp lb add-service doomsday-mgmt reserved:doomsday_ip:1` (uses ocfp-1)
  - Format: `reserved:<key>[:index]` where index selects ocfp subnet (default 0).

- Sync from config (lbs:) and reconcile pool members:
  - Define LBs in bloc config under `lbs:` and sync with: `ocfp lb sync --name <key> [--remove-unused]`
  - Example config:
    lbs:
      ops-https:
        protocol: tcp
        port: 443
        targets:
          - reserved:vault_ip
          - reserved:prometheus_ip
          - reserved:shield_ip
          - reserved:doomsday_ip:1
      router-80:
        protocol: http
        port: 80
        targets:
          - public-ip:router:0
          - public-ip:router:1
      cf-ssh:
        protocol: tcp
        port: 2222
        targets:
          - 10.4.0.50

- Target token formats:
  - `reserved:<key>[:index]` uses reserved IPs computed at bootstrap (STACKIT virtual subnets), e.g. `reserved:vault_ip`, `reserved:doomsday_ip:1`.
  - `public-ip:<job>[:index]` uses public IP resources discovered/created during bootstrap for job labels, e.g. `public-ip:router:0`, `public-ip:cf-ssh:0`, `public-ip:tcp-router:1`.

### Typed load balancer commands

- `ocfp lb ops`:
  - Ensures an external HTTPS LB for ops endpoints (default name `<bloc>-ops-https`, port 443).
  - Default behavior: if `lbs.ops-https` exists in config, pool members are reconciled from it (adds missing, with `--remove-unused` to prune).
  - Fallback: if no config, auto-add reserved IPs `vault_ip`, `prometheus_ip`, `shield_ip` from `ocfp-0`; optional `--with-doomsday` to add `doomsday_ip` from `ocfp-1`.
  - Flags: `--name`, `--port`, `--protocol` (default https), `--remove-unused`, `--with-doomsday`.

- `ocfp lb routers`:
  - Ensures external LBs for CF routers (HTTP/HTTPS) with names `<bloc>-router-80` and `<bloc>-router-443` by default.
  - Default behavior: if a matching key exists in `lbs:` (e.g., `router-80`), pool members are reconciled from it (with `--remove-unused`). Else, it falls back to public IPs labeled `job=router` from state.
  - Flags: `--name-prefix`, `--http`, `--https`, `--http-port`, `--https-port`, `--remove-unused`.

- `ocfp lb tcp-routers`:
  - Ensures an external TCP router LB (operator supplies `--port`).
  - Default behavior: if `lbs.<name>` exists, pool members are reconciled from it (with `--remove-unused`). Else, it falls back to public IPs labeled `job=tcp-router` from state.
  - Flags: `--name`, `--port` (default 1024), `--remove-unused`.

- `ocfp lb cf-ssh`:
  - Ensures an external LB for CF SSH (default name `<bloc>-cf-ssh`, port 2222).
  - Default behavior: if `lbs.<name>` exists, pool members are reconciled from it (with `--remove-unused`). Else, it falls back to public IPs labeled `job=cf-ssh` from state.
  - Flags: `--name`, `--port`, `--remove-unused`.

# Bastion configuration
bastion:
  flavor: m1.small
  image: ubuntu-22.04
  keypair: bastion-key
  
# Availability zones
azs:
  z1:
    zone: eu-central-1a
  z2:
    zone: eu-central-1b

# Blocs (deployment groups)
blocs:
  - name: management
    type: mgmt
    network:
      cidr: 10.0.1.0/24
  - name: deployment
    type: deploy
    network:
      cidr: 10.0.2.0/24
```

### Blobstore Policies (optional)

Object storage bucket policies (versioning/lifecycle) are disabled by default. To enable and tune them per bucket:

```yaml
blobstore:
  enable_policies: true
  bosh_blobstore:
    versioning: true
    noncurrent_days: 30
  cf_buildpacks:
    versioning: true
    noncurrent_days: 90
  cf_droplets:
    versioning: true
    noncurrent_days: 7
  cf_app_packages:
    versioning: false
    noncurrent_days: 30
```

Alternatively, set `OCFP_ENABLE_BUCKET_POLICIES=1` to enable policies from the environment. Applied settings are persisted to state on bootstrap.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `OCFP_CONFIG` | Path to configuration file |
| `OCFP_BLOC_NAME` | Current bloc/environment name |
| `OCFP_PROVIDER` | Cloud provider |
| `OCFP_REGION` | Cloud region |
| `OCFP_DEBUG` | Enable debug logging |
| `OCFP_VERBOSE` | Enable verbose logging |
| `OCFP_TRACE` | Enable trace-level logging |
| `OCFP_NO_LOG` | Disable file logging to `~/.ocfp/logs/` |
| `OCFP_DEBUG_LOOKUP` | Print bastion lookup strategy matches |
| `OCFP_ASCII` | Use ASCII-only tables (equivalent to `--ascii`) |

Note: For the STACKIT provider, if no region is specified via config, `--region`, or `OCFP_REGION`, OCFP defaults to `eu01`.

## Provider Support

### Currently Implemented

- **STACKIT**: Full support for compute, network, and storage operations
- **OpenStack**: (In development)
- **AWS**: (Planned)
- **Azure**: (Planned)
- **GCP**: (Planned)

### Adding a New Provider

1. Implement the `Provider` interface in `internal/cpi/`
2. Register the provider in `internal/cpi/registry.go`
3. Add provider-specific configuration handling
4. Write unit tests for the provider

## Development

### Prerequisites

- Go 1.21 or later
- Make
- Git

### Building

```bash
# Build for current platform
make build

# Build for all platforms
make build-all

# Run tests
make test

# Run linting
make lint

# Run security checks
make security
```

### Project Structure

```
go/
├── cmd/ocfp/           # Main application entry point
├── internal/
│   ├── bootstrap/      # Bootstrap orchestration
│   ├── cli/           # CLI framework and root command
│   ├── commands/      # Command implementations
│   ├── config/        # Configuration management
│   ├── cpi/           # Cloud Provider Interface
│   │   └── stackit/   # STACKIT provider implementation
│   ├── logger/        # Structured logging
│   ├── state/         # State management
│   └── version/       # Version information
├── pkg/               # Public APIs (future)
├── tests/             # Test suites
├── scripts/           # Build and deployment scripts
└── docs/              # Documentation
```

### Testing

```bash
# Run unit tests
go test ./...

# Run with coverage
go test -cover ./...

# Run integration tests
make test-integration

# Run security scan
make security
```

## CI/CD

The project uses GitHub Actions for continuous integration and deployment:

- **Lint**: Code quality checks with golangci-lint
- **Test**: Unit tests with coverage reporting
- **Security**: Vulnerability scanning with govulncheck and gosec
- **Build**: Cross-platform builds for Linux, macOS, and Windows
- **Release**: Automated releases with checksums

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Coding Standards

- Follow Go best practices and idioms
- Maintain test coverage above 80%
- Use structured logging with context
- Handle errors explicitly
- Document exported functions and types

## Migration from Perl CLI

The Go implementation maintains compatibility with existing configuration files and command syntax. To migrate:

1. Install the Go CLI alongside the Perl version
2. Test with your existing configuration files
3. Verify all workflows function correctly
4. Remove the Perl CLI once validated

### Breaking Changes

- None currently - full backward compatibility maintained

## Security

- All credentials are handled securely
- TLS/SSL verification enforced
- Sensitive data never logged
- Regular security scanning with govulncheck and gosec

### Reporting Security Issues

Please report security vulnerabilities to security@ocfp.io

## License

[Apache License 2.0](LICENSE)

## Support

- **Documentation**: [docs.ocfp.io](https://docs.ocfp.io)
- **Issues**: [GitHub Issues](https://github.com/ocfp/ocfp-cli-go/issues)
- **Discussions**: [GitHub Discussions](https://github.com/ocfp/ocfp-cli-go/discussions)
- **Slack**: [#ocfp on Cloud Foundry Slack](https://cloudfoundry.slack.com)

## Acknowledgments

- Original Perl implementation team
- Cloud Foundry Foundation
- All contributors and users

---

Built with ❤️ by the OCFP Team
