package bootstrap_test

import (
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// stubSafe is a no-op SafeInterface used solely to verify the bootstrap manager
// retains the wired Safe client. Method bodies return zero values; tests should
// not invoke any of them.
type stubSafe struct{}

func (stubSafe) Set(string, string, interface{}) error                         { return nil }
func (stubSafe) SetMultiple(string, map[string]interface{}) error              { return nil }
func (stubSafe) Get(string, string) (interface{}, error)                       { return nil, nil }
func (stubSafe) GetAll(string) (map[string]interface{}, error)                 { return nil, nil }
func (stubSafe) Exists(string) (bool, error)                                   { return false, nil }
func (stubSafe) Delete(string, string) error                                   { return nil }
func (stubSafe) List(string) ([]string, error)                                 { return nil, nil }
func (stubSafe) Export(string) (map[string]interface{}, error)                 { return nil, nil }
func (stubSafe) Import(string, map[string]interface{}) error                   { return nil }
func (stubSafe) GetEngineInfo(string) (*vault.EngineInfo, error)               { return nil, nil }
func (stubSafe) MustGet(string, string) interface{}                            { return nil }
func (stubSafe) GetString(string, string) (string, error)                      { return "", nil }
func (stubSafe) GetJSON(string, string) ([]byte, error)                        { return nil, nil }

func TestManagerSetSafeWiresVaultClient(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	sm, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = sm.Load("prod")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	m := bootstrap.NewManager(cfg, nil, sm, &bootstrap.Options{BlocName: "prod"})

	if m.HasSafe() {
		t.Errorf("safe should be nil before SetSafe")
	}

	m.SetSafe(stubSafe{})

	if !m.HasSafe() {
		t.Errorf("safe should be set after SetSafe")
	}
}
