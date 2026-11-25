package jobexec

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestEveryVerbDeclaresABudget(t *testing.T) {
	t.Parallel()

	if len(verbs) == 0 {
		t.Fatal("the verb table is empty")
	}

	for name, v := range verbs {
		if v.name != name {
			t.Errorf("verb %q is filed under %q", v.name, name)
		}

		if v.cancelBudget <= 0 {
			t.Errorf("verb %q has the cancel budget %s, want more than zero", name, v.cancelBudget)
		}

		if v.run == nil {
			t.Errorf("verb %q has no run function", name)
		}
	}
}

// registration is one registerVerb call in the package's source: the file
// it is in and the run function its verb names.
type registration struct {
	file string
	run  string
}

// sourceRegistrations parses the package's own source, without its tests,
// and returns each registerVerb call, the file that declares each function,
// and the initializer of the verbs variable.
func sourceRegistrations(t *testing.T) ([]registration, map[string]string, ast.Expr) {
	t.Helper()

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	var (
		regs  []registration
		funcs = map[string]string{}
		table ast.Expr
	)

	fset := token.NewFileSet()

	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Recv == nil {
					funcs[n.Name.Name] = path
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if name.Name == "verbs" && i < len(n.Values) {
						table = n.Values[i]
					}
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "registerVerb" {
					regs = append(regs, registration{file: path, run: runOf(n)})
				}
			}

			return true
		})
	}

	return regs, funcs, table
}

// runOf returns the name of the run function a registerVerb call's verb
// literal names, and "" when it names none.
func runOf(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.CompositeLit)
		if !ok {
			continue
		}

		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}

			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "run" {
				if val, ok := kv.Value.(*ast.Ident); ok {
					return val.Name
				}
			}
		}
	}

	return ""
}

// TestEachVerbRegistersFromItsOwnFile checks that every registration sits
// in the file that declares its verb's run function, that verbs.go holds
// none, that the table starts empty, and that the table holds exactly one
// verb for each registration and refused none, so nothing but the
// registrations builds it.
func TestEachVerbRegistersFromItsOwnFile(t *testing.T) {
	t.Parallel()

	regs, funcs, table := sourceRegistrations(t)

	if len(verbErrs) != 0 {
		t.Errorf("the table refused registrations: %v", verbErrs)
	}

	if len(regs) != len(verbs) {
		t.Errorf("the source holds %d registrations and the table %d verbs", len(regs), len(verbs))
	}

	for _, r := range regs {
		if r.run == "" {
			t.Errorf("a registration in %s names no run function", r.file)

			continue
		}

		if r.file == "verbs.go" {
			t.Errorf("verbs.go registers the verb that runs %s, which belongs in that verb's own file", r.run)
		}

		if want := funcs[r.run]; want != r.file {
			t.Errorf("the verb that runs %s is registered in %s, and %s declares it", r.run, r.file, want)
		}
	}

	lit, ok := table.(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 0 {
		t.Error("the verbs variable starts with entries of its own, want an empty table")
	}
}

// TestRegisterVerbRefusesADuplicateName checks that a second registration
// of a name is refused and recorded, and that the first stays in the table.
func TestRegisterVerbRefusesADuplicateName(t *testing.T) {
	t.Parallel()

	run := func(context.Context, Job, func() (string, error)) (int, error) { return 0, nil }
	other := func(context.Context, Job, func() (string, error)) (int, error) { return 1, nil }

	table := map[string]verb{}

	var errs []error

	if got := registerVerb(table, &errs, verb{name: "x", cancelBudget: time.Second, run: run}); got != "x" {
		t.Errorf("registerVerb = %q, want x", got)
	}

	registerVerb(table, &errs, verb{name: "x", cancelBudget: 2 * time.Second, run: other})

	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `"x"`) {
		t.Fatalf("the duplicate recorded %v, want one error naming x", errs)
	}

	if len(table) != 1 || table["x"].cancelBudget != time.Second {
		t.Errorf("the table holds %v after the duplicate, want the first registration alone", table)
	}
}

// TestRegisterVerbRefusesAnIncompleteVerb checks that a verb with no name,
// no run function, or no cancel budget is refused and recorded, and that
// the table stays empty.
func TestRegisterVerbRefusesAnIncompleteVerb(t *testing.T) {
	t.Parallel()

	run := func(context.Context, Job, func() (string, error)) (int, error) { return 0, nil }

	for name, v := range map[string]verb{
		"no name":   {cancelBudget: time.Second, run: run},
		"no run":    {name: "x", cancelBudget: time.Second},
		"no budget": {name: "x", run: run},
	} {
		table := map[string]verb{}

		var errs []error

		registerVerb(table, &errs, v)

		if len(errs) != 1 || len(table) != 0 {
			t.Errorf("%s: recorded %v and filed %v, want one error and an empty table", name, errs, table)
		}
	}
}

// TestARefusedRegistrationStopsEveryRun checks that a table that refused a
// registration has an error, and a whole one none, and that a verb process
// with a broken table refuses to start any verb before it reads a line.
func TestARefusedRegistrationStopsEveryRun(t *testing.T) {
	t.Parallel()

	if err := verbTableErr(nil); err != nil {
		t.Errorf("a whole table has the error %v", err)
	}

	broken := verbTableErr([]error{os.ErrInvalid})
	if !errors.Is(broken, os.ErrInvalid) {
		t.Fatalf("a broken table has the error %v, want one that wraps the refusal", broken)
	}

	cfg := verbConfig{version: "v1", verbs: verbs, broken: broken}

	_, _, err := readStart(nil, cfg)
	if err == nil || !strings.Contains(err.Error(), "verb table") {
		t.Errorf("readStart = %v, want the verb table's error", err)
	}
}

// TestTheHeaderReportsTheCancelBudget runs each verb in the table and reads
// the budget from its header, in whole milliseconds.
func TestTheHeaderReportsTheCancelBudget(t *testing.T) {
	t.Parallel()

	for name, v := range verbs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := goodRequest()
			req.Verb = name

			rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, req))

			// A verb that needs a host this test lacks, such as genesis.info
			// without Genesis, ends with a failure, and still writes its
			// header.
			err := rc.run(t)
			if err != nil && name == verbNoop {
				t.Fatalf("Run = %v", err)
			}

			recs := records(t, rc.stdout.Bytes())
			h := bodyOf[progress.Header](t, recs[0])

			if want := v.cancelBudget.Milliseconds(); h.CancelBudgetMs != want {
				t.Errorf("header budget = %d ms, want %d ms", h.CancelBudgetMs, want)
			}
		})
	}
}

// TestARefusedVerbHeaderNamesNoBudget checks that a header that names no
// verb carries no budget, and that the refusal lists the table's verbs.
func TestARefusedVerbHeaderNamesNoBudget(t *testing.T) {
	t.Parallel()

	req := goodRequest()
	req.Verb = "rm"

	rc := newRun(t, storeWith(t, liveRecord()), spoolRoot(t), signedRequest(t, req))

	r := wantRefusal(t, rc.run(t), progress.CodeVerbNotAllowed)
	if want := `this host runs only "genesis.info", "noop"`; !strings.Contains(r.Message, want) {
		t.Errorf("message %q does not hold %q", r.Message, want)
	}

	h := bodyOf[progress.Header](t, records(t, rc.stdout.Bytes())[0])
	if h.CancelBudgetMs != 0 {
		t.Errorf("a refusal header names the budget %d", h.CancelBudgetMs)
	}
}
