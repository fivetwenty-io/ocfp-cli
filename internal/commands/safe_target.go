package commands

import (
	"context"

	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// safeGetForBloc reads one vault path for a bloc, pinning `safe` to that
// bloc's own target with -T.
//
// A bare `safe get` resolves against the single current target recorded in
// ~/.saferc. That pointer is global to the workstation and every `safe target`
// and `safe local` invocation moves it, so with several blocs bootstrapping at
// once a bare read can silently return a sibling's secret — or nothing at all,
// because this bloc's path does not exist in the sibling's vault. Pinning the
// target per invocation removes the ordering dependency without mutating the
// operator's current target.
//
// Targets are tried in the same order as the API client: the inception vault
// owns the bloc's secrets until `ocfp vault migrate` runs, the mgmt vault
// after. With no bloc name there is nothing to pin to and the call keeps its
// historic unpinned form.
func safeGetForBloc(ctx context.Context, blocName, path string) ([]byte, error) {
	targets := vault.BlocSafeTargetNames(blocName)
	if len(targets) == 0 {
		return runner.Output(ctx, "safe", "get", path)
	}

	var lastErr error

	for _, target := range targets {
		output, err := runner.Output(ctx, "safe", "-T", target, "get", path)
		if err == nil {
			return output, nil
		}

		lastErr = err
	}

	return nil, lastErr
}
