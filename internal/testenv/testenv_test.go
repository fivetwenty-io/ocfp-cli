package testenv_test

import (
	"bufio"
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/fivetwenty-io/ocfp-cli/internal/writeguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	testenv.Main(m)
}

// The tests here run in the tree that TestMain set up, so the variables
// already name it.
func TestMainGivesTheTestsATreeOfTheirOwn(t *testing.T) {
	home := os.Getenv("HOME")
	require.NotEmpty(t, home)

	tree := filepath.Dir(home)
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "OCFP_HOME"} {
		value := os.Getenv(name)
		assert.Equal(t, tree, filepath.Dir(value), "%s must sit in the tree", name)
		assert.DirExists(t, value, name)
	}

	assert.Equal(t, "1", os.Getenv(writeguard.EnvVar))
}

// fakeEnv sets every variable that Isolate reads or writes, so the test
// controls them and the cleanup restores them.
func fakeEnv(t *testing.T, values map[string]string) {
	t.Helper()

	for _, name := range []string{
		"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "OCFP_HOME",
		"GOENV", "GOCACHE", "GOMODCACHE", "GOPATH", writeguard.EnvVar, writeguard.RootsEnvVar,
	} {
		t.Setenv(name, values[name])
	}
}

func TestIsolateMovesEveryHomeVariableAndPinsTheGoCaches(t *testing.T) {
	realHome := t.TempDir()
	fakeEnv(t, map[string]string{
		"HOME":           realHome,
		"XDG_STATE_HOME": "/custom/state",
		"OCFP_HOME":      "/custom/ocfp",
	})

	tree := t.TempDir()
	goSettings := map[string]string{
		"GOENV":      filepath.Join(realHome, "Library", "go", "env"),
		"GOCACHE":    filepath.Join(realHome, "Library", "Caches", "go-build"),
		"GOMODCACHE": filepath.Join(realHome, "go", "pkg", "mod"),
		"GOPATH":     filepath.Join(realHome, "go"),
	}

	require.NoError(t, testenv.Isolate(tree, func() (map[string]string, error) { return goSettings, nil }))

	for name, dir := range map[string]string{
		"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state",
		"XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "OCFP_HOME": "ocfp",
	} {
		assert.Equal(t, filepath.Join(tree, dir), os.Getenv(name))
		assert.DirExists(t, filepath.Join(tree, dir))
	}

	for name, value := range goSettings {
		assert.Equal(t, value, os.Getenv(name), "%s must stay where it was", name)
	}

	assert.Equal(t, "1", os.Getenv(writeguard.EnvVar))

	roots := filepath.SplitList(os.Getenv(writeguard.RootsEnvVar))
	for _, want := range []string{
		"/custom/ocfp",
		filepath.Join("/custom/state", "ocfp"),
		filepath.Join(realHome, ".ocfp"),
		filepath.Join(realHome, ".local", "state", "ocfp"),
		filepath.Join(realHome, ".local", "share", "ocfp"),
		filepath.Join(realHome, ".config", "ocfp"),
	} {
		assert.Contains(t, roots, want, "the guard must keep guarding the directory the environment named")
	}
}

// A test binary that another test process started inherits a tree, not
// the real directories, so it keeps the roots it was given.
func TestIsolateKeepsTheRootsATestProcessPassedOn(t *testing.T) {
	fakeEnv(t, map[string]string{
		"HOME":                 t.TempDir(),
		writeguard.EnvVar:      "1",
		writeguard.RootsEnvVar: "/real/.ocfp",
	})

	require.NoError(t, testenv.Isolate(t.TempDir(), func() (map[string]string, error) { return nil, nil }))

	assert.Equal(t, "/real/.ocfp", os.Getenv(writeguard.RootsEnvVar))
}

func TestIsolateFailsWhenItCannotFindTheGoCaches(t *testing.T) {
	home := t.TempDir()
	fakeEnv(t, map[string]string{"HOME": home})

	err := testenv.Isolate(t.TempDir(), func() (map[string]string, error) { return nil, errors.New("go env failed") })
	require.Error(t, err)
	assert.Equal(t, home, os.Getenv("HOME"), "nothing moves when the caches can't be pinned")
}

func TestRunRemovesItsTreeAndReturnsTheCode(t *testing.T) {
	fakeEnv(t, map[string]string{"HOME": t.TempDir(), "GOCACHE": os.Getenv("GOCACHE")})

	var home string

	code := testenv.Run(func() int {
		home = os.Getenv("HOME")

		return 3
	})

	assert.Equal(t, 3, code)
	assert.NoDirExists(t, filepath.Dir(home), "Run removes the tree")
}

// pathPackages are the packages through which a test reaches ocfp's real
// directories: config resolves every one of them, and writeguard sits at
// every write. A package whose test binary links either must isolate its
// tests.
var pathPackages = []string{"/internal/config", "/internal/writeguard"}

// listedPackage is what go list says about one package or test binary.
type listedPackage struct {
	dir       string
	testFiles []string
	deps      []string
}

// Every package whose tests can reach ocfp's directories gives them a home
// of their own, so none can reach the developer's real ones.
func TestEveryPackageThatReachesOcfpPathsIsolatesItsTests(t *testing.T) {
	root, module := moduleRoot(t)
	packages := listPackages(t, root)

	require.Contains(t, packages, module+"/internal/config", "go list must find the config package")

	var missing, checked []string

	for importPath, pkg := range packages {
		if len(pkg.testFiles) == 0 || strings.HasSuffix(importPath, ".test") {
			continue
		}

		binary, ok := packages[importPath+".test"]
		if !ok || !slices.ContainsFunc(pathPackages, func(p string) bool { return slices.Contains(binary.deps, module+p) }) {
			continue
		}

		checked = append(checked, importPath)

		if !callsTestenv(t, pkg, module+"/internal/testenv") {
			missing = append(missing, importPath)
		}
	}

	slices.Sort(missing)
	assert.Empty(t, missing, "these packages' tests reach ocfp's directories, so their TestMain must call testenv.Main or testenv.Run")
	assert.Contains(t, checked, module+"/internal/config", "the check must cover the config package itself")
}

// moduleRoot returns the module's directory and path.
func moduleRoot(t *testing.T) (string, string) {
	t.Helper()

	out := goCommand(t, "", "list", "-m", "-f", "{{.Dir}}\t{{.Path}}")
	dir, module, ok := strings.Cut(strings.TrimSpace(out), "\t")
	require.True(t, ok, "go list -m printed %q", out)

	return dir, module
}

// listPackages lists every package in the module and every test binary,
// keyed by import path. A test binary's key ends in ".test", and its deps
// are everything the binary links.
func listPackages(t *testing.T, root string) map[string]listedPackage {
	t.Helper()

	out := goCommand(t, root, "list", "-e", "-test", "-f",
		"{{.ImportPath}}\t{{.Dir}}\t{{join .TestGoFiles \",\"}}\t{{join .XTestGoFiles \",\"}}\t{{join .Deps \",\"}}", "./...")

	packages := map[string]listedPackage{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)

	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		require.Len(t, fields, 5, "go list line %q", scanner.Text())

		importPath := fields[0]
		if strings.Contains(importPath, " ") {
			continue // a package recompiled for a test binary
		}

		var files []string

		for _, list := range fields[2:4] {
			for name := range strings.SplitSeq(list, ",") {
				if name != "" {
					files = append(files, filepath.Join(fields[1], name))
				}
			}
		}

		// A test binary links a package recompiled with the test files as
		// "path [path.test]", which is still that package.
		var deps []string
		for dep := range strings.SplitSeq(fields[4], ",") {
			path, _, _ := strings.Cut(dep, " ")
			deps = append(deps, path)
		}

		packages[importPath] = listedPackage{dir: fields[1], testFiles: files, deps: deps}
	}

	require.NoError(t, scanner.Err())

	return packages
}

// callsTestenv reports whether a TestMain among pkg's test files calls
// testenv.Main or testenv.Run.
func callsTestenv(t *testing.T, pkg listedPackage, testenvPath string) bool {
	t.Helper()

	fset := token.NewFileSet()

	for _, path := range pkg.testFiles {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		name := importName(file, testenvPath)
		if name == "" {
			continue
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}

			if callsSelector(fn.Body, name, "Main", "Run") {
				return true
			}
		}
	}

	return false
}

// importName returns the name file uses for the package at path, or ""
// when it doesn't import it.
func importName(file *ast.File, path string) string {
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil || value != path {
			continue
		}

		if spec.Name != nil {
			return spec.Name.Name
		}

		return filepath.Base(path)
	}

	return ""
}

// callsSelector reports whether body calls pkg.fn for one of fns.
func callsSelector(body ast.Node, pkg string, fns ...string) bool {
	found := false

	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return !found
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok {
			if ident, isIdent := sel.X.(*ast.Ident); isIdent && ident.Name == pkg && slices.Contains(fns, sel.Sel.Name) {
				found = true
			}
		}

		return !found
	})

	return found
}

// goCommand runs the go command in dir and returns its standard output.
func goCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), "go", args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	require.NoError(t, cmd.Run(), "go %s: %s", strings.Join(args, " "), stderr.String())

	return stdout.String()
}
