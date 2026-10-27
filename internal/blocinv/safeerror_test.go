package blocinv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeToken = "FAKE-TOKEN-DO-NOT-USE"

// malformedConfig is invalid YAML whose offending line holds a fake secret.
const malformedConfig = "blocs:\n  lab:\n    provider: pve\n    auth_token: [" + fakeToken + "\n    region: lab\n"

// captureDebugLog points the global logger at a temp directory at debug level
// and returns a func that reads back everything it has written.
func captureDebugLog(t *testing.T) func() string {
	t.Helper()

	dir := t.TempDir()

	require.NoError(t, logger.Initialize(logger.Config{
		Level: "debug", Debug: true, LogDir: dir, BlocName: "logcheck", Command: "test",
	}))
	t.Cleanup(func() { _ = logger.Initialize(logger.Config{NoLog: true}) })

	return func() string {
		_ = logger.Sync()

		var all strings.Builder

		walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}

			all.Write(data)

			return nil
		})
		require.NoError(t, walkErr)

		return all.String()
	}
}

func TestSafeMessage_ParseErrorsBecomeAFixedPhrase(t *testing.T) {
	var target map[string]any

	yamlErr := yaml.Unmarshal([]byte(malformedConfig), &target)
	require.Error(t, yamlErr)
	require.Contains(t, yamlErr.Error(), fakeToken, "the raw parser error quotes the file")

	jsonErr := json.Unmarshal([]byte(`{"token": "`+fakeToken+`",,`), &target)
	require.Error(t, jsonErr)

	for name, err := range map[string]error{
		"yaml":         yamlErr,
		"wrapped yaml": fmt.Errorf("failed to unmarshal config: %w", yamlErr),
		"json":         jsonErr,
		"wrapped json": fmt.Errorf("loading: %w", jsonErr),
	} {
		got := SafeMessage(err, "/cfg/config.yml")
		assert.NotContains(t, got, fakeToken, name)
		assert.Equal(t, "/cfg/config.yml cannot be parsed (file contents are not shown)", got, name)
	}

	assert.Equal(t, "file cannot be parsed (file contents are not shown)", SafeMessage(yamlErr, ""))
}

func TestSafeMessage_OtherErrorsKeepOnlyTheirFirstLine(t *testing.T) {
	err := errors.New("first line\nsecond line holds " + fakeToken + "\nthird line")

	got := SafeMessage(err, "/cfg/config.yml")
	assert.Equal(t, "first line", got)

	assert.Equal(t, "", SafeMessage(nil, "/cfg/config.yml"))

	long := SafeMessage(errors.New(strings.Repeat("x", 1000)), "")
	assert.LessOrEqual(t, len(long), 260)
}

func TestSafeMessage_SourceQuotingTextWithoutAParserTypeIsStillParsing(t *testing.T) {
	flattened := fmt.Errorf("%s", "[4:17] sequence end token ']' not found\n   3 | provider: pve\n>  4 |     auth_token: ["+fakeToken+"\n")

	assert.NotContains(t, SafeMessage(flattened, "/cfg/config.yml"), fakeToken)
}

func TestSanitize_KeepsTheCauseForErrorsIs(t *testing.T) {
	cause := &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}

	err := Sanitize(fmt.Errorf("reading: %w", cause), "/x")
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Equal(t, "reading: open /x: permission denied", err.Error())
	assert.NoError(t, Sanitize(nil, "/x"))
}

func TestCollect_MalformedConfigNeverReachesTheLog(t *testing.T) {
	readLog := captureDebugLog(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "config.yml", malformedConfig)

	entries, err := Collect(Options{ConfigDirs: []string{dir}})
	require.NoError(t, err)
	assert.Empty(t, entries)

	logged := readLog()
	assert.Contains(t, logged, path, "the log still names the file it skipped")
	assert.NotContains(t, logged, fakeToken)
}

func TestCollect_MalformedStateNeverReachesTheLogOrTheEntry(t *testing.T) {
	readLog := captureDebugLog(t)
	home := t.TempDir()
	writeState(t, home, "bad", `{"provider": "`+fakeToken+`",,, [unterminated`)

	entries, err := Collect(Options{StateHomes: []string{home}})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.True(t, entries[0].Broken)
	assert.NotContains(t, entries[0].BrokenError, fakeToken)

	logged := readLog()
	assert.Contains(t, logged, "bad.json")
	assert.NotContains(t, logged, fakeToken)
}

func TestSummarize_ParseFailureErrorNeverCarriesFileContent(t *testing.T) {
	entry := collectOne(t, "", richState)
	require.NoError(t, os.WriteFile(entry.StatePath, []byte(`{"provider": "`+fakeToken+`",,, [unterminated`), 0o600))

	_, err := Summarize(entry)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), fakeToken)
	assert.Contains(t, err.Error(), "lab.json")
}
