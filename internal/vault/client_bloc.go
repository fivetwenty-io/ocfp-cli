package vault

import (
	"fmt"
	"os"
)

// defaultVaultAddress is the address used when neither the environment nor the
// bloc's own safe target supplies one.
const defaultVaultAddress = "https://127.0.0.1:8200"

// InceptionTargetSuffix names a bloc's workstation-local inception vault target.
const InceptionTargetSuffix = "-inception"

// MgmtTargetSuffix names a bloc's management vault target, which owns the
// bloc's secrets once `ocfp vault migrate` has run.
const MgmtTargetSuffix = "-mgmt"

// BlocSafeTargetNames returns the safe target names that may hold a bloc's
// secrets, in resolution order: the inception vault first (it is authoritative
// until migration deletes it), then the management vault.
func BlocSafeTargetNames(blocName string) []string {
	if blocName == "" {
		return nil
	}

	return []string{blocName + InceptionTargetSuffix, blocName + MgmtTargetSuffix}
}

// NewClientForBloc creates a vault client bound to one bloc's own vault.
//
// It deliberately does not consult the `safe` global current target. That
// pointer is a single shared mutable owned by ~/.saferc: with several blocs
// bootstrapping at once, whichever bloc ran `safe target` last owns it, so a
// client derived from it writes one bloc's secrets into another bloc's vault.
// Resolution is by explicit environment first, then by this bloc's own target
// name — never by "whatever is current".
func NewClientForBloc(blocName string) (*Client, error) {
	cfg, err := resolveBlocVaultConfig(blocName)
	if err != nil {
		return nil, err
	}

	return NewClient(cfg)
}

// resolveBlocVaultConfig builds the vault client config for a bloc.
func resolveBlocVaultConfig(blocName string) (*Config, error) {
	if blocName == "" {
		return nil, ErrNoBlocForVaultTarget
	}

	address := os.Getenv("VAULT_ADDR")
	token := os.Getenv("VAULT_TOKEN")
	skipVerify := os.Getenv("VAULT_SKIP_VERIFY") == envValueTrue

	// An explicit address and token override everything: that is how a caller
	// pins a bloc to a specific vault (and how the bastion pins itself).
	if address == "" || token == "" {
		// A bloc with no target of its own is left token-less on purpose. The
		// client then fails with ErrTokenAuthRequiresVaultToken, which tells
		// the operator to set VAULT_ADDR/VAULT_TOKEN or run `safe target` —
		// far better than silently borrowing whichever vault is current.
		targetAddr, targetToken, targetSkip, err := readBlocSafeTarget(blocName)
		if err == nil {
			if address == "" {
				address = targetAddr
			}

			if token == "" {
				token = targetToken
			}

			skipVerify = skipVerify || targetSkip
		}
	}

	if address == "" {
		address = defaultVaultAddress
	}

	return &Config{
		Address:   address,
		Token:     token,
		Namespace: os.Getenv("VAULT_NAMESPACE"),
		AuthType:  getEnvOrDefault("VAULT_AUTH_TYPE", "token"),
		Username:  os.Getenv("VAULT_USERNAME"),
		Password:  os.Getenv("VAULT_PASSWORD"),
		RoleID:    os.Getenv("VAULT_ROLE_ID"),
		SecretID:  os.Getenv("VAULT_SECRET_ID"),
		CACert:    os.Getenv("VAULT_CACERT"),
		TLSSkip:   skipVerify,
	}, nil
}

// readBlocSafeTarget looks up the bloc's own targets in ~/.saferc by name and
// returns the first one that resolves.
func readBlocSafeTarget(blocName string) (string, string, bool, error) {
	var lastErr error

	for _, name := range BlocSafeTargetNames(blocName) {
		addr, token, skipVerify, err := readSafeConfigTarget(name)
		if err == nil {
			return addr, token, skipVerify, nil
		}

		lastErr = err
	}

	if lastErr == nil {
		lastErr = ErrNoBlocForVaultTarget
	}

	return "", "", false, fmt.Errorf("no vault target for bloc %q: %w", blocName, lastErr)
}
