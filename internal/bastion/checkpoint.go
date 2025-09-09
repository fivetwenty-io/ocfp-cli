package bastion

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// File permissions.
	checkpointDirMode  = 0750
	checkpointFileMode = 0600

	// Progress calculation.
	percentageMultiplier = 100
)

// Checkpoint validation errors.
func ErrUnsupportedCheckpointVersion(version string) error {
	return fmt.Errorf("unsupported checkpoint version: %s", version) //nolint:err113 // dynamic error with context
}

func ErrCheckpointBlocNameMismatch(got, expected string) error {
	return fmt.Errorf("checkpoint bloc name mismatch: got %s, expected %s", got, expected) //nolint:err113 // dynamic error with context
}

func ErrCheckpointProviderMismatch(got, expected string) error {
	return fmt.Errorf("checkpoint provider mismatch: got %s, expected %s", got, expected) //nolint:err113 // dynamic error with context
}

func ErrCheckpointTooOld(age string) error {
	return fmt.Errorf("checkpoint is too old: %s", age) //nolint:err113 // dynamic error with context
}

func ErrInvalidCheckpointSteps(completed, total int) error {
	return fmt.Errorf("invalid checkpoint: completed steps (%d) > total steps (%d)", completed, total) //nolint:err113 // dynamic error with context
}

// CheckpointManager handles saving and loading provisioning state.
type CheckpointManager struct {
	config        *config.Config
	checkpointDir string
	log           logger.Logger
}

// CheckpointData represents saved provisioning state.
type CheckpointData struct {
	BlocName        string                 `json:"blocName"`
	Provider        string                 `json:"provider"`
	StartTime       time.Time              `json:"startTime"`
	LastUpdate      time.Time              `json:"lastUpdate"`
	TotalSteps      int                    `json:"totalSteps"`
	CompletedSteps  int                    `json:"completedSteps"`
	CurrentStep     string                 `json:"currentStep"`
	CompletedPhases map[string]bool        `json:"completedPhases"`
	FailedPhases    map[string]string      `json:"failedPhases"`
	SkippedPhases   map[string]string      `json:"skippedPhases"`
	Metadata        map[string]interface{} `json:"metadata"`
	Version         string                 `json:"version"`
}

// NewCheckpointManager creates a new checkpoint manager.
func NewCheckpointManager(cfg *config.Config) *CheckpointManager {
	checkpointDir := filepath.Join(os.Getenv("HOME"), ".ocfp", "checkpoints")

	return &CheckpointManager{
		config:        cfg,
		checkpointDir: checkpointDir,
		log:           logger.Get(),
	}
}

// Save saves the current provisioning state.
func (cm *CheckpointManager) Save(progress *ProvisioningProgress, metadata map[string]interface{}) error {
	// Ensure checkpoint directory exists
	err := os.MkdirAll(cm.checkpointDir, checkpointDirMode)
	if err != nil {
		return fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	checkpoint := &CheckpointData{
		BlocName:        cm.config.Name,
		Provider:        cm.config.Provider,
		StartTime:       progress.StartTime,
		LastUpdate:      time.Now(),
		TotalSteps:      progress.TotalSteps,
		CompletedSteps:  progress.CompletedSteps,
		CurrentStep:     progress.CurrentStep,
		CompletedPhases: progress.Checkpoints,
		FailedPhases:    make(map[string]string),
		SkippedPhases:   make(map[string]string),
		Metadata:        metadata,
		Version:         "1.0", // Checkpoint format version
	}

	// Add failed phases information
	for _, err := range progress.Errors {
		bastionErr := &BastionError{
			Type:         "",
			Phase:        "",
			Command:      "",
			Message:      "",
			Cause:        nil,
			Retryable:    false,
			Suggestions:  nil,
			AttemptCount: 0,
		}
		if errors.As(err, &bastionErr) {
			checkpoint.FailedPhases[bastionErr.Phase] = bastionErr.Message
		}
	}

	checkpointFile := cm.getCheckpointPath()

	// Write checkpoint data as JSON
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint data: %w", err)
	}

	err = os.WriteFile(checkpointFile, data, checkpointFileMode)
	if err != nil {
		return fmt.Errorf("failed to write checkpoint file: %w", err)
	}

	cm.log.Debug("Checkpoint saved",
		"file", checkpointFile,
		"completed_steps", checkpoint.CompletedSteps,
		"total_steps", checkpoint.TotalSteps)

	return nil
}

// Load loads saved provisioning state.
func (cm *CheckpointManager) Load() (*CheckpointData, error) {
	checkpointFile := cm.getCheckpointPath()

	// Check if checkpoint file exists
	_, err := os.Stat(checkpointFile)
	if os.IsNotExist(err) {
		cm.log.Debug("No checkpoint file found", "file", checkpointFile)

		return nil, nil //nolint:nilnil // explicit: no checkpoint is not an error
	}

	// Read checkpoint file
	data, err := os.ReadFile(checkpointFile) // #nosec G304 - checkpointFile is constructed from safe paths
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint file: %w", err)
	}

	// Parse checkpoint data
	var checkpoint CheckpointData

	err = json.Unmarshal(data, &checkpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint data: %w", err)
	}

	// Validate checkpoint data
	err = cm.validateCheckpoint(&checkpoint)
	if err != nil {
		cm.log.Warn("Invalid checkpoint data, ignoring", "error", err.Error())

		return nil, nil //nolint:nilnil // invalid checkpoint treated as absence
	}

	cm.log.Info("Checkpoint loaded",
		"file", checkpointFile,
		"completed_steps", checkpoint.CompletedSteps,
		"total_steps", checkpoint.TotalSteps,
		"age", time.Since(checkpoint.LastUpdate).String())

	return &checkpoint, nil
}

// Clear removes the checkpoint file.
func (cm *CheckpointManager) Clear() error {
	checkpointFile := cm.getCheckpointPath()

	_, err := os.Stat(checkpointFile)
	if os.IsNotExist(err) {
		return nil // Nothing to clear
	}

	err = os.Remove(checkpointFile)
	if err != nil {
		return fmt.Errorf("failed to remove checkpoint file: %w", err)
	}

	cm.log.Debug("Checkpoint cleared", "file", checkpointFile)

	return nil
}

// RestoreProgress restores ProvisioningProgress from checkpoint data.
func (cm *CheckpointManager) RestoreProgress(checkpoint *CheckpointData) *ProvisioningProgress {
	if checkpoint == nil {
		return &ProvisioningProgress{
			TotalSteps:     0,
			CompletedSteps: 0,
			CurrentStep:    "",
			StartTime:      time.Now(),
			Errors:         nil,
			Checkpoints:    make(map[string]bool),
		}
	}

	return &ProvisioningProgress{
		TotalSteps:     checkpoint.TotalSteps,
		CompletedSteps: checkpoint.CompletedSteps,
		CurrentStep:    checkpoint.CurrentStep,
		StartTime:      checkpoint.StartTime,
		Errors:         []error{}, // Reset errors on resume
		Checkpoints:    checkpoint.CompletedPhases,
	}
}

// IsPhaseCompleted checks if a phase was completed in a previous run.
func (cm *CheckpointManager) IsPhaseCompleted(checkpoint *CheckpointData, phase string) bool {
	if checkpoint == nil || checkpoint.CompletedPhases == nil {
		return false
	}

	return checkpoint.CompletedPhases[phase]
}

// MarkPhaseCompleted marks a phase as completed.
func (cm *CheckpointManager) MarkPhaseCompleted(progress *ProvisioningProgress, phase string) {
	if progress.Checkpoints == nil {
		progress.Checkpoints = make(map[string]bool)
	}

	progress.Checkpoints[phase] = true
}

// GetCompletionStatus returns completion status information.
func (cm *CheckpointManager) GetCompletionStatus(checkpoint *CheckpointData) map[string]interface{} {
	if checkpoint == nil {
		return map[string]interface{}{
			"completed":       false,
			"progress":        0.0,
			"completed_steps": 0,
			"total_steps":     0,
		}
	}

	progress := 0.0
	if checkpoint.TotalSteps > 0 {
		progress = float64(checkpoint.CompletedSteps) / float64(checkpoint.TotalSteps) * percentageMultiplier
	}

	return map[string]interface{}{
		"completed":       checkpoint.CompletedSteps >= checkpoint.TotalSteps,
		"progress":        progress,
		"completed_steps": checkpoint.CompletedSteps,
		"total_steps":     checkpoint.TotalSteps,
		"current_step":    checkpoint.CurrentStep,
		"start_time":      checkpoint.StartTime,
		"last_update":     checkpoint.LastUpdate,
		"duration":        checkpoint.LastUpdate.Sub(checkpoint.StartTime).String(),
	}
}

// ListCheckpoints lists all available checkpoints.
func (cm *CheckpointManager) ListCheckpoints() ([]CheckpointData, error) {
	_, err := os.Stat(cm.checkpointDir)
	if os.IsNotExist(err) {
		return []CheckpointData{}, nil
	}

	entries, err := os.ReadDir(cm.checkpointDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint directory: %w", err)
	}

	checkpoints := make([]CheckpointData, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		checkpointPath := filepath.Join(cm.checkpointDir, entry.Name())

		data, err := os.ReadFile(checkpointPath) // #nosec G304 - checkpointPath is constructed from safe paths
		if err != nil {
			cm.log.Warn("Failed to read checkpoint file",
				"file", entry.Name(),
				"error", err.Error())

			continue
		}

		var checkpoint CheckpointData

		err = json.Unmarshal(data, &checkpoint)
		if err != nil {
			cm.log.Warn("Failed to parse checkpoint file",
				"file", entry.Name(),
				"error", err.Error())

			continue
		}

		checkpoints = append(checkpoints, checkpoint)
	}

	return checkpoints, nil
}

// CleanupOldCheckpoints removes checkpoint files older than the specified duration.
func (cm *CheckpointManager) CleanupOldCheckpoints(maxAge time.Duration) error {
	checkpoints, err := cm.ListCheckpoints()
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-maxAge)

	for _, checkpoint := range checkpoints {
		if checkpoint.LastUpdate.Before(cutoff) {
			filename := fmt.Sprintf("bastion-%s-%s.json", checkpoint.BlocName, checkpoint.Provider)
			checkpointPath := filepath.Join(cm.checkpointDir, filename)

			err := os.Remove(checkpointPath)
			if err != nil {
				cm.log.Warn("Failed to remove old checkpoint",
					"file", filename,
					"error", err.Error())
			} else {
				cm.log.Debug("Removed old checkpoint",
					"file", filename,
					"age", time.Since(checkpoint.LastUpdate).String())
			}
		}
	}

	return nil
}

// getCheckpointPath returns the path to the checkpoint file.
func (cm *CheckpointManager) getCheckpointPath() string {
	filename := fmt.Sprintf("bastion-%s-%s.json", cm.config.Name, cm.config.Provider)

	return filepath.Join(cm.checkpointDir, filename)
}

// validateCheckpoint validates checkpoint data consistency.
func (cm *CheckpointManager) validateCheckpoint(checkpoint *CheckpointData) error {
	// Check version compatibility
	if checkpoint.Version != "1.0" {
		return ErrUnsupportedCheckpointVersion(checkpoint.Version)
	}

	// Check basic data consistency
	if checkpoint.BlocName != cm.config.Name {
		return ErrCheckpointBlocNameMismatch(checkpoint.BlocName, cm.config.Name)
	}

	if checkpoint.Provider != cm.config.Provider {
		return ErrCheckpointProviderMismatch(checkpoint.Provider, cm.config.Provider)
	}

	// Check if checkpoint is not too old (24 hours)
	if time.Since(checkpoint.LastUpdate) > 24*time.Hour {
		return ErrCheckpointTooOld(time.Since(checkpoint.LastUpdate).String())
	}

	// Check data integrity
	if checkpoint.CompletedSteps > checkpoint.TotalSteps {
		return ErrInvalidCheckpointSteps(checkpoint.CompletedSteps, checkpoint.TotalSteps)
	}

	return nil
}
