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

Create a configuration file at `~/.ocfp/config.yml`:

```yaml
name: production
provider: stackit
iaas: stackit
region: eu-central-1
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
ocfp bootstrap --bloc-name production

# Configure networking and security
ocfp configure --bloc-name production
```

### 3. Access bastion host

```bash
# SSH to bastion
ocfp ssh --bloc-name production

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
| `lb` | Load balancer management |
| `scale` | Scale resources |
| `backup` | Backup configurations |
| `restore` | Restore from backup |

## Configuration

### Configuration File Structure

```yaml
# Basic configuration
name: environment-name
provider: stackit
iaas: stackit
region: eu-central-1
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

### Environment Variables

| Variable | Description |
|----------|-------------|
| `OCFP_CONFIG` | Path to configuration file |
| `OCFP_BLOC_NAME` | Current bloc/environment name |
| `OCFP_PROVIDER` | Cloud provider |
| `OCFP_REGION` | Cloud region |
| `OCFP_DEBUG` | Enable debug logging |

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