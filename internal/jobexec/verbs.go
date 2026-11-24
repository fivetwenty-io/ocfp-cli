package jobexec

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// verb is one entry in the verb table.
type verb struct {
	// name is what a request's verb field must hold.
	name string

	// cancelBudget is how long the verb has to stop once a cancel arrives.
	// It must be more than zero, and the header reports it.
	cancelBudget time.Duration

	// run runs the verb against j. It returns the job's exit code, and an
	// error that matches ErrCanceled when it stopped at a safe point after a
	// cancel. It writes no header and no result.
	run func(ctx context.Context, j Job, hostname func() (string, error)) (exitCode int, err error)
}

// verbs is the table of verbs this host runs, filed by name.
var verbs = map[string]verb{
	verbNoop:        {name: verbNoop, cancelBudget: 5 * time.Second, run: runNoop},
	verbGenesisInfo: {name: verbGenesisInfo, cancelBudget: 10 * time.Second, run: runGenesisInfo},
}

// verbList quotes the table's verb names in order, for a message.
func verbList() string {
	names := slices.Sorted(maps.Keys(verbs))
	for i, n := range names {
		names[i] = fmt.Sprintf("%q", n)
	}

	return strings.Join(names, ", ")
}
