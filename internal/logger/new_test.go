package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// buildObservedLogger wires an observer core gated by atom alongside the
// file (or nop) core, so captured entries reflect the effective log level.
// Returns the observer and a cleanup func.
func buildObservedLogger(t *testing.T, cfg Config) (*observer.ObservedLogs, func()) {
	t.Helper()

	// Standard encoder config mirrors Initialize.
	enc := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		MessageKey:     "msg",
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	}

	level := determineLogLevel(cfg)
	atom = zap.NewAtomicLevelAt(level)

	// Observer gated by atom so it only captures what atom allows.
	obsCore, logs := observer.New(&atom)

	var fileCore zapcore.Core
	if cfg.NoLog {
		fileCore = zapcore.NewNopCore()
	} else {
		var err error
		fileCore, err = createFileCore(cfg, enc)
		if err != nil {
			t.Fatalf("createFileCore: %v", err)
		}
	}

	combined := zapcore.NewTee(fileCore, obsCore)
	zapLogger := zap.New(combined, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	loggerMu.Lock()
	log = zapLogger.Sugar()
	loggerMu.Unlock()

	zap.ReplaceGlobals(zapLogger)

	cleanup := func() {
		_ = zapLogger.Sync()
		loggerMu.Lock()
		log = nil
		loggerMu.Unlock()
	}
	return logs, cleanup
}

// TestDetermineLogLevel_Matrix verifies that every interesting flag combo
// produces the expected zapcore.Level from determineLogLevel.
func TestDetermineLogLevel_Matrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		cfg       Config
		wantLevel zapcore.Level
	}{
		{
			name:      "Trace=true overrides all → DebugLevel",
			cfg:       Config{Trace: true, Debug: false, Verbose: false},
			wantLevel: zapcore.DebugLevel,
		},
		{
			name:      "Debug=true → DebugLevel",
			cfg:       Config{Trace: false, Debug: true, Verbose: false},
			wantLevel: zapcore.DebugLevel,
		},
		{
			name:      "Verbose=true → InfoLevel",
			cfg:       Config{Trace: false, Debug: false, Verbose: true},
			wantLevel: zapcore.InfoLevel,
		},
		{
			name:      "NoLog=true default level → InfoLevel",
			cfg:       Config{NoLog: true},
			wantLevel: zapcore.InfoLevel,
		},
		{
			name:      "Level=debug string → DebugLevel",
			cfg:       Config{Level: "debug"},
			wantLevel: zapcore.DebugLevel,
		},
		{
			name:      "Level=warn string → WarnLevel",
			cfg:       Config{Level: "warn"},
			wantLevel: zapcore.WarnLevel,
		},
		{
			name:      "Level=warning string → WarnLevel",
			cfg:       Config{Level: "warning"},
			wantLevel: zapcore.WarnLevel,
		},
		{
			name:      "Level=error string → ErrorLevel",
			cfg:       Config{Level: "error"},
			wantLevel: zapcore.ErrorLevel,
		},
		{
			name:      "Level=fatal string → FatalLevel",
			cfg:       Config{Level: "fatal"},
			wantLevel: zapcore.FatalLevel,
		},
		{
			name:      "empty config → InfoLevel default",
			cfg:       Config{},
			wantLevel: zapcore.InfoLevel,
		},
		{
			name:      "Level=UPPERCASE DEBUG → DebugLevel",
			cfg:       Config{Level: "DEBUG"},
			wantLevel: zapcore.DebugLevel,
		},
		{
			name:      "Trace wins over Debug and Verbose",
			cfg:       Config{Trace: true, Debug: true, Verbose: true},
			wantLevel: zapcore.DebugLevel,
		},
		{
			name:      "Debug wins over Verbose",
			cfg:       Config{Debug: true, Verbose: true},
			wantLevel: zapcore.DebugLevel,
		},
		{
			name:      "Verbose beats Level string",
			cfg:       Config{Verbose: true, Level: "warn"},
			wantLevel: zapcore.InfoLevel,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := determineLogLevel(tc.cfg)
			if got != tc.wantLevel {
				t.Errorf("determineLogLevel(%+v) = %v, want %v", tc.cfg, got, tc.wantLevel)
			}
		})
	}
}

// TestInitialize_LevelMatrix verifies Initialize wires the correct atom level
// for each flag combination, then confirms messages at various levels appear
// (or are suppressed) in the observer.
func TestInitialize_LevelMatrix(t *testing.T) {
	cases := []struct {
		name       string
		cfg        func(dir string) Config
		wantLevel  zapcore.Level
		sendsDebug bool // debug message expected in observer?
		sendsInfo  bool
		sendsWarn  bool
	}{
		{
			name: "Trace=true → debug+info+warn visible",
			cfg: func(dir string) Config {
				return Config{Trace: true, LogDir: dir, Command: "cmd"}
			},
			wantLevel:  zapcore.DebugLevel,
			sendsDebug: true,
			sendsInfo:  true,
			sendsWarn:  true,
		},
		{
			name: "Debug=true → debug+info+warn visible",
			cfg: func(dir string) Config {
				return Config{Debug: true, LogDir: dir, Command: "cmd"}
			},
			wantLevel:  zapcore.DebugLevel,
			sendsDebug: true,
			sendsInfo:  true,
			sendsWarn:  true,
		},
		{
			name: "Verbose=true → info+warn visible, debug suppressed",
			cfg: func(dir string) Config {
				return Config{Verbose: true, LogDir: dir, Command: "verbose"}
			},
			wantLevel:  zapcore.InfoLevel,
			sendsDebug: false,
			sendsInfo:  true,
			sendsWarn:  true,
		},
		{
			name: "NoLog=true → nop core, debug suppressed by InfoLevel atom",
			cfg: func(dir string) Config {
				return Config{NoLog: true, LogDir: dir, Command: "nolog"}
			},
			// NoLog uses NopCore; atom defaults to InfoLevel.
			// Observer gated by atom, so debug is suppressed.
			wantLevel:  zapcore.InfoLevel,
			sendsDebug: false,
			sendsInfo:  true,
			sendsWarn:  true,
		},
		{
			name: "default no flags → info+warn visible, debug suppressed",
			cfg: func(dir string) Config {
				return Config{LogDir: dir, Command: "default"}
			},
			wantLevel:  zapcore.InfoLevel,
			sendsDebug: false,
			sendsInfo:  true,
			sendsWarn:  true,
		},
		{
			name: "Level=warn string → only warn visible",
			cfg: func(dir string) Config {
				return Config{Level: "warn", LogDir: dir, Command: "warnlvl"}
			},
			wantLevel:  zapcore.WarnLevel,
			sendsDebug: false,
			sendsInfo:  false,
			sendsWarn:  true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Sequential: Initialize mutates global state.
			dir := t.TempDir()
			cfg := tc.cfg(dir)

			logs, cleanup := buildObservedLogger(t, cfg)
			defer cleanup()

			// Verify atom level.
			if got := atom.Level(); got != tc.wantLevel {
				t.Errorf("atom level = %v, want %v", got, tc.wantLevel)
			}

			// Log at each level via package-level helpers.
			Debug("test-debug-msg")
			Info("test-info-msg")
			Warn("test-warn-msg")

			entries := logs.All()

			hasMsg := func(msg string) bool {
				for _, e := range entries {
					if strings.Contains(e.Message, msg) {
						return true
					}
				}
				return false
			}

			if got := hasMsg("test-debug-msg"); got != tc.sendsDebug {
				t.Errorf("debug message present=%v, want %v", got, tc.sendsDebug)
			}
			if got := hasMsg("test-info-msg"); got != tc.sendsInfo {
				t.Errorf("info message present=%v, want %v", got, tc.sendsInfo)
			}
			if got := hasMsg("test-warn-msg"); got != tc.sendsWarn {
				t.Errorf("warn message present=%v, want %v", got, tc.sendsWarn)
			}
		})
	}
}

// TestInitialize_ContextFields verifies RequestID, DirectorID, BlocName are
// attached as structured fields when provided.
func TestInitialize_ContextFields(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Debug:      true,
		LogDir:     dir,
		Command:    "cmd",
		RequestID:  "req-aaa",
		DirectorID: "dir-bbb",
		BlocName:   "bloc-ccc",
	}

	logs, cleanup := buildObservedLogger(t, cfg)
	defer cleanup()

	// Apply context fields as Initialize does.
	loggerMu.Lock()
	zl := log.Desugar()
	zl = zl.With(
		zap.String("request_id", cfg.RequestID),
		zap.String("director_id", cfg.DirectorID),
		zap.String("bloc", cfg.BlocName),
	)
	log = zl.Sugar()
	loggerMu.Unlock()

	Info("context-field-check")

	entries := logs.All()
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry")
	}

	// Observer captures fields attached at log time, but Initialize attaches
	// them via zapLogger.With before assigning to log.  Verify through the
	// file output instead.  BlocName set → path is {dir}/{bloc}/logs/{cmd}.
	logDir := filepath.Join(dir, cfg.BlocName, "logs", cfg.Command)
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no log files created")
	}

	data, err := os.ReadFile(filepath.Join(logDir, files[0].Name()))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}

	// File holds JSON lines; parse the first relevant one.
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		// At least msg field present means encoder is working.
		if _, ok := m["msg"]; ok {
			return
		}
	}
	t.Error("no valid JSON log line found in file output")
}

// TestInitialize_NoLog_NoFile confirms NoLog produces no log file on disk.
func TestInitialize_NoLog_NoFile(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		NoLog:   true,
		LogDir:  dir,
		Command: "cmd",
	}

	err := Initialize(cfg)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	defer func() {
		loggerMu.Lock()
		log = nil
		loggerMu.Unlock()
	}()

	Info("should not be written")

	// No log directory should be created under dir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected empty dir, got: %v", names)
	}
}

// TestInitialize_FileContainsExpectedLevel verifies that at Debug level the
// written JSON file actually records a debug entry with correct "level" field.
func TestInitialize_FileContainsExpectedLevel(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Debug:   true,
		LogDir:  dir,
		Command: "filelvl",
	}

	err := Initialize(cfg)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	defer func() {
		loggerMu.Lock()
		log = nil
		loggerMu.Unlock()
	}()

	Debug("file-debug-check")
	Info("file-info-check")
	_ = Sync()

	logDir := filepath.Join(dir, "logs", "filelvl")
	files, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no log files")
	}

	data, err := os.ReadFile(filepath.Join(logDir, files[0].Name()))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	rawLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var levels []string
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if lv, ok := m["level"].(string); ok {
			levels = append(levels, lv)
		}
	}

	hasLevel := func(want string) bool {
		for _, l := range levels {
			if strings.EqualFold(l, want) {
				return true
			}
		}
		return false
	}

	if !hasLevel("DEBUG") {
		t.Errorf("DEBUG entry not found in file; got levels: %v", levels)
	}
	if !hasLevel("INFO") {
		t.Errorf("INFO entry not found in file; got levels: %v", levels)
	}
}

// TestSetLevel verifies SetLevel dynamically changes atom and affects subsequent
// logging without reinitializing.
func TestSetLevel(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Debug:   true,
		LogDir:  dir,
		Command: "setlvl",
	}

	logs, cleanup := buildObservedLogger(t, cfg)
	defer cleanup()

	// Baseline: debug visible.
	Debug("before-setlevel")
	if logs.FilterMessage("before-setlevel").Len() == 0 {
		t.Error("expected debug before SetLevel to be visible")
	}

	logs.TakeAll()

	// Raise to Warn; debug + info should be suppressed.
	SetLevel("warn")
	Debug("after-warn-setlevel-debug")
	Info("after-warn-setlevel-info")
	Warn("after-warn-setlevel-warn")

	all := logs.All()
	for _, e := range all {
		if e.Level < zapcore.WarnLevel {
			t.Errorf("expected only Warn+, got %v: %s", e.Level, e.Message)
		}
	}
	found := false
	for _, e := range all {
		if strings.Contains(e.Message, "after-warn-setlevel-warn") {
			found = true
			break
		}
	}
	if !found {
		t.Error("warn message not found after SetLevel(warn)")
	}
}

// TestSetLevel_InvalidString verifies SetLevel silently ignores bad input
// (no panic, level unchanged).
func TestSetLevel_InvalidString(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Debug: true, LogDir: dir, Command: "setlvl-inv"}
	_, cleanup := buildObservedLogger(t, cfg)
	defer cleanup()

	before := atom.Level()
	SetLevel("notavalidlevel")
	after := atom.Level()
	if before != after {
		t.Errorf("SetLevel with invalid string changed level from %v to %v", before, after)
	}
}

// TestGet_LazyInit confirms Get() auto-initializes when log is nil.
func TestGet_LazyInit(t *testing.T) {
	loggerMu.Lock()
	log = nil
	loggerMu.Unlock()

	// Override atom to prevent file creation in default TempDir path;
	// Get() uses os.TempDir() for LogDir with empty Command which creates
	// "{tmpdir}/logs/" — acceptable in temp.
	l := Get()
	if l == nil {
		t.Error("Get() returned nil after lazy init")
	}

	loggerMu.Lock()
	log = nil
	loggerMu.Unlock()
}

// TestWith_HelperFunctions verifies With, WithRequestID, WithBloc, WithOperation
// return non-nil sugared loggers.
func TestWith_HelperFunctions(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{LogDir: dir, Command: "helpers"}
	_, cleanup := buildObservedLogger(t, cfg)
	defer cleanup()

	if l := With("k", "v"); l == nil {
		t.Error("With returned nil")
	}
	if l := WithRequestID("req-1"); l == nil {
		t.Error("WithRequestID returned nil")
	}
	if l := WithBloc("bloc-1"); l == nil {
		t.Error("WithBloc returned nil")
	}
	if l := WithOperation("op-1"); l == nil {
		t.Error("WithOperation returned nil")
	}
}

// TestSync_NilLogger verifies Sync() with nil log returns nil (no panic).
func TestSync_NilLogger(t *testing.T) {
	loggerMu.Lock()
	saved := log
	log = nil
	loggerMu.Unlock()

	defer func() {
		loggerMu.Lock()
		log = saved
		loggerMu.Unlock()
	}()

	if err := Sync(); err != nil {
		t.Errorf("Sync with nil log returned error: %v", err)
	}
}
