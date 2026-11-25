package jobexec

import (
	"context"
	"errors"
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

// verbs is the table of verbs this host runs, filed by name. It starts
// empty, and each verb's own file fills it with one package-level variable
// that calls registerVerb, so nothing but those registrations builds it.
// Go initializes the table before any variable that registers into it, and
// every registration before any function runs.
var verbs = map[string]verb{}

// verbErrs holds what was wrong with each registration the table refused.
// While it holds anything, this host runs no verb.
var verbErrs []error

// registerVerb files v in table under its name and returns the name. A verb
// with no name, no run function, or no cancel budget, or one whose name the
// table already holds, is refused: the table keeps what it held, and the
// reason is added to errs.
func registerVerb(table map[string]verb, errs *[]error, v verb) string {
	var err error

	switch _, held := table[v.name]; {
	case v.name == "":
		err = errors.New("a verb registers with no name")
	case v.run == nil:
		err = fmt.Errorf("verb %s registers with no run function", quote(v.name))
	case v.cancelBudget <= 0:
		err = fmt.Errorf("verb %s registers with the cancel budget %s, want more than zero", quote(v.name), v.cancelBudget)
	case held:
		err = fmt.Errorf("verb %s is registered twice", quote(v.name))
	}

	if err != nil {
		*errs = append(*errs, err)

		return v.name
	}

	table[v.name] = v

	return v.name
}

// verbTableErr is the error of a table whose registrations errs refused, and
// nil when it refused none.
func verbTableErr(errs []error) error {
	err := errors.Join(errs...)
	if err != nil {
		return fmt.Errorf("the verb table refused a registration: %w", err)
	}

	return nil
}

// verbList quotes the table's verb names in order, for a message.
func verbList() string {
	names := slices.Sorted(maps.Keys(verbs))
	for i, n := range names {
		names[i] = fmt.Sprintf("%q", n)
	}

	return strings.Join(names, ", ")
}
