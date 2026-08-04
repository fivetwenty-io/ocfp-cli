package precompile

import (
	"context"
	"crypto/sha1" //nolint:gosec // computing the same digest ResolveStemcell verifies against, not for cryptographic security
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// errS3 is an objectAPI whose HeadObject/PutObject can be forced to fail, to
// exercise ResolveStemcell's S3 error paths. fakeS3 (compiler_test.go) has no
// error-injection hooks, so this is a separate, minimal fake rather than an
// extension of it.
type errS3 struct {
	headErr error
	putErr  error
	puts    int
}

func (f *errS3) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	return nil, &s3types.NotFound{}
}

func (f *errS3) PutObject(_ context.Context, _ *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.puts++
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &s3.PutObjectOutput{}, nil
}

func TestStemcellKey(t *testing.T) {
	got := StemcellKey("ubuntu-noble", "1.383")
	want := "stemcells/ubuntu-noble-1.383.tgz"
	if got != want {
		t.Errorf("StemcellKey = %q, want %q", got, want)
	}
}

func TestStemcellKeySlugsSpecialChars(t *testing.T) {
	got := StemcellKey("ubuntu noble", "1.383+dev.5")
	want := "stemcells/ubuntu-noble-1.383-dev.5.tgz"
	if got != want {
		t.Errorf("StemcellKey = %q, want %q", got, want)
	}
}

func TestResolveStemcellPresentSkipsDownloadAndUpload(t *testing.T) {
	s3c := newFakeS3()
	key := StemcellKey("ubuntu-noble", "1.383")
	s3c.objects[key] = "cafe"

	// upstreamURL is deliberately empty: if the present path attempted a
	// download it would fail on the empty URL, proving no download happened.
	url, sha, err := ResolveStemcell(context.Background(), s3c, nil,
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", "", "", false)
	if err != nil {
		t.Fatalf("ResolveStemcell: %v", err)
	}
	if want := HTTPSURL("https://10.0.0.5:9000", "dev-ocf-bosh", key); url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if sha != "cafe" {
		t.Errorf("sha256 = %q, want %q", sha, "cafe")
	}
	if s3c.puts != 0 {
		t.Errorf("present path should not upload, got %d puts", s3c.puts)
	}
}

func TestResolveStemcellAbsentDownloadsAndCaches(t *testing.T) {
	body := []byte("fake-stemcell-tarball-bytes")
	h := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s3c := newFakeS3()
	key := StemcellKey("ubuntu-noble", "1.383")

	url, sha, err := ResolveStemcell(context.Background(), s3c, srv.Client(),
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, "", false)
	if err != nil {
		t.Fatalf("ResolveStemcell: %v", err)
	}
	if want := HTTPSURL("https://10.0.0.5:9000", "dev-ocf-bosh", key); url != want {
		t.Errorf("url = %q, want %q", url, want)
	}
	if sha != wantSHA {
		t.Errorf("sha256 = %q, want %q", sha, wantSHA)
	}
	if s3c.puts != 1 {
		t.Errorf("expected 1 blobstore put, got %d", s3c.puts)
	}
	if got := s3c.objects[key]; got != wantSHA {
		t.Errorf("stored metadata sha = %q, want %q", got, wantSHA)
	}
}

func TestResolveStemcellForceReDownloadsWhenPresent(t *testing.T) {
	body := []byte("fresh-stemcell-tarball")
	h := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s3c := newFakeS3()
	key := StemcellKey("ubuntu-noble", "1.383")
	s3c.objects[key] = "stale"

	_, sha, err := ResolveStemcell(context.Background(), s3c, srv.Client(),
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, "", true)
	if err != nil {
		t.Fatalf("ResolveStemcell: %v", err)
	}
	if sha != wantSHA {
		t.Errorf("force should re-download and return fresh sha, got %q want %q", sha, wantSHA)
	}
	if s3c.puts != 1 {
		t.Errorf("expected 1 blobstore put, got %d", s3c.puts)
	}
}

func TestResolveStemcellAbsentNoUpstreamErrors(t *testing.T) {
	s3c := newFakeS3()
	_, _, err := ResolveStemcell(context.Background(), s3c, nil,
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", "", "", false)
	if err == nil {
		t.Error("expected error when cache miss and no upstream URL given")
	}
}

func TestResolveStemcellRejectsEmptyNameOrVersion(t *testing.T) {
	s3c := newFakeS3()
	if _, _, err := ResolveStemcell(context.Background(), s3c, nil, "b", "https://e", "", "1.383", "https://x", "", false); err == nil {
		t.Error("expected error for empty name")
	}
	if _, _, err := ResolveStemcell(context.Background(), s3c, nil, "b", "https://e", "ubuntu-noble", "", "https://x", "", false); err == nil {
		t.Error("expected error for empty version")
	}
}

func TestResolveStemcellUpstreamDownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s3c := newFakeS3()
	_, _, err := ResolveStemcell(context.Background(), s3c, srv.Client(),
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, "", false)
	if err == nil {
		t.Error("expected error when upstream returns non-200")
	}
}

func TestResolveStemcellSHA1MatchSucceeds(t *testing.T) {
	body := []byte("sha1-pinned-stemcell-bytes")
	h1 := sha1.Sum(body) //nolint:gosec // matches the digest algorithm ResolveStemcell verifies against
	wantSHA1 := hex.EncodeToString(h1[:])
	h256 := sha256.Sum256(body)
	wantSHA256 := hex.EncodeToString(h256[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s3c := newFakeS3()
	key := StemcellKey("ubuntu-noble", "1.383")

	_, sha, err := ResolveStemcell(context.Background(), s3c, srv.Client(),
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, wantSHA1, false)
	if err != nil {
		t.Fatalf("ResolveStemcell: %v", err)
	}
	if sha != wantSHA256 {
		t.Errorf("sha256 = %q, want %q", sha, wantSHA256)
	}
	if s3c.puts != 1 {
		t.Errorf("expected 1 blobstore put on sha1 match, got %d", s3c.puts)
	}
	if got := s3c.objects[key]; got != wantSHA256 {
		t.Errorf("stored metadata sha = %q, want %q", got, wantSHA256)
	}
}

func TestResolveStemcellSHA1MismatchErrorsNoUpload(t *testing.T) {
	body := []byte("corrupted-in-transit")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	s3c := newFakeS3()
	_, _, err := ResolveStemcell(context.Background(), s3c, srv.Client(),
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, "0000000000000000000000000000000000dead", false)
	if err == nil {
		t.Fatal("expected error on sha1 mismatch")
	}
	if s3c.puts != 0 {
		t.Errorf("sha1 mismatch must not upload, got %d puts", s3c.puts)
	}
}

func TestResolveStemcellHeadObjectErrorSurfaces(t *testing.T) {
	fake := &errS3{headErr: errors.New("network boom")}

	// upstreamURL and hc are deliberately unusable: a HeadObject error other
	// than not-found must return before any download is attempted.
	_, _, err := ResolveStemcell(context.Background(), fake, nil,
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", "", "", false)
	if err == nil {
		t.Fatal("expected error when HeadObject fails with a non-not-found error")
	}
	if fake.puts != 0 {
		t.Errorf("head failure must not attempt upload, got %d puts", fake.puts)
	}
}

func TestResolveStemcellPutObjectErrorSurfaces(t *testing.T) {
	body := []byte("uploadable-bytes")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	fake := &errS3{putErr: errors.New("s3 unavailable")}

	_, _, err := ResolveStemcell(context.Background(), fake, srv.Client(),
		"dev-ocf-bosh", "https://10.0.0.5:9000",
		"ubuntu-noble", "1.383", srv.URL, "", false)
	if err == nil {
		t.Fatal("expected error when PutObject fails")
	}
	if fake.puts != 1 {
		t.Errorf("expected PutObject to be attempted once, got %d", fake.puts)
	}
}
