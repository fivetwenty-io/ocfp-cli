package keyfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// localTargetSuffix ends the name of the target safe local registers while it
// runs an inception vault. No bloc's own target ends this way, since those
// end in -inception or -mgmt.
const localTargetSuffix = "-local"

// LocalTargetName is the target safe local registers for the inception vault
// whose own target is vaultName, for as long as it runs that vault.
//
// safe local refuses to start under a name safe already has, and an older
// ocfp made the bloc's own target current. Running safe local under its own
// name lets the bloc's target stay in place across a restart, updated rather
// than deleted and made again, so a current target that names it is never
// cleared.
func LocalTargetName(vaultName string) string {
	return vaultName + localTargetSuffix
}

// TargetIsCurrent reports whether safe's current target is the target name,
// either by its name or by its URL. When ~/.saferc cannot be read or parsed
// it reports true, so a caller about to delete the target leaves it alone
// rather than guess. A missing ~/.saferc has no current target at all.
func TargetIsCurrent(name string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}

	data, err := os.ReadFile(filepath.Join(home, ".saferc")) // #nosec G304 -- safe's own config under the user's home
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}

	if err != nil {
		return true
	}

	var safeRC struct {
		Current string `yaml:"current"`
		Vaults  map[string]struct {
			URL string `yaml:"url"`
		} `yaml:"vaults"`
	}

	if yaml.Unmarshal(data, &safeRC) != nil {
		return true
	}

	current := strings.TrimSpace(safeRC.Current)

	switch current {
	case "":
		return false
	case name:
		return true
	}

	if _, isName := safeRC.Vaults[current]; isName {
		return false
	}

	target, ok := safeRC.Vaults[name]

	return ok && strings.TrimSuffix(target.URL, "/") == strings.TrimSuffix(current, "/")
}

// EnvWithoutSafeTarget is the environment with SAFE_TARGET removed. safe
// looks SAFE_TARGET up before it lists or deletes targets and fails when it
// names a target safe does not know, so the calls that manage the bloc's own
// targets run without it.
func EnvWithoutSafeTarget() []string {
	env := os.Environ()
	kept := env[:0:0]

	for _, kv := range env {
		if !strings.HasPrefix(kv, "SAFE_TARGET=") {
			kept = append(kept, kv)
		}
	}

	return kept
}
