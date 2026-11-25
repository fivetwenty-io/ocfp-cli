package enroll

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// memSource serves chunks of data, counting the requests, and can fail
// or corrupt one index.
type memSource struct {
	data    []byte
	size    int64
	calls   []int
	failAt  int
	failErr error
	corrupt int
}

func (s *memSource) Chunk(_ context.Context, i int) ([]byte, error) {
	s.calls = append(s.calls, i)
	if s.failErr != nil && i == s.failAt {
		return nil, s.failErr
	}

	b := append([]byte{}, s.data[int64(i)*s.size:min(int64(i+1)*s.size, int64(len(s.data)))]...)
	if s.corrupt == i {
		b[0] ^= 0xff
	}

	return b, nil
}

func newSource(data []byte) *memSource {
	return &memSource{data: data, size: goldenChunkSize, failAt: -1, corrupt: -1}
}

func fetchFixture(t *testing.T) (Binary, []byte, *os.File) {
	t.Helper()

	data := goldenBinary("amd64")
	bin := binaryFor("amd64", data, goldenChunkSize)

	f, err := os.OpenFile(filepath.Join(t.TempDir(), bin.Name), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = f.Close() })

	return bin, data, f
}

func fileBytes(t *testing.T, f *os.File) []byte {
	t.Helper()

	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestFetchFresh(t *testing.T) {
	t.Parallel()

	bin, data, f := fetchFixture(t)
	src := newSource(data)

	if err := Fetch(t.Context(), bin, src, f); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(fileBytes(t, f), data) || len(src.calls) != 4 {
		t.Errorf("fetched %d chunks", len(src.calls))
	}

	// A second fetch finds every chunk and asks for none.
	again := newSource(data)
	if err := Fetch(t.Context(), bin, again, f); err != nil || len(again.calls) != 0 {
		t.Errorf("a finished fetch asked for %v, %v", again.calls, err)
	}

	if err := VerifyBinary(bin, bytes.NewReader(data)); err != nil {
		t.Error(err)
	}

	if err := VerifyBinary(bin, bytes.NewReader(append(append([]byte{}, data...), 0))); CodeOf(err) != CodeDigestMismatch {
		t.Errorf("VerifyBinary took a longer file: %v", err)
	}
}

func TestFetchResumes(t *testing.T) {
	t.Parallel()

	bin, data, f := fetchFixture(t)
	stop := errors.New("the connection closed")

	src := newSource(data)
	src.failAt, src.failErr = 2, stop

	if err := Fetch(t.Context(), bin, src, f); !errors.Is(err, stop) || StopsFetch(err) {
		t.Fatalf("a closed connection = %v", err)
	}

	if got := fileBytes(t, f); !bytes.Equal(got, data[:2*goldenChunkSize]) {
		t.Fatalf("after the stop the file holds %d bytes", len(got))
	}

	resume := newSource(data)
	if err := Fetch(t.Context(), bin, resume, f); err != nil {
		t.Fatal(err)
	}

	if len(resume.calls) != 2 || resume.calls[0] != 2 || !bytes.Equal(fileBytes(t, f), data) {
		t.Errorf("the resume asked for %v", resume.calls)
	}
}

func TestFetchRefetchesACorruptPartial(t *testing.T) {
	t.Parallel()

	bin, data, f := fetchFixture(t)

	partial := append([]byte{}, data[:3*goldenChunkSize]...)
	partial[goldenChunkSize+5] ^= 1

	if _, err := f.WriteAt(partial, 0); err != nil {
		t.Fatal(err)
	}

	src := newSource(data)
	if err := Fetch(t.Context(), bin, src, f); err != nil {
		t.Fatal(err)
	}

	if len(src.calls) != 3 || src.calls[0] != 1 || !bytes.Equal(fileBytes(t, f), data) {
		t.Errorf("a corrupt chunk 1 led to %v", src.calls)
	}
}

func TestFetchRefusesABadChunk(t *testing.T) {
	t.Parallel()

	bin, data, f := fetchFixture(t)

	src := newSource(data)
	src.corrupt = 1

	if err := Fetch(t.Context(), bin, src, f); CodeOf(err) != CodeDigestMismatch || StopsFetch(err) {
		t.Fatalf("a bad chunk = %v", err)
	}

	if got := fileBytes(t, f); !bytes.Equal(got, data[:goldenChunkSize]) {
		t.Errorf("a bad chunk was written: the file holds %d bytes", len(got))
	}

	short := newSource(data[:len(data)-1])
	if err := Fetch(t.Context(), bin, short, f); CodeOf(err) != CodeDigestMismatch {
		t.Errorf("a short last chunk = %v", err)
	}
}

func TestFetchEmptiesAWholeFileMismatch(t *testing.T) {
	t.Parallel()

	bin, data, f := fetchFixture(t)

	// Every chunk matches, but the whole-file digest names other bytes,
	// as a manifest with a wrong file digest would.
	bin.SHA256 = bin.Chunks[0]

	if err := Fetch(t.Context(), bin, newSource(data), f); CodeOf(err) != CodeDigestMismatch {
		t.Fatalf("a whole-file mismatch = %v", err)
	}

	if got := fileBytes(t, f); len(got) != 0 {
		t.Errorf("a whole-file mismatch left %d bytes", len(got))
	}

	// Trailing bytes past the binary are cut off.
	good := binaryFor("amd64", data, goldenChunkSize)
	if _, err := f.WriteAt(append(append([]byte{}, data...), "tail"...), 0); err != nil {
		t.Fatal(err)
	}

	src := newSource(data)
	if err := Fetch(t.Context(), good, src, f); err != nil || len(src.calls) != 0 || !bytes.Equal(fileBytes(t, f), data) {
		t.Errorf("a file with trailing bytes = %v after %v", err, src.calls)
	}
}

func TestFetchStopsOnRevocationAndCancel(t *testing.T) {
	t.Parallel()

	bin, data, f := fetchFixture(t)

	src := newSource(data)
	src.failAt, src.failErr = 0, refuse(CodeWorkerRevoked, "removed")

	if err := Fetch(t.Context(), bin, src, f); !StopsFetch(err) {
		t.Errorf("a revoked worker = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := Fetch(ctx, bin, newSource(data), f); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled fetch = %v", err)
	}

	bad := bin
	bad.Chunks = bad.Chunks[1:]

	if err := Fetch(t.Context(), bad, newSource(data), f); CodeOf(err) != CodeDigestMismatch {
		t.Errorf("a binary out of form = %v", err)
	}

	for _, code := range []string{
		CodeReleaseSignatureExpired, CodeReleaseVersionUnavailable, CodeReleaseRateLimited,
		CodeReleaseRequestInvalid, CodeUnavailable, CodeSignatureExpired,
	} {
		if StopsFetch(refuse(code, "")) {
			t.Errorf("%s stops the fetch", code)
		}
	}
}
