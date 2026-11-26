package commands

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/firstrun"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

// fakeFirstRunVault is an in-memory vault for the first-run commands. It
// answers a path that holds nothing the way Safe does.
type fakeFirstRunVault struct {
	mu       sync.Mutex
	data     map[string]map[string]interface{}
	writeErr error
}

func newFakeFirstRunVault() *fakeFirstRunVault {
	return &fakeFirstRunVault{data: map[string]map[string]interface{}{}}
}

func (f *fakeFirstRunVault) GetAll(path string) (map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	leaf, ok := f.data[path]
	if !ok {
		return nil, vault.ErrNoSecretFoundAtPath(path)
	}

	out := make(map[string]interface{}, len(leaf))
	for k, v := range leaf {
		out[k] = v
	}

	return out, nil
}

func (f *fakeFirstRunVault) SetMultiple(path string, values map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.writeErr != nil {
		return f.writeErr
	}

	if f.data[path] == nil {
		f.data[path] = map[string]interface{}{}
	}

	for k, v := range values {
		f.data[path][k] = v
	}

	return nil
}

func (f *fakeFirstRunVault) get(path, key string) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.data[path][key]
}

func (f *fakeFirstRunVault) put(path, key string, value interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.data[path] == nil {
		f.data[path] = map[string]interface{}{}
	}

	f.data[path][key] = value
}

const (
	firstRunCmdBloc   = "lab"
	firstRunCmdCode   = "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIO"
	firstRunCmdPublic = "cnsi1P1Ys5tWdhuQX5G8sBfbOi3+MbxNsLmQT2Rqf/E="
)

// TestPopulateFirstRunKeyShowsANewCodeOnlyToATerminal checks both paths
// of the populate step. A terminal sees the new code once with its
// sentence, and the code is marked shown. Any other output sees nothing
// about the code, and the code stays unshown for the bloc build's
// closing summary.
func TestPopulateFirstRunKeyShowsANewCodeOnlyToATerminal(t *testing.T) {
	t.Parallel()

	t.Run("terminal", func(t *testing.T) {
		t.Parallel()

		store := newFakeFirstRunVault()

		var out bytes.Buffer

		err := populateFirstRunKey(&out, store, firstRunCmdBloc, true)
		if err != nil {
			t.Fatal(err)
		}

		code, _ := store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey).(string)
		if code == "" || !strings.Contains(out.String(), code) {
			t.Fatalf("the terminal didn't see the new code: %q", out.String())
		}

		if strings.Count(out.String(), code) != 1 {
			t.Fatal("the code was shown more than once")
		}

		if !strings.Contains(out.String(), vault.FirstRunCodeSentence) {
			t.Fatal("the code was shown without its sentence")
		}

		if got := store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey); got != "true" {
			t.Fatalf("shown = %v, want true", got)
		}

		out.Reset()

		err = populateFirstRunKey(&out, store, firstRunCmdBloc, true)
		if err != nil {
			t.Fatal(err)
		}

		if out.Len() != 0 {
			t.Fatalf("a second populate printed %q", out.String())
		}
	})

	t.Run("not a terminal", func(t *testing.T) {
		t.Parallel()

		store := newFakeFirstRunVault()

		var out bytes.Buffer

		err := populateFirstRunKey(&out, store, firstRunCmdBloc, false)
		if err != nil {
			t.Fatal(err)
		}

		code, _ := store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey).(string)
		if code == "" {
			t.Fatal("no code was written")
		}

		norm, _ := firstrun.Normalize(code)
		if strings.Contains(out.String(), code) || strings.Contains(out.String(), norm) {
			t.Fatal("the code reached output that isn't a terminal")
		}

		if out.Len() != 0 {
			t.Fatalf("printed %q, want nothing", out.String())
		}

		if got := store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey); got != "false" {
			t.Fatalf("shown = %v, want false", got)
		}
	})
}

// TestPopulateFirstRunKeyRepairsWithoutShowing checks that repairing the
// public half from a stored code shows nothing, even on a terminal.
func TestPopulateFirstRunKeyRepairsWithoutShowing(t *testing.T) {
	t.Parallel()

	store := newFakeFirstRunVault()
	store.put(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey, firstRunCmdCode)
	store.put(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey, "false")

	var out bytes.Buffer

	err := populateFirstRunKey(&out, store, firstRunCmdBloc, true)
	if err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Fatalf("a repair printed %q", out.String())
	}

	if got := store.get(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunPublicKey); got != firstRunCmdPublic {
		t.Fatalf("the public half = %v", got)
	}
}

// TestPopulateFirstRunKeyNamesTheRotationWhenTheCodeIsGone checks the
// notices for a public half with no code and for a mismatch. Neither
// prints a code.
func TestPopulateFirstRunKeyNamesTheRotationWhenTheCodeIsGone(t *testing.T) {
	t.Parallel()

	for name, code := range map[string]string{"no code": "", "mismatch": "7777-7777-7777-7777-7777-7777"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := newFakeFirstRunVault()
			store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunPublicKey, firstRunCmdPublic)

			if code != "" {
				store.put(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey, code)
			}

			var out bytes.Buffer

			err := populateFirstRunKey(&out, store, firstRunCmdBloc, true)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(out.String(), "ocfp first-run rotate --bloc lab") {
				t.Fatalf("the notice %q doesn't name the rotation", out.String())
			}

			if code != "" && strings.Contains(out.String(), code) {
				t.Fatal("the notice printed the code")
			}
		})
	}
}

// TestPopulateFirstRunKeyFailsOnAWriteError checks that a vault that
// refuses the write fails the step.
func TestPopulateFirstRunKeyFailsOnAWriteError(t *testing.T) {
	t.Parallel()

	store := newFakeFirstRunVault()
	store.writeErr = errors.New("permission denied")

	var out bytes.Buffer

	err := populateFirstRunKey(&out, store, firstRunCmdBloc, true)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("got %v", err)
	}

	if out.Len() != 0 {
		t.Fatalf("printed %q", out.String())
	}
}

// TestPreviewFirstRunKeyNeverPrintsTheCode checks each dry-run line and
// that none of them carries the code.
func TestPreviewFirstRunKeyNeverPrintsTheCode(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		code, public string
		want         string
	}{
		"fresh":     {"", "", "Would make the bloc's first-run key"},
		"no public": {firstRunCmdCode, "", "Would write the first-run public half"},
		"no code":   {"", firstRunCmdPublic, "has no code beside it"},
		"in place":  {firstRunCmdCode, firstRunCmdPublic, "is in place"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := newFakeFirstRunVault()
			if tc.code != "" {
				store.put(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunCodeKey, tc.code)
			}

			if tc.public != "" {
				store.put(vault.FirstRunPublicPath(firstRunCmdBloc), vault.FirstRunPublicKey, tc.public)
			}

			var out bytes.Buffer

			err := previewFirstRunKey(&out, store, firstRunCmdBloc)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("printed %q, want %q", out.String(), tc.want)
			}

			if strings.Contains(out.String(), firstRunCmdCode) {
				t.Fatal("the preview printed the code")
			}

			if store.get(vault.FirstRunCodePath(firstRunCmdBloc), vault.FirstRunShownKey) != nil {
				t.Fatal("the preview wrote to the vault")
			}
		})
	}
}
