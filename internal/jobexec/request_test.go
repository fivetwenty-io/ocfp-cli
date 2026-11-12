package jobexec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress/progresstest"
	"github.com/fivetwenty-io/ocfp-cli/internal/strictjson"
)

// accepted marks a row whose input is not refused.
const accepted progress.Code = ""

// TestJobRequestUsesTheSharedHelper replays requests through checkOwner. The
// was column holds the code each row got from the request decoder this
// package carried before strictjson, and want holds the code it gets now.
// The two differ only where the shared helper refuses more.
func TestJobRequestUsesTheSharedHelper(t *testing.T) {
	t.Parallel()

	good := mustMarshal(t, goodRequest())

	newer := goodRequest()
	newer.WireContract = 2
	newerPayload := mustMarshal(t, newer)

	stranger := goodRequest()
	stranger.InstallationID = "inst-fixture-02"
	strangerPayload := mustMarshal(t, stranger)

	for _, tc := range []struct {
		name      string
		payload   []byte
		key       []byte
		was, want progress.Code
	}{
		{"good", good, nil, accepted, accepted},
		{"env keys that differ only in case", replaceOnce(t, good, `"env":{"LANG":"C"}`, `"env":{"LANG":"C","lang":"D"}`), nil, accepted, accepted},
		{"null env", replaceOnce(t, good, `"env":{"LANG":"C"}`, `"env":null`), nil, accepted, accepted},
		{"unknown field", replaceOnce(t, good, `{"wireContract"`, `{"argv":["sh"],"wireContract"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"case variant inside an unknown value", replaceOnce(t, good, `{"wireContract"`, `{"x":{"JOBID":1},"wireContract"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"case variant inside an unknown value in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, `{"x":{"JOBID":1},"wireContract"`), nil, progress.CodeWireContractUnsupported, progress.CodeWireContractUnsupported},
		{"unknown lone surrogate in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, `{"x":"\ud800","wireContract"`), nil, progress.CodeWireContractUnsupported, progress.CodeWireContractUnsupported},
		{"unknown U+FFFD in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, "{\"x\":\"�\",\"wireContract\""), nil, progress.CodeWireContractUnsupported, progress.CodeWireContractUnsupported},
		{"repeated key in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, `{"verb":"x","wireContract"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"invalid UTF-8 in a newer contract", replaceOnce(t, newerPayload, `{"wireContract"`, "{\"x\":\"\xff\",\"wireContract\""), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"unknown field from another installation", replaceOnce(t, strangerPayload, `{"wireContract"`, `{"argv":["sh"],"wireContract"`), nil, progress.CodeOwnerMismatch, progress.CodeOwnerMismatch},
		{"repeated key from another installation", replaceOnce(t, strangerPayload, `{"wireContract"`, `{"verb":"x","wireContract"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"case variant from another installation", replaceOnce(t, strangerPayload, `"jobId"`, `"JOBID"`), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"invalid UTF-8 from another installation", replaceOnce(t, strangerPayload, `"noop"`, "\"no\xffop\""), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"trailing data from another installation", append(bytes.Clone(strangerPayload), 'x'), nil, progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"repeated key under another key", replaceOnce(t, good, `{"wireContract"`, `{"verb":"x","wireContract"`), otherKey(), progress.CodeRequestUnsigned, progress.CodeRequestUnsigned},
		{"invalid UTF-8 under another key", replaceOnce(t, good, `"noop"`, "\"no\xffop\""), otherKey(), progress.CodeRequestUnsigned, progress.CodeRequestUnsigned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key := ownerKey()
			if tc.key != nil {
				key = tc.key
			}

			st := storeWith(t, liveRecord())

			_, release, err := checkOwner(t.Context(), signPayload(tc.payload, key), st, fixedClock{testNow})
			if release != nil {
				defer func() { _ = release() }()
			}

			wantCode(t, err, tc.want)

			// A payload that the shared helper refuses never reaches a
			// check that reads its fields, so only a refusal that comes
			// before decoding can carry another code.
			if _, herr := strictjson.DecodeWithUnknown(tc.payload, new(Request)); herr != nil && tc.key == nil {
				wantCode(t, err, progress.CodeJobRequestInvalid)
			}
		})
	}
}

// TestRequestLineUsesTheSharedHelper replays signed request lines through
// readRequest, with the same was and want columns.
func TestRequestLineUsesTheSharedHelper(t *testing.T) {
	t.Parallel()

	line := mustMarshal(t, signPayload(mustMarshal(t, goodRequest()), ownerKey()))

	for _, tc := range []struct {
		name      string
		line      []byte
		was, want progress.Code
	}{
		{"good", line, accepted, accepted},
		{"unknown field", replaceOnce(t, line, `{"format"`, `{"extra":1,"format"`), progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"trailing data", append(bytes.Clone(line), " {}"...), progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
		{"null", []byte("null"), accepted, progress.CodeJobRequestInvalid},
		{"repeated format", replaceOnce(t, line, `{"format"`, `{"format":2,"format"`), accepted, progress.CodeJobRequestInvalid},
		{"repeated payload", replaceOnce(t, line, `{"format"`, `{"payload":"","format"`), accepted, progress.CodeJobRequestInvalid},
		{"format in another case", replaceOnce(t, line, `{"format"`, `{"Format"`), accepted, progress.CodeJobRequestInvalid},
		{"invalid UTF-8 in an unknown key", replaceOnce(t, line, `{"format"`, "{\"\xff\":1,\"format\""), progress.CodeJobRequestInvalid, progress.CodeJobRequestInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := readRequest(bytes.NewReader(tc.line))
			wantCode(t, err, tc.want)
		})
	}
}

// TestSpoolFilesUseTheSharedHelper replays the status and exit fixtures, and
// damaged copies of them, through the spool's reader. Was and want are true
// when the file is read without an error.
func TestSpoolFilesUseTheSharedHelper(t *testing.T) {
	t.Parallel()

	status := progresstest.Fixture("status-v1.json")
	exited := progresstest.Fixture("exit-status-exited-v1.json")
	exit := progresstest.Fixture("exit-v1.json")

	for _, tc := range []struct {
		name      string
		file      string
		data      []byte
		was, want bool
	}{
		{"status fixture", statusFile, status, true, true},
		{"exited status fixture", statusFile, exited, true, true},
		{"exit fixture", exitFile, exit, true, true},
		{"status with an unknown field", statusFile, replaceOnce(t, status, `{"state"`, `{"extra":1,"state"`), false, false},
		{"status with trailing data", statusFile, append(bytes.Clone(status), '{', '}'), false, false},
		{"exit with an unknown field", exitFile, replaceOnce(t, exit, `{"exitCode"`, `{"extra":1,"exitCode"`), false, false},
		{"null status", statusFile, []byte("null\n"), true, false},
		{"status with a repeated state", statusFile, replaceOnce(t, status, `{"state"`, `{"state":"exited","state"`), true, false},
		{"status with the state in another case", statusFile, replaceOnce(t, status, `{"state"`, `{"State"`), true, false},
		{"status with invalid UTF-8", statusFile, replaceOnce(t, status, `"running"`, "\"runn\xffing\""), true, false},
		{"exit with a repeated code", exitFile, replaceOnce(t, exit, `{"exitCode"`, `{"exitCode":1,"exitCode"`), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, _, err := CreateSpool(spoolRoot(t), "job-fixture-01")
			if err != nil {
				t.Fatal(err)
			}

			err = os.WriteFile(filepath.Join(s.Dir, tc.file), tc.data, 0o600)
			if err != nil {
				t.Fatal(err)
			}

			if tc.file == statusFile {
				_, _, err = s.readStatus()
			} else {
				_, _, err = s.readExit()
			}

			if got := err == nil; got != tc.want {
				t.Fatalf("read without error = %v, want %v (%v)", got, tc.want, err)
			}
		})
	}
}

// wantCode checks that err is nil when code is empty, and otherwise that it
// is a refusal with code.
func wantCode(t *testing.T, err error, code progress.Code) {
	t.Helper()

	if code == accepted {
		if err != nil {
			t.Fatalf("got %v, want no error", err)
		}

		return
	}

	wantRefusal(t, err, code)
}
