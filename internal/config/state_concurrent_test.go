package config_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSaveBlocKeys_ConcurrentBlocsDoNotLoseUpdates is the regression guard for
// the last shared mutable in the bootstrap path: state.yml is one file for
// every bloc, and SaveBlocKeys is a load-modify-save. Six bootstraps running at
// once interleave inside that window, so a bloc's SSH keys are silently
// dropped and every later command for it comes up keyless.
func TestSaveBlocKeys_ConcurrentBlocsDoNotLoseUpdates(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	const blocCount = 8

	var wg sync.WaitGroup

	errs := make([]error, blocCount)

	for i := range blocCount {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			bloc := "ocfp-lab-" + strconv.Itoa(idx)
			errs[idx] = config.SaveBlocKeys(bloc, map[string]string{"ssh": "key-" + strconv.Itoa(idx)})
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "SaveBlocKeys for bloc %d", i)
	}

	for i := range blocCount {
		bloc := "ocfp-lab-" + strconv.Itoa(i)

		keys, err := config.GetBlocKeys(bloc)
		require.NoError(t, err)

		assert.Equalf(t, "key-"+strconv.Itoa(i), keys["ssh"],
			"bloc %s lost its keys to a concurrent writer", bloc)
	}
}

// TestSetCurrentBloc_ConcurrentWritesPreserveBlocKeys checks the other
// load-modify-save on the same file: switching the current bloc must not drop
// a sibling's keys written at the same moment.
func TestSetCurrentBloc_ConcurrentWritesPreserveBlocKeys(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	const writers = 8

	var wg sync.WaitGroup

	for i := range writers {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			bloc := "ocfp-lab-" + strconv.Itoa(idx)
			_ = config.SaveBlocKeys(bloc, map[string]string{"ssh": "key-" + strconv.Itoa(idx)})
			_ = config.SetCurrentBloc(bloc, "/tmp/config.yml")
		}(i)
	}

	wg.Wait()

	for i := range writers {
		bloc := "ocfp-lab-" + strconv.Itoa(i)

		keys, err := config.GetBlocKeys(bloc)
		require.NoError(t, err)

		assert.Equalf(t, "key-"+strconv.Itoa(i), keys["ssh"],
			"bloc %s lost its keys during a concurrent current-bloc switch", bloc)
	}
}
