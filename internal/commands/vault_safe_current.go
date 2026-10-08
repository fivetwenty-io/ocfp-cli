package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// safeCurrentTargetSteps read and set safe's current target, the one a bare
// safe command uses. Starting a vault moves it, because both 'safe local'
// and 'safe target <name> <url>' make the target they register current.
type safeCurrentTargetSteps struct {
	// current returns the name of safe's current target, or "" when no
	// target is current.
	current func(ctx context.Context) (string, error)
	// setCurrent makes the named target, which must exist, current.
	setCurrent func(ctx context.Context, name string) error
}

// newSafeCurrentTargetSteps wires the real reads and writes of safe's
// current target through the safe binary the prerequisite check validated.
func newSafeCurrentTargetSteps(safePath string) safeCurrentTargetSteps {
	return safeCurrentTargetSteps{
		current: func(ctx context.Context) (string, error) {
			return readSafeCurrentTarget(ctx, safePath)
		},
		setCurrent: func(ctx context.Context, name string) error {
			return setSafeCurrentTarget(ctx, safePath, name)
		},
	}
}

// safeTargetEnv overrides safe's current target for a single command. The
// commands that read and set the current target run without it, so they see
// and change the one in ~/.saferc.
const safeTargetEnv = "SAFE_TARGET"

// readSafeCurrentTarget asks safe for its current target with 'safe target
// --json', which prints the current target only and an empty name when no
// target is current. 'safe targets --json' lists every target but does not
// say which one is current.
func readSafeCurrentTarget(ctx context.Context, safePath string) (string, error) {
	out, err := vaultOps.run(ctx, cleanupCommand{
		name: safePath, args: []string{"target", "--json"}, withoutEnv: []string{safeTargetEnv},
	})
	if err != nil {
		return "", fmt.Errorf("safe could not report its current target: %w", err)
	}

	var current struct {
		Name string `json:"name"`
	}

	err = json.Unmarshal(out, &current)
	if err != nil {
		return "", fmt.Errorf("safe reported its current target in a form ocfp cannot read: %w", err)
	}

	return strings.TrimSpace(current.Name), nil
}

// setSafeCurrentTarget makes an existing target current with 'safe target
// <name>', which changes nothing else about that target.
func setSafeCurrentTarget(ctx context.Context, safePath, name string) error {
	_, err := vaultOps.run(ctx, cleanupCommand{
		name: safePath, args: []string{"target", name}, withoutEnv: []string{safeTargetEnv},
	})
	if err != nil {
		return fmt.Errorf("safe could not make %s its current target again: %w", name, err)
	}

	return nil
}

// earlierSafeTarget is safe's current target as it stood before a run moved
// it. known is false when safe could not say, and then err says why.
type earlierSafeTarget struct {
	name  string
	known bool
	err   error
}

// rememberSafeCurrentTarget reads safe's current target before anything in
// the run can move it, so that putBackSafeCurrentTarget can restore it.
func rememberSafeCurrentTarget(ctx context.Context, steps safeCurrentTargetSteps) earlierSafeTarget {
	if steps.current == nil {
		return earlierSafeTarget{}
	}

	name, err := steps.current(ctx)
	if err != nil {
		return earlierSafeTarget{err: err}
	}

	return earlierSafeTarget{name: name, known: true}
}

// putBackSafeCurrentTarget makes the target that was current before the run
// current again. Starting the bloc's vault registers the bloc's target and
// makes it current, through 'safe local' or 'safe target <name> <url>', and
// an operator's own target should not change under them because a vault
// came back. When no target was current before, or the bloc's own target
// was, the bloc's target stays current, as it did before ocfp put targets
// back, and when none was, the run says so. A target that cannot be put back is only a warning, because the run
// has already done what it was asked to, and the vault is up.
func putBackSafeCurrentTarget(ctx context.Context, steps safeCurrentTargetSteps, earlier earlierSafeTarget,
	vaultName string, log *zap.SugaredLogger,
) {
	if !earlier.known {
		if earlier.err != nil {
			log.Warnw("ocfp could not tell which safe target was current before the run, so "+vaultName+
				" may now be the current target; run 'safe target <name>' to choose another",
				"error", earlier.err)
		}

		return
	}

	if earlier.name == "" {
		log.Infow("No safe target was current before the run, so " + vaultName +
			" is now safe's current target; run 'safe target <name>' to choose another")

		return
	}

	if earlier.name == vaultName || steps.setCurrent == nil {
		return
	}

	now, err := steps.current(ctx)
	if err == nil && now == earlier.name {
		return
	}

	err = steps.setCurrent(ctx, earlier.name)
	if err != nil {
		log.Warnw("ocfp could not make "+earlier.name+" safe's current target again, so "+vaultName+
			" may now be the current target; run 'safe target "+earlier.name+"' to put it back",
			"error", err)

		return
	}

	log.Debugw("Put back safe's earlier current target", "target", earlier.name)
}
