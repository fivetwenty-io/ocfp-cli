package config

import (
	"hash/fnv"
	"os"
	"strconv"
)

const (
	// InceptionVaultPortEnvVar overrides the workstation-local inception vault
	// listener port for a single invocation. It takes precedence over both the
	// per-bloc config field and the derived default.
	InceptionVaultPortEnvVar = "OCFP_VAULT_INCEPTION_PORT"

	// LegacyInceptionVaultPort is the historical fixed inception port. It is
	// still used when no bloc is named (single-bloc workstations) and by the
	// bastion-side inception vault, where only one bloc ever runs and Genesis
	// env files reference http://127.0.0.1:8234 directly.
	LegacyInceptionVaultPort = 8234

	// InceptionVaultPortRangeStart is the first port of the per-bloc range.
	// The range deliberately mirrors the legacy port (8234 -> 18234) so an
	// operator seeing 18xxx recognises it as an inception vault.
	InceptionVaultPortRangeStart = 18234

	// InceptionVaultPortRangeSize is the number of ports in the per-bloc range.
	InceptionVaultPortRangeSize = 1000

	maxTCPPort = 65535
)

// InceptionVaultPort returns the workstation-local inception vault listener
// port for a bloc.
//
// Concurrent `ocfp bootstrap` runs for different blocs share one workstation,
// so a fixed port makes the second run fail to bind and — worse — makes later
// commands for the losing bloc read and write the winning bloc's vault. The
// port is therefore scoped to the bloc, and derived deterministically so that
// every later command for the same bloc rediscovers it without shared state.
//
// Resolution order:
//
//  1. the OCFP_VAULT_INCEPTION_PORT environment variable
//
//  2. vault_inception_port for the bloc in ~/.ocfp/config.yml
//
//  3. a deterministic port derived from the bloc name
//
//  4. LegacyInceptionVaultPort when no bloc is named
func InceptionVaultPort(blocName string) int {
	port, ok := inceptionPortFromEnv()
	if ok {
		return port
	}

	port, ok = inceptionPortFromConfig(blocName)
	if ok {
		return port
	}

	return DeterministicInceptionVaultPort(blocName)
}

// DeterministicInceptionVaultPort derives a bloc's inception port from its name
// alone. It reads no environment and no config, so two processes — or two
// operators — always agree on the port for a given bloc.
func DeterministicInceptionVaultPort(blocName string) int {
	if blocName == "" {
		return LegacyInceptionVaultPort
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(blocName))

	return InceptionVaultPortRangeStart + int(hash.Sum32()%InceptionVaultPortRangeSize)
}

// inceptionPortFromEnv reads the env override, ignoring values that are not a
// usable TCP port.
func inceptionPortFromEnv() (int, bool) {
	raw := os.Getenv(InceptionVaultPortEnvVar)
	if raw == "" {
		return 0, false
	}

	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > maxTCPPort {
		return 0, false
	}

	return port, true
}

// inceptionPortFromConfig reads vault_inception_port for the named bloc from
// the config file. A missing file, unreadable file, unknown bloc, or unset
// field all fall through to the derived default.
func inceptionPortFromConfig(blocName string) (int, bool) {
	if blocName == "" {
		return 0, false
	}

	path := determineConfigPath("")
	if path == "" {
		return 0, false
	}

	var cfgFile ConfigFile

	err := loadFromFile(path, &cfgFile)
	if err != nil {
		return 0, false
	}

	bloc, ok := cfgFile.Blocs[blocName]
	if !ok || bloc == nil || bloc.VaultInceptionPort <= 0 || bloc.VaultInceptionPort > maxTCPPort {
		return 0, false
	}

	return bloc.VaultInceptionPort, true
}
