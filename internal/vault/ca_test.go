package vault

import (
	"errors"
	"testing"
)

// fakeSafe is a minimal SafeInterface stub backed by an in-memory map.
type fakeSafe struct {
	data       map[string]map[string]interface{}
	failOnRead error
}

func newFakeSafe() *fakeSafe {
	return &fakeSafe{data: map[string]map[string]interface{}{}}
}

func (f *fakeSafe) Set(path, key string, value interface{}) error {
	if _, ok := f.data[path]; !ok {
		f.data[path] = map[string]interface{}{}
	}

	f.data[path][key] = value

	return nil
}

func (f *fakeSafe) SetMultiple(path string, data map[string]interface{}) error {
	if _, ok := f.data[path]; !ok {
		f.data[path] = map[string]interface{}{}
	}

	for k, v := range data {
		f.data[path][k] = v
	}

	return nil
}

func (f *fakeSafe) Get(path, key string) (interface{}, error) {
	if d, ok := f.data[path]; ok {
		return d[key], nil
	}

	return nil, nil
}

func (f *fakeSafe) GetAll(path string) (map[string]interface{}, error) {
	if f.failOnRead != nil {
		return nil, f.failOnRead
	}

	if d, ok := f.data[path]; ok {
		return d, nil
	}

	return nil, nil
}

func (f *fakeSafe) Exists(path string) (bool, error) {
	_, ok := f.data[path]
	return ok, nil
}

func (f *fakeSafe) Delete(path, key string) error {
	if d, ok := f.data[path]; ok {
		delete(d, key)
	}

	return nil
}

func (f *fakeSafe) List(string) ([]string, error)                 { return nil, nil }
func (f *fakeSafe) Export(string) (map[string]interface{}, error) { return nil, nil }
func (f *fakeSafe) Import(string, map[string]interface{}) error   { return nil }
func (f *fakeSafe) GetEngineInfo(string) (*EngineInfo, error)     { return nil, nil }
func (f *fakeSafe) MustGet(path, key string) interface{}          { v, _ := f.Get(path, key); return v }
func (f *fakeSafe) GetString(path, key string) (string, error) {
	v, _ := f.Get(path, key)
	s, _ := v.(string)
	return s, nil
}
func (f *fakeSafe) GetJSON(string, string) ([]byte, error) { return nil, nil }

func TestLoadOrGenerateBlocCA_ColdPathGeneratesAndWrites(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()

	mat, err := LoadOrGenerateBlocCA(safe, "pve-wayne")
	if err != nil {
		t.Fatalf("LoadOrGenerateBlocCA: %v", err)
	}

	if mat.CertPEM == "" || mat.KeyPEM == "" || mat.Fingerprint == "" {
		t.Errorf("returned material is incomplete: %+v", mat)
	}

	stored, ok := safe.data["secret/ocfp/pve-wayne/ca"]
	if !ok {
		t.Fatalf("expected secret/ocfp/pve-wayne/ca to be written")
	}

	if stored["cert"] != mat.CertPEM || stored["key"] != mat.KeyPEM || stored["fingerprint"] != mat.Fingerprint {
		t.Errorf("stored values do not match returned material")
	}

	if _, ok := stored["created_at"]; !ok {
		t.Errorf("created_at missing from stored CA")
	}
}

func TestLoadOrGenerateBlocCA_WarmPathReturnsExisting(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()

	first, err := LoadOrGenerateBlocCA(safe, "pve-wayne")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	second, err := LoadOrGenerateBlocCA(safe, "pve-wayne")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprint changed on warm path: %q → %q", first.Fingerprint, second.Fingerprint)
	}

	if first.CertPEM != second.CertPEM {
		t.Errorf("cert PEM changed on warm path")
	}
}

func TestLoadOrGenerateBlocCA_CorruptedPathErrors(t *testing.T) {
	t.Parallel()

	safe := newFakeSafe()
	safe.data["secret/ocfp/pve-wayne/ca"] = map[string]interface{}{
		"cert": "", // missing cert → malformed
		"key":  "junk",
	}

	_, err := LoadOrGenerateBlocCA(safe, "pve-wayne")
	if err == nil {
		t.Errorf("expected error for malformed CA")
	}

	if !errors.Is(err, ErrBlocCAMalformed) {
		t.Errorf("expected ErrBlocCAMalformed, got %v", err)
	}
}

func TestLoadOrGenerateBlocCA_RequiresInputs(t *testing.T) {
	t.Parallel()

	_, err := LoadOrGenerateBlocCA(nil, "pve-wayne")
	if err == nil {
		t.Errorf("expected error for nil safe")
	}

	_, err = LoadOrGenerateBlocCA(newFakeSafe(), "")
	if err == nil {
		t.Errorf("expected error for empty bloc")
	}
}
