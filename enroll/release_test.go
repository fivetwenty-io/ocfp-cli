package enroll

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var goldenClock = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func goldenReleaseMessages(t *testing.T) (map[string][]byte, map[string][]byte) {
	t.Helper()

	data := goldenBinary("amd64")
	set := buildSet(t, goldenManifest(), 1, setNotBefore, setNotAfter)
	end := setNotAfter

	enc := func(b []byte, err error) []byte {
		t.Helper()

		if err != nil {
			t.Fatal(err)
		}

		return b
	}

	requests := map[string][]byte{
		"manifest":       enc(EncodeRelease(ManifestRequest{})),
		"chunk":          enc(EncodeRelease(ChunkRequest{Version: goldenVersion, Arch: "amd64", Index: 3})),
		"chunk-first":    enc(EncodeRelease(ChunkRequest{Version: goldenVersion, Arch: "arm64", Index: 0})),
		"status":         enc(EncodeRelease(StatusRequest{Code: CodeSignatureExpired, Clock: goldenClock})),
		"status-no-code": enc(EncodeRelease(StatusRequest{Clock: goldenClock})),
	}

	answers := map[string][]byte{
		"manifest": enc(EncodeRelease(ManifestAnswer{Set: &set})),
		"manifest-expired": enc(EncodeRelease(ManifestAnswer{Refusal: &ReleaseRefusal{
			Code: CodeReleaseSignatureExpired, Version: goldenVersion, NotAfter: &end,
		}})),
		"chunk": enc(EncodeRelease(ChunkAnswer{
			Version: goldenVersion, Arch: "amd64", Index: 3, Chunk: data[3*goldenChunkSize:],
			SHA256: binaryFor("amd64", data, goldenChunkSize).Chunks[3],
		})),
		"chunk-first": enc(EncodeRelease(ChunkAnswer{
			Version: goldenVersion, Arch: "amd64", Index: 0, Chunk: data[:goldenChunkSize],
			SHA256: binaryFor("amd64", data, goldenChunkSize).Chunks[0],
		})),
		"chunk-revoked":  enc(EncodeRelease(ChunkAnswer{Refusal: &ReleaseRefusal{Code: CodeWorkerRevoked}})),
		"chunk-version":  enc(EncodeRelease(ChunkAnswer{Refusal: &ReleaseRefusal{Code: CodeReleaseVersionUnavailable}})),
		"status":         enc(EncodeRelease(StatusAnswer{})),
		"status-limited": enc(EncodeRelease(StatusAnswer{Refusal: &ReleaseRefusal{Code: CodeReleaseRateLimited}})),
	}

	return requests, answers
}

func asStrings(m map[string][]byte) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}

	return out
}

// TestReleaseGolden pins every release message and decodes each back.
func TestReleaseGolden(t *testing.T) {
	t.Parallel()

	requests, answers := goldenReleaseMessages(t)
	checkGoldenJSON(t, "release-requests.json", asStrings(requests))
	checkGoldenJSON(t, "release-answers.json", asStrings(answers))

	if _, err := DecodeRelease[ManifestRequest](requests["manifest"]); err != nil {
		t.Error(err)
	}

	cr, err := DecodeRelease[ChunkRequest](requests["chunk"])
	if err != nil || cr.Index != 3 {
		t.Fatalf("chunk request = %+v, %v", cr, err)
	}

	for _, name := range []string{"status", "status-no-code"} {
		if _, err := DecodeRelease[StatusRequest](requests[name]); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	ma, err := DecodeRelease[ManifestAnswer](answers["manifest"])
	if err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyManifestSet(testRoots(t), TrustState{}, *ma.Set, setNow); err != nil {
		t.Errorf("the set from the answer does not verify: %v", err)
	}

	ex, err := DecodeRelease[ManifestAnswer](answers["manifest-expired"])
	if err != nil || CodeOf(ex.Refusal.Err()) != CodeReleaseSignatureExpired || !ex.Refusal.NotAfter.Equal(setNotAfter) {
		t.Errorf("expired answer = %+v, %v", ex, err)
	}

	ca, err := DecodeRelease[ChunkAnswer](answers["chunk"])
	if err != nil || !ca.Answers(cr) || ca.Answers(ChunkRequest{Version: goldenVersion, Arch: "amd64", Index: 2}) {
		t.Errorf("chunk answer = %v", err)
	}

	rev, err := DecodeRelease[ChunkAnswer](answers["chunk-revoked"])
	if err != nil || !StopsFetch(rev.Refusal.Err()) || rev.Answers(cr) {
		t.Errorf("revoked answer = %+v, %v", rev, err)
	}

	for _, name := range []string{"status", "status-limited"} {
		if _, err := DecodeRelease[StatusAnswer](answers[name]); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestReleaseRefusesMalformed(t *testing.T) {
	t.Parallel()

	requests, answers := goldenReleaseMessages(t)

	reqCases := map[string]func() error{
		"trailing space": func() error { _, err := DecodeRelease[ChunkRequest](append(requests["chunk"], ' ')); return err },
		"unknown field": func() error {
			_, err := DecodeRelease[ManifestRequest]([]byte(`{"arch":"amd64"}`))
			return err
		},
		"negative index": func() error {
			_, err := DecodeRelease[ChunkRequest]([]byte(`{"version":"1.4.0","arch":"amd64","index":-1}`))
			return err
		},
		"index past bound": func() error {
			_, err := EncodeRelease(ChunkRequest{Version: goldenVersion, Arch: "amd64", Index: MaxChunks})
			return err
		},
		"bad version": func() error {
			_, err := EncodeRelease(ChunkRequest{Version: "v1", Arch: "amd64"})
			return err
		},
		"local clock": func() error {
			_, err := EncodeRelease(StatusRequest{Clock: goldenClock.Local()})
			return err
		},
		"lower-case code": func() error {
			_, err := EncodeRelease(StatusRequest{Code: "x", Clock: goldenClock})
			return err
		},
		"too long": func() error {
			_, err := DecodeRelease[StatusRequest](bytes.Repeat([]byte(" "), MaxReleaseRequest+1))
			return err
		},
		"empty": func() error { _, err := DecodeRelease[ManifestRequest](nil); return err },
	}
	for name, f := range reqCases {
		if err := f(); CodeOf(err) != CodeReleaseRequestInvalid {
			t.Errorf("request %s = %v", name, err)
		}
	}

	set := buildSet(t, goldenManifest(), 1, setNotBefore, setNotAfter)
	half := set
	half.RevocationSig = nil
	end := setNotAfter

	ansCases := map[string]func() error{
		"both": func() error {
			_, err := EncodeRelease(ManifestAnswer{Set: &set, Refusal: &ReleaseRefusal{Code: CodeUnavailable}})
			return err
		},
		"neither":         func() error { _, err := EncodeRelease(ManifestAnswer{}); return err },
		"half revocation": func() error { _, err := EncodeRelease(ManifestAnswer{Set: &half}); return err },
		"unknown code": func() error {
			_, err := EncodeRelease(StatusAnswer{Refusal: &ReleaseRefusal{Code: "NOPE"}})
			return err
		},
		"join code": func() error {
			_, err := EncodeRelease(StatusAnswer{Refusal: &ReleaseRefusal{Code: CodePendingFull}})
			return err
		},
		"expired no date": func() error {
			_, err := EncodeRelease(StatusAnswer{Refusal: &ReleaseRefusal{Code: CodeReleaseSignatureExpired}})
			return err
		},
		"date on another": func() error {
			_, err := EncodeRelease(StatusAnswer{Refusal: &ReleaseRefusal{Code: CodeUnavailable, Version: goldenVersion, NotAfter: &end}})
			return err
		},
		"wrong digest": func() error {
			_, err := EncodeRelease(ChunkAnswer{Version: goldenVersion, Arch: "amd64", Chunk: []byte("x"), SHA256: strings.Repeat("0", 64)})
			return err
		},
		"refusal with chunk": func() error {
			_, err := EncodeRelease(ChunkAnswer{Refusal: &ReleaseRefusal{Code: CodeUnavailable}, Chunk: []byte("x")})
			return err
		},
		"empty chunk field": func() error {
			_, err := DecodeRelease[ChunkAnswer]([]byte(`{"version":"1.4.0","arch":"amd64","chunk":"","sha256":"` + strings.Repeat("0", 64) + `"}`))
			return err
		},
		"reordered": func() error {
			b := bytes.Replace(answers["chunk"], []byte(`{"version"`), []byte(`{"sha256":"x","version"`), 1)
			_, err := DecodeRelease[ChunkAnswer](b)
			return err
		},
	}
	for name, f := range ansCases {
		if err := f(); CodeOf(err) != CodeReleaseAnswerInvalid {
			t.Errorf("answer %s = %v", name, err)
		}
	}
}
