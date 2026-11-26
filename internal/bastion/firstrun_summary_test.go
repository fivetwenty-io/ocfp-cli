package bastion

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
)

// summaryFirstRunVault is an in-memory vault reader for the closing
// summary's test. It answers a path that holds nothing the way Safe does.
type summaryFirstRunVault struct {
	mu       sync.Mutex
	data     map[string]map[string]interface{}
	reads    int
	writeErr error
}

func (f *summaryFirstRunVault) GetAll(path string) (map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reads++

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

func (f *summaryFirstRunVault) SetMultiple(path string, values map[string]interface{}) error {
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

func (f *summaryFirstRunVault) shown(bloc string) interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.data[vault.FirstRunCodePath(bloc)][vault.FirstRunShownKey]
}

const (
	summaryBloc = "lab"
	summaryCode = "AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIO"
)

func summaryVault(shown string) *summaryFirstRunVault {
	return &summaryFirstRunVault{data: map[string]map[string]interface{}{
		vault.FirstRunCodePath(summaryBloc): {vault.FirstRunCodeKey: summaryCode, vault.FirstRunShownKey: shown},
	}}
}

// opener hands the closing summary the fake, and counts the opens.
func opener(store vault.FirstRunVault, err error, opens *int, closes *int) firstRunVaultOpener {
	return func(bloc string) (vault.FirstRunVault, func(), error) {
		*opens++

		if bloc != summaryBloc {
			return nil, nil, errors.New("opened the wrong bloc's vault")
		}

		if err != nil {
			return nil, nil, err
		}

		return store, func() { *closes++ }, nil
	}
}

// TestClosingSummaryShowsAnUnshownCodeOnce checks the real run on a
// terminal: the code is shown once with its sentence, marked shown, and
// the vault is closed. A second close shows nothing.
func TestClosingSummaryShowsAnUnshownCodeOnce(t *testing.T) {
	t.Parallel()

	store := summaryVault("false")

	var (
		out           bytes.Buffer
		opens, closes int
	)

	showFirstRunCodeAtClose(&out, logger.Get(), summaryBloc, false, true, opener(store, nil, &opens, &closes))

	if strings.Count(out.String(), summaryCode) != 1 || !strings.Contains(out.String(), vault.FirstRunCodeSentence) {
		t.Fatalf("printed %q, want the code once with its sentence", out.String())
	}

	if store.shown(summaryBloc) != "true" {
		t.Fatal("the code wasn't marked shown")
	}

	if opens != 1 || closes != 1 {
		t.Fatalf("opened %d and closed %d times", opens, closes)
	}

	out.Reset()

	showFirstRunCodeAtClose(&out, logger.Get(), summaryBloc, false, true, opener(store, nil, &opens, &closes))

	if out.Len() != 0 {
		t.Fatalf("a second close printed %q", out.String())
	}
}

// TestClosingSummaryShowsNothingItShouldnt checks a dry run, output that
// isn't a terminal, and a code already shown. The first two never open
// the vault.
func TestClosingSummaryShowsNothingItShouldnt(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		dryRun, terminal bool
		shown            string
		wantOpens        int
	}{
		"dry run":        {dryRun: true, terminal: true, shown: "false", wantOpens: 0},
		"not a terminal": {dryRun: false, terminal: false, shown: "false", wantOpens: 0},
		"already shown":  {dryRun: false, terminal: true, shown: "true", wantOpens: 1},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := summaryVault(tc.shown)

			var (
				out           bytes.Buffer
				opens, closes int
			)

			showFirstRunCodeAtClose(&out, logger.Get(), summaryBloc, tc.dryRun, tc.terminal,
				opener(store, nil, &opens, &closes))

			if out.Len() != 0 {
				t.Fatalf("printed %q", out.String())
			}

			if store.shown(summaryBloc) != tc.shown {
				t.Fatal("shown changed")
			}

			if opens != tc.wantOpens || closes != tc.wantOpens {
				t.Fatalf("opened %d and closed %d times, want %d", opens, closes, tc.wantOpens)
			}
		})
	}
}

// TestClosingSummaryPointsAtShowWhenTheVaultIsOutOfReach checks that an
// unreachable vault or a failed read never fails the build, and that the
// operator is told how to see the code later.
func TestClosingSummaryPointsAtShowWhenTheVaultIsOutOfReach(t *testing.T) {
	t.Parallel()

	t.Run("open fails", func(t *testing.T) {
		t.Parallel()

		var (
			out           bytes.Buffer
			opens, closes int
		)

		showFirstRunCodeAtClose(&out, logger.Get(), summaryBloc, false, true,
			opener(nil, errors.New("no target"), &opens, &closes))

		if !strings.Contains(out.String(), "ocfp first-run show --bloc lab") {
			t.Fatalf("printed %q, want the show command", out.String())
		}
	})

	t.Run("stored code malformed", func(t *testing.T) {
		t.Parallel()

		store := summaryVault("false")
		store.data[vault.FirstRunCodePath(summaryBloc)][vault.FirstRunCodeKey] = "SECRET"

		var (
			out           bytes.Buffer
			opens, closes int
		)

		showFirstRunCodeAtClose(&out, logger.Get(), summaryBloc, false, true, opener(store, nil, &opens, &closes))

		if strings.Contains(out.String(), "SECRET") {
			t.Fatal("the summary printed the stored value")
		}

		if !strings.Contains(out.String(), "ocfp first-run show --bloc lab") {
			t.Fatalf("printed %q, want the show command", out.String())
		}

		if closes != 1 {
			t.Fatal("the vault wasn't closed")
		}
	})

	t.Run("marking fails", func(t *testing.T) {
		t.Parallel()

		store := summaryVault("false")
		store.writeErr = errors.New("sealed")

		var (
			out           bytes.Buffer
			opens, closes int
		)

		showFirstRunCodeAtClose(&out, logger.Get(), summaryBloc, false, true, opener(store, nil, &opens, &closes))

		if strings.Count(out.String(), summaryCode) != 1 {
			t.Fatalf("printed %q, want the code once", out.String())
		}
	})

	t.Run("no bloc", func(t *testing.T) {
		t.Parallel()

		var (
			out           bytes.Buffer
			opens, closes int
		)

		showFirstRunCodeAtClose(&out, logger.Get(), "", false, true, opener(nil, nil, &opens, &closes))

		if out.Len() != 0 || opens != 0 {
			t.Fatalf("printed %q after %d opens", out.String(), opens)
		}
	})
}
