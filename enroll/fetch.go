package enroll

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ChunkSource answers for one chunk of one release binary. A source
// that returns a coded Error with CodeWorkerRevoked stops the fetch;
// any other error is for the caller to retry after its backoff.
type ChunkSource interface {
	Chunk(ctx context.Context, index int) ([]byte, error)
}

// FetchTarget is the file a fetch writes. *os.File satisfies it.
type FetchTarget interface {
	io.ReaderAt
	io.WriterAt
	Truncate(size int64) error
	Sync() error
}

// Fetch brings target to bin's bytes, chunk by chunk from src. It keeps
// every leading chunk already in target that matches bin's chunk digest
// and fetches from the first that does not, so a fetch that stopped
// resumes where it left off. A fetched chunk whose length or digest is
// wrong is refused with CodeDigestMismatch and never written. When every
// chunk matches, Fetch checks the whole file's digest; on a mismatch it
// empties target and refuses with CodeDigestMismatch, so the next fetch
// starts over. A source error ends the fetch with that error, and
// target keeps every chunk written before it. Only a nil return means
// target holds the binary, which the caller may then mark ready.
func Fetch(ctx context.Context, bin Binary, src ChunkSource, target FetchTarget) error {
	if err := bin.validate(); err != nil {
		return refuse(CodeDigestMismatch, err.Error())
	}

	start, err := verifiedPrefix(bin, target)
	if err != nil {
		return err
	}

	// Cut the file after the last chunk that matched, which also drops
	// any bytes past the binary's end.
	if err := target.Truncate(min(int64(start)*bin.ChunkSize, bin.Size)); err != nil {
		return fmt.Errorf("enroll: fetch: %w", err)
	}

	for i := start; i < len(bin.Chunks); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		b, err := src.Chunk(ctx, i)
		if err != nil {
			return err
		}

		if !chunkMatches(bin, i, b) {
			return refuse(CodeDigestMismatch, fmt.Sprintf("chunk %d of %s does not match the manifest", i, bin.Name))
		}

		if _, err := target.WriteAt(b, int64(i)*bin.ChunkSize); err != nil {
			return fmt.Errorf("enroll: fetch: %w", err)
		}
	}

	if err := target.Sync(); err != nil {
		return fmt.Errorf("enroll: fetch: %w", err)
	}

	return checkWhole(bin, target)
}

// verifiedPrefix returns how many leading chunks of target already
// match bin.
func verifiedPrefix(bin Binary, target io.ReaderAt) (int, error) {
	buf := make([]byte, bin.ChunkSize)

	for i := range bin.Chunks {
		n := bin.ChunkLen(i)

		got, err := target.ReadAt(buf[:n], int64(i)*bin.ChunkSize)
		if errors.Is(err, io.EOF) || int64(got) < n {
			return i, nil
		}

		if err != nil {
			return 0, fmt.Errorf("enroll: fetch: %w", err)
		}

		if !chunkMatches(bin, i, buf[:n]) {
			return i, nil
		}
	}

	return len(bin.Chunks), nil
}

func chunkMatches(bin Binary, i int, b []byte) bool {
	if int64(len(b)) != bin.ChunkLen(i) {
		return false
	}

	return digestEqual(b, bin.Chunks[i])
}

func digestEqual(b []byte, want string) bool {
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])

	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// checkWhole hashes the whole of target, and refuses a target longer than
// bin or whose digest is not bin's.
func checkWhole(bin Binary, target FetchTarget) error {
	h := sha256.New()

	n, err := io.Copy(h, io.NewSectionReader(target, 0, bin.Size+1))
	if err != nil {
		return fmt.Errorf("enroll: fetch: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if n == bin.Size && subtle.ConstantTimeCompare([]byte(got), []byte(bin.SHA256)) == 1 {
		return nil
	}

	if err := target.Truncate(0); err != nil {
		return fmt.Errorf("enroll: fetch: %w", err)
	}

	return refuse(CodeDigestMismatch, bin.Name+" does not match the manifest's digest")
}

// VerifyBinary reports whether r holds exactly bin's bytes.
func VerifyBinary(bin Binary, r io.Reader) error {
	h := sha256.New()

	n, err := io.Copy(h, io.LimitReader(r, bin.Size+1))
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if n != bin.Size || subtle.ConstantTimeCompare([]byte(got), []byte(bin.SHA256)) != 1 {
		return refuse(CodeDigestMismatch, bin.Name+" does not match the manifest's digest")
	}

	return nil
}
