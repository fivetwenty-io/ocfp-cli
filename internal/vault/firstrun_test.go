package vault

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
)

// firstRunFakeVault is an in-memory vault that answers GetAll the way
// Safe does, with ErrSecretNotFound for a path that holds nothing, and
// records every write.
type firstRunFakeVault struct {
	mu       sync.Mutex
	data     map[string]map[string]interface{}
	writes   []string
	readErr  map[string]error
	writeErr map[string]error
}

func newFirstRunFakeVault() *firstRunFakeVault {
	return &firstRunFakeVault{
		data:     map[string]map[string]interface{}{},
		readErr:  map[string]error{},
		writeErr: map[string]error{},
	}
}

func (f *firstRunFakeVault) GetAll(path string) (map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.readErr[path]; err != nil {
		return nil, err
	}

	leaf, ok := f.data[path]
	if !ok {
		return nil, ErrNoSecretFoundAtPath(path)
	}

	out := make(map[string]interface{}, len(leaf))
	for k, v := range leaf {
		out[k] = v
	}

	return out, nil
}

func (f *firstRunFakeVault) SetMultiple(path string, values map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.writeErr[path]; err != nil {
		return err
	}

	leaf, ok := f.data[path]
	if !ok {
		leaf = map[string]interface{}{}
		f.data[path] = leaf
	}

	for k, v := range values {
		leaf[k] = v
		f.writes = append(f.writes, path+":"+k)
	}

	return nil
}

func (f *firstRunFakeVault) get(path, key string) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.data[path][key]
}

func (f *firstRunFakeVault) put(path, key string, value interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.data[path] == nil {
		f.data[path] = map[string]interface{}{}
	}

	f.data[path][key] = value
}

func (f *firstRunFakeVault) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.writes)
}

const (
	firstRunTestBloc = "lab"
	vectorCode       = "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIO"
	vectorPublic     = "cnsi1P1Ys5tWdhuQX5G8sBfbOi3+MbxNsLmQT2Rqf/E="
)

func TestFirstRunPaths(t *testing.T) {
	t.Parallel()

	if got := FirstRunPublicPath("lab"); got != "secret/config/lab/mgmt/first-run" {
		t.Fatalf("FirstRunPublicPath = %q", got)
	}

	if got := FirstRunCodePath("lab"); got != "secret/lab-operator/first-run" {
		t.Fatalf("FirstRunCodePath = %q", got)
	}
}

// TestEnsureFirstRunKeyWritesBothLeavesOnce checks a fresh bloc: the code
// leaf gets a formatted code with shown false, the public leaf gets the
// code's public half, and a second run changes nothing.
func TestEnsureFirstRunKeyWritesBothLeavesOnce(t *testing.T) {
	t.Parallel()

	fake := newFirstRunFakeVault()

	out, err := EnsureFirstRunKey(fake, firstRunTestBloc)
	if err != nil {
		t.Fatal(err)
	}

	if !out.WroteCode || !out.WrotePublic {
		t.Fatalf("a fresh bloc wrote code=%v public=%v, want both", out.WroteCode, out.WrotePublic)
	}

	code, _ := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey).(string)

	formatted, err := firstrun.Format(code)
	if err != nil || formatted != code {
		t.Fatalf("the stored code is not in its formatted form")
	}

	if out.Code != code {
		t.Fatal("the outcome's code is not the stored code")
	}

	if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey); got != "false" {
		t.Fatalf("shown = %v, want false", got)
	}

	want, _ := firstrun.PublicFor(code)
	if got := fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey); got != want {
		t.Fatalf("the public leaf = %v, want the code's public half", got)
	}

	writes := fake.writeCount()

	again, err := EnsureFirstRunKey(fake, firstRunTestBloc)
	if err != nil {
		t.Fatal(err)
	}

	if again.WroteCode || again.WrotePublic || again.Mismatch || again.CodeMissing {
		t.Fatalf("a second run reported %+v, want no change", again)
	}

	if fake.writeCount() != writes {
		t.Fatal("a second run wrote to the vault")
	}

	if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey); got != code {
		t.Fatal("a second run changed the code")
	}
}

// TestEnsureFirstRunKeyRepairsThePublicHalfFromTheCode checks that a
// missing public leaf beside a present code is derived from that code,
// and that the code leaf and the public leaf's other keys are untouched.
func TestEnsureFirstRunKeyRepairsThePublicHalfFromTheCode(t *testing.T) {
	t.Parallel()

	fake := newFirstRunFakeVault()
	fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey, vectorCode)
	fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey, "true")
	fake.put(FirstRunPublicPath(firstRunTestBloc), FirstRunLinkKey, "https://example.test/#/setup?code=")

	out, err := EnsureFirstRunKey(fake, firstRunTestBloc)
	if err != nil {
		t.Fatal(err)
	}

	if out.WroteCode || !out.WrotePublic {
		t.Fatalf("wrote code=%v public=%v, want only the public half", out.WroteCode, out.WrotePublic)
	}

	if got := fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey); got != vectorPublic {
		t.Fatalf("the repaired public half = %v, want the vector's", got)
	}

	if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey); got != vectorCode {
		t.Fatal("the repair changed the code")
	}

	if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey); got != "true" {
		t.Fatal("the repair changed shown")
	}

	if got := fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunLinkKey); got == nil {
		t.Fatal("the repair dropped the link")
	}

	for _, w := range fake.writes {
		if w != FirstRunPublicPath(firstRunTestBloc)+":"+FirstRunPublicKey {
			t.Fatalf("the repair wrote %s", w)
		}
	}
}

// TestEnsureFirstRunKeyNeverMintsBesideAPublicHalf checks the other
// one-leaf case: a public half with no code can't be repaired, because
// the code can't be derived from it, so nothing is written and the
// outcome says the code is missing.
func TestEnsureFirstRunKeyNeverMintsBesideAPublicHalf(t *testing.T) {
	t.Parallel()

	fake := newFirstRunFakeVault()
	fake.put(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey, vectorPublic)

	out, err := EnsureFirstRunKey(fake, firstRunTestBloc)
	if err != nil {
		t.Fatal(err)
	}

	if out.WroteCode || out.WrotePublic || !out.CodeMissing {
		t.Fatalf("outcome %+v, want CodeMissing and no writes", out)
	}

	if fake.writeCount() != 0 {
		t.Fatalf("wrote %v", fake.writes)
	}

	if out.Code != "" {
		t.Fatal("the outcome holds a code")
	}
}

// TestEnsureFirstRunKeyWritesTheCodeBesideAnEmptyPublicLeaf checks that
// a public leaf holding only the deploy's address keys counts as having
// no public half.
func TestEnsureFirstRunKeyWritesTheCodeBesideAnEmptyPublicLeaf(t *testing.T) {
	t.Parallel()

	fake := newFirstRunFakeVault()
	fake.put(FirstRunPublicPath(firstRunTestBloc), FirstRunURLKey, "https://example.test")

	out, err := EnsureFirstRunKey(fake, firstRunTestBloc)
	if err != nil {
		t.Fatal(err)
	}

	if !out.WroteCode || !out.WrotePublic {
		t.Fatalf("outcome %+v, want both written", out)
	}

	if got := fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunURLKey); got != "https://example.test" {
		t.Fatal("the write dropped the url")
	}
}

// TestEnsureFirstRunKeyReportsAMismatch checks that two present leaves
// that don't belong together are reported and left alone.
func TestEnsureFirstRunKeyReportsAMismatch(t *testing.T) {
	t.Parallel()

	fake := newFirstRunFakeVault()
	fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey, "7777-7777-7777-7777-7777-7777")
	fake.put(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey, vectorPublic)

	out, err := EnsureFirstRunKey(fake, firstRunTestBloc)
	if err != nil {
		t.Fatal(err)
	}

	if !out.Mismatch || out.WroteCode || out.WrotePublic {
		t.Fatalf("outcome %+v, want a mismatch and no writes", out)
	}

	if fake.writeCount() != 0 {
		t.Fatal("a mismatch was overwritten")
	}
}

// TestEnsureFirstRunKeyRefusesAStoredMalformedCode checks that a code
// leaf holding something that isn't a code is refused and never
// overwritten, and that the refusal doesn't echo the stored value.
func TestEnsureFirstRunKeyRefusesAStoredMalformedCode(t *testing.T) {
	t.Parallel()

	for name, stored := range map[string]interface{}{
		"short":     "SECRET-VALUE",
		"not text":  42,
		"empty":     "",
		"a boolean": true,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFirstRunFakeVault()
			fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey, stored)

			_, err := EnsureFirstRunKey(fake, firstRunTestBloc)
			if firstrun.CodeOf(err) != firstrun.CodeMalformed {
				t.Fatalf("got %v, want %s", err, firstrun.CodeMalformed)
			}

			if s, ok := stored.(string); ok && s != "" && strings.Contains(err.Error(), s) {
				t.Fatal("the refusal echoes the stored code")
			}

			if fake.writeCount() != 0 {
				t.Fatal("a malformed code was overwritten")
			}
		})
	}
}

// TestEnsureFirstRunKeyStopsOnReadAndWriteErrors checks that an
// unreachable vault is never taken for an empty one, and that a failed
// code write leaves no public half that the next run can't repair.
func TestEnsureFirstRunKeyStopsOnReadAndWriteErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("permission denied")

	t.Run("code read", func(t *testing.T) {
		t.Parallel()

		fake := newFirstRunFakeVault()
		fake.readErr[FirstRunCodePath(firstRunTestBloc)] = boom

		_, err := EnsureFirstRunKey(fake, firstRunTestBloc)
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}

		if fake.writeCount() != 0 {
			t.Fatal("wrote after a failed read")
		}
	})

	t.Run("public read", func(t *testing.T) {
		t.Parallel()

		fake := newFirstRunFakeVault()
		fake.readErr[FirstRunPublicPath(firstRunTestBloc)] = boom

		_, err := EnsureFirstRunKey(fake, firstRunTestBloc)
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}

		if fake.writeCount() != 0 {
			t.Fatal("wrote after a failed read")
		}
	})

	t.Run("code write", func(t *testing.T) {
		t.Parallel()

		fake := newFirstRunFakeVault()
		fake.writeErr[FirstRunCodePath(firstRunTestBloc)] = boom

		_, err := EnsureFirstRunKey(fake, firstRunTestBloc)
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}

		if fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey) != nil {
			t.Fatal("a public half was written with no code beside it")
		}
	})

	t.Run("public write", func(t *testing.T) {
		t.Parallel()

		fake := newFirstRunFakeVault()
		fake.writeErr[FirstRunPublicPath(firstRunTestBloc)] = boom

		out, err := EnsureFirstRunKey(fake, firstRunTestBloc)
		if !errors.Is(err, boom) {
			t.Fatalf("got %v", err)
		}

		if !out.WroteCode {
			t.Fatal("the outcome hides the code it wrote")
		}

		delete(fake.writeErr, FirstRunPublicPath(firstRunTestBloc))

		again, err := EnsureFirstRunKey(fake, firstRunTestBloc)
		if err != nil || again.WroteCode || !again.WrotePublic {
			t.Fatalf("the next run gave %+v, %v, want only the public half", again, err)
		}
	})
}

func TestFirstRunRefusesABadBlocName(t *testing.T) {
	t.Parallel()

	for _, bloc := range []string{"", "a/b", "*", "a+b", " lab", "lab\n", "..", "a b"} {
		fake := newFirstRunFakeVault()

		_, err := EnsureFirstRunKey(fake, bloc)
		if !errors.Is(err, ErrFirstRunBlocInvalid) {
			t.Fatalf("bloc %q gave %v", bloc, err)
		}

		_, err = ReadFirstRunState(fake, bloc)
		if !errors.Is(err, ErrFirstRunBlocInvalid) {
			t.Fatalf("bloc %q gave %v on read", bloc, err)
		}
	}

	if _, err := EnsureFirstRunKey(nil, "lab"); !errors.Is(err, ErrFirstRunNoVault) {
		t.Fatalf("a nil vault gave %v", err)
	}
}

// TestReadFirstRunStateReadsShownAsTextOrBoolean checks that shown reads
// the same whether safe stored it as text or as a JSON boolean.
func TestReadFirstRunStateReadsShownAsTextOrBoolean(t *testing.T) {
	t.Parallel()

	for stored, want := range map[interface{}]bool{"true": true, "false": false, true: true, false: false, "": false} {
		fake := newFirstRunFakeVault()
		fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey, vectorCode)
		fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey, stored)

		state, err := ReadFirstRunState(fake, firstRunTestBloc)
		if err != nil {
			t.Fatal(err)
		}

		if state.Shown != want {
			t.Fatalf("shown %v read as %v", stored, state.Shown)
		}
	}
}

// TestShowFirstRunCodeOnce checks the closing summary's rule: a code not
// yet shown is printed with its sentence and marked shown, and nothing is
// printed when it was shown, when output isn't a terminal, or when the
// bloc has no code.
func TestShowFirstRunCodeOnce(t *testing.T) {
	t.Parallel()

	seed := func(shown string) *firstRunFakeVault {
		fake := newFirstRunFakeVault()
		fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey, vectorCode)
		fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey, shown)

		return fake
	}

	t.Run("not shown, terminal", func(t *testing.T) {
		t.Parallel()

		fake := seed("false")

		var out bytes.Buffer

		shown, err := ShowFirstRunCodeOnce(&out, fake, firstRunTestBloc, true)
		if err != nil || !shown {
			t.Fatalf("shown=%v err=%v", shown, err)
		}

		if !strings.Contains(out.String(), vectorCode) || !strings.Contains(out.String(), FirstRunCodeSentence) {
			t.Fatalf("output %q lacks the code or its sentence", out.String())
		}

		if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey); got != "true" {
			t.Fatalf("shown = %v, want true", got)
		}

		if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey); got != vectorCode {
			t.Fatal("showing changed the code")
		}
	})

	for name, tc := range map[string]struct {
		shown    string
		terminal bool
	}{
		"already shown": {"true", true},
		"not terminal":  {"false", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := seed(tc.shown)

			var out bytes.Buffer

			shown, err := ShowFirstRunCodeOnce(&out, fake, firstRunTestBloc, tc.terminal)
			if err != nil || shown {
				t.Fatalf("shown=%v err=%v", shown, err)
			}

			if out.Len() != 0 {
				t.Fatalf("printed %q", out.String())
			}

			if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey); got != tc.shown {
				t.Fatal("shown changed")
			}
		})
	}

	t.Run("no code", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer

		shown, err := ShowFirstRunCodeOnce(&out, newFirstRunFakeVault(), firstRunTestBloc, true)
		if err != nil || shown || out.Len() != 0 {
			t.Fatalf("shown=%v err=%v out=%q", shown, err, out.String())
		}
	})

	t.Run("marking fails after printing", func(t *testing.T) {
		t.Parallel()

		fake := seed("false")
		fake.writeErr[FirstRunCodePath(firstRunTestBloc)] = errors.New("sealed")

		var out bytes.Buffer

		shown, err := ShowFirstRunCodeOnce(&out, fake, firstRunTestBloc, true)
		if !shown || !errors.Is(err, ErrFirstRunMarkShown) {
			t.Fatalf("shown=%v err=%v, want shown and a marking error", shown, err)
		}
	})
}

// TestRotateFirstRunKeyOverwritesBothLeaves checks that rotation makes a
// new code, overwrites both leaves, keeps the public leaf's other keys,
// and records shown as asked.
func TestRotateFirstRunKeyOverwritesBothLeaves(t *testing.T) {
	t.Parallel()

	for _, shown := range []bool{true, false} {
		t.Run(fmt.Sprint("shown ", shown), func(t *testing.T) {
			t.Parallel()

			fake := newFirstRunFakeVault()
			fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey, vectorCode)
			fake.put(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey, "true")
			fake.put(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey, vectorPublic)
			fake.put(FirstRunPublicPath(firstRunTestBloc), FirstRunLinkKey, "https://example.test/#/setup?code=")

			code, err := RotateFirstRunKey(fake, firstRunTestBloc, shown)
			if err != nil {
				t.Fatal(err)
			}

			if code == vectorCode {
				t.Fatal("rotation kept the old code")
			}

			if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunCodeKey); got != code {
				t.Fatal("the stored code is not the new code")
			}

			want, _ := firstrun.PublicFor(code)
			if got := fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunPublicKey); got != want {
				t.Fatal("the public half is not the new code's")
			}

			if got := fake.get(FirstRunCodePath(firstRunTestBloc), FirstRunShownKey); got != fmt.Sprint(shown) {
				t.Fatalf("shown = %v, want %v", got, shown)
			}

			if fake.get(FirstRunPublicPath(firstRunTestBloc), FirstRunLinkKey) == nil {
				t.Fatal("rotation dropped the link")
			}
		})
	}
}
