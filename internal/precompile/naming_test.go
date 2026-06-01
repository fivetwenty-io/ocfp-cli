package precompile

import "testing"

func TestCompiledKey(t *testing.T) {
	got := CompiledKey(Release{Name: "capi", Version: "1.235.0"}, Stemcell{OS: "ubuntu-noble", Version: "1.383"})
	want := "compiled-releases/capi-1.235.0-ubuntu-noble-1.383.tgz"
	if got != want {
		t.Errorf("CompiledKey = %q, want %q", got, want)
	}
}

func TestCompiledKeySlugsDevVersion(t *testing.T) {
	got := CompiledKey(Release{Name: "uaa", Version: "78.14.0+dev.5"}, DefaultStemcell)
	want := "compiled-releases/uaa-78.14.0-dev.5-ubuntu-noble-1.383.tgz"
	if got != want {
		t.Errorf("CompiledKey = %q, want %q", got, want)
	}
}

func TestHTTPSURLPathStyle(t *testing.T) {
	got := HTTPSURL("https://10.0.0.5:9000/", "dev-ocf-bosh", "compiled-releases/bpm-1.4.31-ubuntu-noble-1.383.tgz")
	want := "https://10.0.0.5:9000/dev-ocf-bosh/compiled-releases/bpm-1.4.31-ubuntu-noble-1.383.tgz"
	if got != want {
		t.Errorf("HTTPSURL = %q, want %q", got, want)
	}
}

func TestStemcellString(t *testing.T) {
	if DefaultStemcell.String() != "ubuntu-noble/1.383" {
		t.Errorf("DefaultStemcell.String() = %q", DefaultStemcell.String())
	}
}
