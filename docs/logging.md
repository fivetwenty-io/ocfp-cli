# OCFP Logging Documentation

## Overview

OCFP uses structured JSON logging to capture detailed operational information for troubleshooting, auditing, and analysis. Logs are organized in a hierarchical directory structure based on bloc (environment) and command execution.

## Log Directory Structure

All OCFP logs are stored under `~/.ocfp/` with the following hierarchy:

```
~/.ocfp/
  ├── {bloc}/
  │   └── logs/
  │       ├── {command}/
  │       │   └── {timestamp}.log
  │       └── {command}/
  │           └── {subcommand}/
  │               └── {timestamp}.log
  └── logs/  # For commands without --bloc flag
      ├── {command}/
      │   └── {timestamp}.log
      └── {command}/
          └── {subcommand}/
              └── {timestamp}.log
```

### Path Components

- **{bloc}**: The bloc/environment name specified via `--bloc` flag
- **logs**: Static directory name containing all logs for a bloc
- **{command}**: The primary command being executed (e.g., `bootstrap`, `state`, `lb`)
- **{subcommand}**: The subcommand if applicable (e.g., `sync`, `ops`, `export`)
- **{timestamp}**: Timestamp in format `YYYYMMDD-HHMMSS` (e.g., `20250122-143022`)

## Examples

### Simple Commands

**With bloc:**
```bash
ocfp --bloc dev bootstrap
# Logs to: ~/.ocfp/dev/logs/bootstrap/20250122-143022.log
```

**Without bloc:**
```bash
ocfp teardown
# Logs to: ~/.ocfp/logs/teardown/20250122-143022.log
```

### Subcommands

**State sync with bloc:**
```bash
ocfp --bloc prod state sync
# Logs to: ~/.ocfp/prod/logs/state/sync/20250122-143022.log
```

**Load balancer operations:**
```bash
ocfp --bloc staging lb ops
# Logs to: ~/.ocfp/staging/logs/lb/ops/20250122-143022.log
```

**Vault management:**
```bash
ocfp --bloc dev vault populate
# Logs to: ~/.ocfp/dev/logs/vault/populate/20250122-143022.log
```

### Multiple Blocs

Each bloc maintains separate log directories:

```
~/.ocfp/
  ├── dev/
  │   └── logs/
  │       ├── bootstrap/
  │       ├── state/
  │       │   ├── sync/
  │       │   └── export/
  │       └── lb/
  │           └── ops/
  ├── staging/
  │   └── logs/
  │       └── ...
  └── prod/
      └── logs/
          └── ...
```

## Log Format

Logs are written in JSON format for structured parsing and analysis.

### Sample Log Entry

```json
{
  "timestamp": "2025-01-22T14:30:22.123Z",
  "level": "INFO",
  "msg": "Starting bootstrap",
  "bloc": "dev",
  "operation": "bootstrap",
  "request_id": "req-abc-123",
  "caller": "bootstrap/bootstrap.go:45"
}
```

### Log Fields

| Field | Description | Always Present |
|-------|-------------|----------------|
| `timestamp` | ISO 8601 timestamp | Yes |
| `level` | Log level (DEBUG, INFO, WARN, ERROR, FATAL) | Yes |
| `msg` | Human-readable message | Yes |
| `bloc` | Bloc/environment name | If `--bloc` specified |
| `operation` | Current operation context | When set via `WithOperation()` |
| `request_id` | Unique request identifier | When generated |
| `director_id` | BOSH director ID | When available |
| `caller` | Source code location | Yes |
| Additional fields | Command-specific context | Varies |

## Log Levels

OCFP supports the following log levels:

| Level | Usage | Enabled By |
|-------|-------|------------|
| **DEBUG** | Detailed diagnostic information | `--debug` or `--trace` |
| **INFO** | General informational messages | `--verbose`, `--debug`, `--trace` |
| **WARN** | Warning messages for unexpected but non-critical issues | Always |
| **ERROR** | Error messages for failures | Always |
| **FATAL** | Critical errors that cause program termination | Always |

### Enabling Debug Output

```bash
# Debug level
ocfp --debug --bloc dev bootstrap

# Trace level (most verbose)
ocfp --trace --bloc dev bootstrap

# Verbose (INFO and above)
ocfp --verbose --bloc dev state sync
```

## Log Management

### Disabling Logging

To disable file logging entirely:

```bash
# Via flag
ocfp --no-log --bloc dev bootstrap

# Via environment variable
export OCFP_NO_LOG=true
ocfp --bloc dev bootstrap
```

Note: This only disables file logging. Console output (stdout/stderr) is unaffected.

### Log Rotation

OCFP creates a new log file for each command invocation. Old logs are not automatically deleted.

To manage log file accumulation:

```bash
# Find logs older than 30 days
find ~/.ocfp -name "*.log" -mtime +30

# Delete logs older than 30 days
find ~/.ocfp -name "*.log" -mtime +30 -delete

# Archive old logs
find ~/.ocfp -name "*.log" -mtime +30 -exec gzip {} \;
```

### Analyzing Logs

#### Using jq for JSON parsing

```bash
# Extract all ERROR level messages
cat ~/.ocfp/dev/logs/bootstrap/20250122-143022.log | jq 'select(.level == "ERROR")'

# Show all messages from a specific operation
cat ~/.ocfp/dev/logs/state/sync/20250122-143022.log | jq 'select(.operation == "sync")'

# Count log entries by level
cat ~/.ocfp/dev/logs/bootstrap/20250122-143022.log | jq -r '.level' | sort | uniq -c

# Extract error messages with timestamps
cat ~/.ocfp/dev/logs/bootstrap/20250122-143022.log | \
  jq -r 'select(.level == "ERROR") | "\(.timestamp) \(.msg)"'
```

#### Grep for specific patterns

```bash
# Find all logs containing "failed"
grep -r "failed" ~/.ocfp/dev/logs/

# Search for specific resource ID across all logs
grep -r "instance-xyz-123" ~/.ocfp/prod/logs/
```

## Troubleshooting

### Log File Not Created

**Cause**: Parent directory doesn't exist or insufficient permissions.

**Solution**:
```bash
# Check directory permissions
ls -la ~/.ocfp/

# Recreate if necessary
mkdir -p ~/.ocfp
chmod 750 ~/.ocfp
```

### Cannot Find Recent Logs

**Check the correct path based on command structure:**

For `ocfp --bloc dev state sync`:
```bash
# Correct location
ls -lt ~/.ocfp/dev/logs/state/sync/

# Common mistake: looking in wrong location
ls -lt ~/.ocfp/logs/dev/state/  # Wrong!
```

### Logs Not Verbose Enough

Enable higher verbosity:
```bash
# Increase log level
ocfp --verbose --bloc dev bootstrap  # INFO and above
ocfp --debug --bloc dev bootstrap    # DEBUG and above
ocfp --trace --bloc dev bootstrap    # Maximum verbosity
```

## Best Practices

1. **Include bloc in commands**: Always use `--bloc` for environment-specific operations to maintain organized logs

2. **Use request IDs for tracing**: When debugging issues, track operations using request IDs across log files

3. **Regular log cleanup**: Implement log retention policies to prevent disk space issues

4. **Structured searching**: Use `jq` for JSON log parsing rather than plain text tools

5. **Debug carefully**: Enable debug/trace logging only when needed as it can generate large log files

6. **Preserve logs for failures**: Keep logs from failed operations for troubleshooting and root cause analysis

## Integration with External Tools

### Centralized Logging

To forward OCFP logs to a centralized logging system:

```bash
# Example: Forward to Elasticsearch
cat ~/.ocfp/dev/logs/bootstrap/*.log | \
  while read line; do
    curl -X POST "http://elasticsearch:9200/ocfp-logs/_doc" \
      -H "Content-Type: application/json" \
      -d "$line"
  done
```

### Log Monitoring

Monitor OCFP operations in real-time:

```bash
# Tail latest log file
tail -f ~/.ocfp/dev/logs/bootstrap/$(ls -t ~/.ocfp/dev/logs/bootstrap/ | head -1)

# Watch for errors across all logs
tail -f ~/.ocfp/dev/logs/*/*.log | jq -r 'select(.level == "ERROR")'
```

## Security Considerations

- Logs are created with mode `0600` (owner read/write only)
- Log directories are created with mode `0750` (owner rwx, group rx)
- Sensitive data (credentials, tokens) should never be logged
- Review logs before sharing to ensure no sensitive information is present

## Configuration Reference

### Environment Variables

| Variable | Effect | Default |
|----------|--------|---------|
| `OCFP_DEBUG` | Enable debug logging | `false` |
| `OCFP_VERBOSE` | Enable verbose logging | `false` |
| `OCFP_TRACE` | Enable trace logging | `false` |
| `OCFP_NO_LOG` | Disable file logging | `false` |

### Command Line Flags

| Flag | Effect |
|------|--------|
| `--debug` / `-d` | Enable debug logging |
| `--verbose` / `-v` | Enable verbose logging |
| `--trace` | Enable trace logging |
| `--no-log` | Disable file logging |

## See Also

- [README.md](../README.md) - Main documentation
- [Configuration Guide](configuration.md) - Configuration file structure
- [Troubleshooting Guide](troubleshooting.md) - Common issues and solutions
