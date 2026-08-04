package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"go.uber.org/zap/zapcore"
)

// TestCreateFileCore_SimpleBlocCommand tests log path for simple command with bloc
func TestCreateFileCore_SimpleBlocCommand(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     tempDir,
		BlocName:   "test-bloc",
		Command:    "bootstrap",
		Subcommand: "",
		RequestID:  "test-123",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	// Expected: {tempDir}/test-bloc/logs/bootstrap/{timestamp}.log
	expectedDir := filepath.Join(tempDir, "test-bloc", "logs", "bootstrap")
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("Expected directory not created: %s", expectedDir)
	}

	// Check that a log file was created
	files, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 log file, got %d", len(files))
	}

	if len(files) > 0 && !strings.HasSuffix(files[0].Name(), ".log") {
		t.Errorf("Expected .log file, got %s", files[0].Name())
	}
}

// TestCreateFileCore_SimpleNoBlocCommand tests log path for simple command without bloc
func TestCreateFileCore_SimpleNoBlocCommand(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     tempDir,
		BlocName:   "",
		Command:    "teardown",
		Subcommand: "",
		RequestID:  "",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	// Expected: {tempDir}/logs/teardown/{timestamp}.log
	expectedDir := filepath.Join(tempDir, "logs", "teardown")
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("Expected directory not created: %s", expectedDir)
	}

	// Check that a log file was created
	files, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 log file, got %d", len(files))
	}
}

// TestCreateFileCore_SubcommandWithBloc tests log path for subcommand with bloc
func TestCreateFileCore_SubcommandWithBloc(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     tempDir,
		BlocName:   "prod-bloc",
		Command:    "state",
		Subcommand: "sync",
		RequestID:  "",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	// Expected: {tempDir}/prod-bloc/logs/state/sync/{timestamp}.log
	expectedDir := filepath.Join(tempDir, "prod-bloc", "logs", "state", "sync")
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("Expected directory not created: %s", expectedDir)
	}

	// Check that a log file was created
	files, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 log file, got %d", len(files))
	}
}

// TestCreateFileCore_SubcommandNoBloc tests log path for subcommand without bloc
func TestCreateFileCore_SubcommandNoBloc(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     tempDir,
		BlocName:   "",
		Command:    "lb",
		Subcommand: "ops",
		RequestID:  "",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	// Expected: {tempDir}/logs/lb/ops/{timestamp}.log
	expectedDir := filepath.Join(tempDir, "logs", "lb", "ops")
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("Expected directory not created: %s", expectedDir)
	}

	// Check that a log file was created
	files, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 log file, got %d", len(files))
	}
}

// TestCreateFileCore_CustomLogDir tests log path with custom LogDir
func TestCreateFileCore_CustomLogDir(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	customLogDir := filepath.Join(tempDir, "custom-logs")

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     customLogDir,
		BlocName:   "dev-bloc",
		Command:    "vault",
		Subcommand: "export",
		RequestID:  "",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	// Expected: {customLogDir}/dev-bloc/logs/vault/export/{timestamp}.log
	expectedDir := filepath.Join(customLogDir, "dev-bloc", "logs", "vault", "export")
	if _, err := os.Stat(expectedDir); os.IsNotExist(err) {
		t.Errorf("Expected directory not created: %s", expectedDir)
	}

	// Check that a log file was created
	files, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 log file, got %d", len(files))
	}
}

// TestCreateFileCore_RequestIDInFilename tests that request ID appears in filename
func TestCreateFileCore_RequestIDInFilename(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     tempDir,
		BlocName:   "test-bloc",
		Command:    "bootstrap",
		Subcommand: "",
		RequestID:  "req-xyz-789",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	// Expected: {tempDir}/test-bloc/logs/bootstrap/{timestamp}-req-xyz-789.log
	expectedDir := filepath.Join(tempDir, "test-bloc", "logs", "bootstrap")

	files, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("Failed to read log directory: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected 1 log file, got %d", len(files))
	}

	if len(files) > 0 {
		filename := files[0].Name()
		if !strings.Contains(filename, "req-xyz-789") {
			t.Errorf("Expected request ID in filename, got: %s", filename)
		}
	}
}

// TestCreateFileCore_FallbackLogDir_ResolvesLegacyWhenStateHomeAbsent
// exercises the cfg.LogDir == "" fallback path (as hit by root.go's
// PersistentPreRunE, which never sets LogDir): with a pre-migration
// ~/.ocfp/{bloc}/logs directory present and no XDG state directory yet,
// the fallback must land the log file under the legacy directory rather
// than a fresh, empty XDG_STATE_HOME/{bloc}/logs directory that no
// resolver (GetStateDir, getBaseDir, vault.go's inception log resolution)
// would ever look at. This mirrors the dual-read pattern every sibling
// call site (e.g. vault.go's inception vault log dir) already uses.
// Not run in parallel: it mutates process-wide HOME/XDG_* env vars via
// t.Setenv.
func TestCreateFileCore_FallbackLogDir_ResolvesLegacyWhenStateHomeAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", home)

	legacyLogsDir := filepath.Join(home, ".ocfp", "test-bloc", "logs")

	err := os.MkdirAll(legacyLogsDir, 0o750)
	if err != nil {
		t.Fatalf("MkdirAll(legacyLogsDir): %v", err)
	}

	cfg := Config{
		Level:      "info",
		Debug:      false,
		Verbose:    false,
		Trace:      false,
		NoLog:      false,
		LogDir:     "",
		BlocName:   "test-bloc",
		Command:    "bootstrap",
		Subcommand: "",
		RequestID:  "",
		DirectorID: "",
	}

	encoderConfig := zapcore.EncoderConfig{
		MessageKey: "msg",
		LevelKey:   "level",
	}

	_, err = createFileCore(cfg, encoderConfig)
	if err != nil {
		t.Fatalf("createFileCore failed: %v", err)
	}

	expectedDir := filepath.Join(legacyLogsDir, "bootstrap")
	if _, statErr := os.Stat(expectedDir); os.IsNotExist(statErr) {
		t.Errorf("expected log dir under legacy path %s, not created", expectedDir)
	}

	staleNewDir := filepath.Join(config.StateHome(), "test-bloc", "logs", "bootstrap")
	if staleNewDir == expectedDir {
		t.Fatalf("test setup invalid: new and legacy dirs coincide (%s)", staleNewDir)
	}

	if _, statErr := os.Stat(staleNewDir); statErr == nil {
		t.Errorf("expected no log dir under new XDG state path %s while legacy exists", staleNewDir)
	}
}

// TestInitialize tests the Initialize function with various configs
func TestInitialize(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config with bloc and subcommand",
			cfg: Config{
				Level:      "info",
				Debug:      false,
				Verbose:    false,
				Trace:      false,
				NoLog:      false,
				LogDir:     t.TempDir(),
				BlocName:   "test-bloc",
				Command:    "state",
				Subcommand: "sync",
				RequestID:  "",
				DirectorID: "",
			},
			wantErr: false,
		},
		{
			name: "valid config without bloc",
			cfg: Config{
				Level:      "debug",
				Debug:      true,
				Verbose:    false,
				Trace:      false,
				NoLog:      false,
				LogDir:     t.TempDir(),
				BlocName:   "",
				Command:    "teardown",
				Subcommand: "",
				RequestID:  "",
				DirectorID: "",
			},
			wantErr: false,
		},
		{
			name: "valid config with trace",
			cfg: Config{
				Level:      "",
				Debug:      false,
				Verbose:    false,
				Trace:      true,
				NoLog:      false,
				LogDir:     t.TempDir(),
				BlocName:   "dev",
				Command:    "lb",
				Subcommand: "ops",
				RequestID:  "trace-test",
				DirectorID: "dir-123",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Initialize(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("Initialize() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify logger was initialized
			if err == nil {
				logger := Get()
				if logger == nil {
					t.Error("Get() returned nil logger after Initialize()")
				}
			}
		})
	}
}
