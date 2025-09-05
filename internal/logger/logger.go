package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/security"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// Global logger instance.
	log *zap.SugaredLogger

	// Atom for dynamic log level changes.
	atom zap.AtomicLevel
)

// Logger type alias for consistent usage across the codebase.
type Logger = *zap.SugaredLogger

// Config holds logger configuration.
type Config struct {
	Level      string
	Debug      bool
	Verbose    bool
	Trace      bool
	NoLog      bool
	LogDir     string
	BlocName   string
	Command    string
	RequestID  string
	DirectorID string
}

// Initialize sets up the global logger.
func Initialize(cfg Config) error {
	// Determine log level
	level := determineLogLevel(cfg)
	atom = zap.NewAtomicLevelAt(level)

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Always log to file (JSON) only; stdout/stderr reserved for user UX
	cores := []zapcore.Core{}

	fileCore, err := createFileCore(cfg, encoderConfig)
	if err != nil {
		return fmt.Errorf("failed to create file logger: %w", err)
	}

	cores = append(cores, fileCore)

	// Create the logger
	core := zapcore.NewTee(cores...)
	zapLogger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	// Add context fields
	if cfg.RequestID != "" {
		zapLogger = zapLogger.With(zap.String("request_id", cfg.RequestID))
	}

	if cfg.DirectorID != "" {
		zapLogger = zapLogger.With(zap.String("director_id", cfg.DirectorID))
	}

	if cfg.BlocName != "" {
		zapLogger = zapLogger.With(zap.String("bloc", cfg.BlocName))
	}

	log = zapLogger.Sugar()

	// Replace global logger
	zap.ReplaceGlobals(zapLogger)

	return nil
}

// createFileCore creates a file logging core.
func createFileCore(cfg Config, encoderConfig zapcore.EncoderConfig) (zapcore.Core, error) {
	// Place logs under {LogDir}/{command}/{timestamp}.log
	baseDir := cfg.LogDir
	if baseDir == "" {
		// Fallback to ~/.ocfp/log if not provided
		if home, err := os.UserHomeDir(); err == nil {
			baseDir = filepath.Join(home, ".ocfp", "log")
		}
	}
	// Ensure per-command subdirectory
	dir := filepath.Join(baseDir, cfg.Command)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}

	// Generate filename by timestamp (and optional request ID)
	timestamp := time.Now().Format("20060102-150405")

	filename := timestamp + ".log"
	if cfg.RequestID != "" {
		filename = fmt.Sprintf("%s-%s.log", timestamp, cfg.RequestID)
	}

	logPath := filepath.Join(dir, filename)

	// Open log file
	if err := security.ValidatePath(logPath); err != nil {
		return nil, fmt.Errorf("invalid log path: %w", err)
	}

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600) // #nosec G304 - logPath is validated above
	if err != nil {
		return nil, err
	}

	// Create JSON encoder for file output
	jsonEncoder := zapcore.NewJSONEncoder(encoderConfig)

	return zapcore.NewCore(jsonEncoder, zapcore.AddSync(file), atom), nil
}

// determineLogLevel determines the log level based on configuration.
func determineLogLevel(cfg Config) zapcore.Level {
	if cfg.Trace {
		return zapcore.DebugLevel // Zap doesn't have trace, use debug
	}

	if cfg.Debug {
		return zapcore.DebugLevel
	}

	if cfg.Verbose {
		return zapcore.InfoLevel
	}

	// Parse configured level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// SetLevel dynamically changes the log level.
func SetLevel(level string) {
	var zapLevel zapcore.Level
	err := zapLevel.UnmarshalText([]byte(level))
	if err != nil {
		return
	}

	atom.SetLevel(zapLevel)
}

// Get returns the global logger instance.
func Get() *zap.SugaredLogger {
	if log == nil {
		// Initialize with defaults if not already initialized
		err := Initialize(Config{Level: "info"})
		if err != nil {
			// Fallback to console logger if initialization fails
			panic(fmt.Errorf("failed to initialize logger: %w", err))
		}
	}

	return log
}

// Debug logs a debug message.
func Debug(args ...interface{}) {
	Get().Debug(args...)
}

// Debugf logs a formatted debug message.
func Debugf(format string, args ...interface{}) {
	Get().Debugf(format, args...)
}

// Info logs an info message.
func Info(args ...interface{}) {
	Get().Info(args...)
}

// Infof logs a formatted info message.
func Infof(format string, args ...interface{}) {
	Get().Infof(format, args...)
}

// Warn logs a warning message.
func Warn(args ...interface{}) {
	Get().Warn(args...)
}

// Warnf logs a formatted warning message.
func Warnf(format string, args ...interface{}) {
	Get().Warnf(format, args...)
}

// Error logs an error message.
func Error(args ...interface{}) {
	Get().Error(args...)
}

// Errorf logs a formatted error message.
func Errorf(format string, args ...interface{}) {
	Get().Errorf(format, args...)
}

// Fatal logs a fatal message and exits.
func Fatal(args ...interface{}) {
	Get().Fatal(args...)
}

// Fatalf logs a formatted fatal message and exits.
func Fatalf(format string, args ...interface{}) {
	Get().Fatalf(format, args...)
}

// With creates a child logger with additional fields.
func With(fields ...interface{}) *zap.SugaredLogger {
	return Get().With(fields...)
}

// WithRequestID creates a logger with request ID context.
func WithRequestID(requestID string) *zap.SugaredLogger {
	return Get().With("request_id", requestID)
}

// WithBloc creates a logger with bloc context.
func WithBloc(blocName string) *zap.SugaredLogger {
	return Get().With("bloc", blocName)
}

// WithOperation creates a logger with operation context.
func WithOperation(operation string) *zap.SugaredLogger {
	return Get().With("operation", operation)
}

// Sync flushes any buffered log entries.
func Sync() error {
	if log != nil {
		return log.Sync()
	}

	return nil
}

// ArchiveOldLogs compresses and archives logs older than specified days.
func ArchiveOldLogs(logDir string, daysOld int) error {
	cutoff := time.Now().AddDate(0, 0, -daysOld)
	archiveDir := filepath.Join(logDir, "archive")

	// Create archive directory if it doesn't exist
	if err := os.MkdirAll(archiveDir, 0750); err != nil {
		return err
	}

	// Walk through log directory
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Check if file is a log file
		if !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		// Get file info
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Check if file is old enough to archive
		if info.ModTime().Before(cutoff) {
			// TODO: Implement compression and move to archive
			// For now, just log that we would archive it
			Debugf("Would archive old log: %s", entry.Name())
		}
	}

	return nil
}
