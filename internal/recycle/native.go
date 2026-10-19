package recycle

import (
	"context"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"slices"
)

// NativeJournal is an owner-only recovery record. It may contain secret user data.
type NativeJournal struct {
	AbortFrom           string            `json:"abort_from,omitempty"`
	Version             int               `json:"version"`
	Token               string            `json:"token"`
	Phase               string            `json:"phase"`
	RecordedInstanceID  string            `json:"recorded_instance_id"`
	Tags                map[string]string `json:"tags"`
	SSHUser             string            `json:"ssh_user"`
	SSHKeyPath          string            `json:"ssh_key_path"`
	VerificationCommand string            `json:"verification_command"`
	Identity            cpi.BootIdentity  `json:"identity"`
	Image               string            `json:"image"`
	ReplacementID       string            `json:"replacement_id"`
	SnapshotID          string            `json:"snapshot_id"`
}

// NativeCluster reconciles native boot operations against observed cloud state.
// Prepare and Snapshot must recover IDs by Token after lost responses. Complete
// atomically retains recovery snapshot details and removes the active journal.
type NativeCluster interface {
	Plan(ctx context.Context) (*NativeJournal, error)
	Load(ctx context.Context) (*NativeJournal, error)
	Save(ctx context.Context, j *NativeJournal) error
	Complete(ctx context.Context, j *NativeJournal) error
	Prepare(ctx context.Context, j *NativeJournal) error
	Stop(ctx context.Context, j *NativeJournal) error
	Snapshot(ctx context.Context, j *NativeJournal) error
	Switch(ctx context.Context, j *NativeJournal) error
	Start(ctx context.Context, j *NativeJournal) error
	Verify(ctx context.Context, j *NativeJournal) error
	Retire(ctx context.Context, j *NativeJournal) error
	Restore(ctx context.Context, j *NativeJournal) error
	DeleteReplacement(ctx context.Context, j *NativeJournal) error
}

// NativeEngine preserves the instance and data disks while replacing its root.
type NativeEngine struct {
	cluster NativeCluster
	pause   bool
}

func NewNative(cluster NativeCluster, stopBeforeRetire bool) *NativeEngine {
	return &NativeEngine{cluster: cluster, pause: stopBeforeRetire}
}

var errNativeJournal = errors.New("invalid native recycle journal")
var errNativeAborting = errors.New("native recycle abort in progress; resume with --abort")

func validateNativeJournal(j *NativeJournal) error {
	if j == nil || j.Version != 1 || j.Token == "" || j.Identity.InstanceID == "" || j.Identity.RootVolumeID == "" || j.Image == "" {
		return errNativeJournal
	}

	phase := j.Phase
	if phase == "abort" {
		phase = j.AbortFrom
	}

	if slices.Contains([]string{"stop", "snapshot", "switch", "start", "verify", "ready", "retire", "complete"}, phase) && j.ReplacementID == "" {
		return errNativeJournal
	}

	if slices.Contains([]string{"switch", "start", "verify", "ready", "retire", "complete"}, phase) && j.SnapshotID == "" {
		return errNativeJournal
	}

	return nil
}

func (e *NativeEngine) Run(ctx context.Context) error {
	j, err := e.loadOrPlan(ctx)
	if err != nil {
		return err
	}

	if e.pause && (j.Phase == "retire" || j.Phase == "complete") {
		return ErrPastPointOfNoReturn
	}

	if j.Phase == "abort" {
		return errNativeAborting
	}

	phases := []string{"prepare", "stop", "snapshot", "switch", "start", "verify", "ready", "retire", "complete"}

	start := slices.Index(phases, j.Phase)
	if start < 0 {
		return errNativeJournal
	}

	for _, phase := range phases[start:] {
		// Verify before recording irreversible intent, preserving abort on readiness failure.
		if phase == "retire" {
			if err = e.cluster.Verify(ctx, j); err != nil {
				return err
			}
		}

		j.Phase = phase
		if err = e.cluster.Save(ctx, j); err != nil {
			return fmt.Errorf("journal %s: %w", phase, err)
		}

		if phase == "ready" && e.pause {
			return nil
		}

		if phase == "complete" {
			return e.cluster.Complete(ctx, j)
		}

		if err = e.perform(ctx, j); err != nil {
			// Persist API handles returned alongside wait failures, without advancing intent.
			return errors.Join(fmt.Errorf("native recycle %s: %w", phase, err), e.cluster.Save(ctx, j))
		}

		if err = e.cluster.Save(ctx, j); err != nil {
			return fmt.Errorf("journal result %s: %w", phase, err)
		}
	}

	return nil
}

func (e *NativeEngine) perform(ctx context.Context, j *NativeJournal) error {
	switch j.Phase {
	case "prepare":
		return e.cluster.Prepare(ctx, j)
	case "stop":
		return e.cluster.Stop(ctx, j)
	case "snapshot":
		return e.cluster.Snapshot(ctx, j)
	case "switch":
		if j.ReplacementID == "" || j.SnapshotID == "" {
			return errNativeJournal
		}

		return e.cluster.Switch(ctx, j)
	case "start":
		return e.cluster.Start(ctx, j)
	case "verify":
		return e.cluster.Verify(ctx, j)
	case "ready":
		return nil
	case "retire":
		if j.ReplacementID == "" || j.SnapshotID == "" {
			return errNativeJournal
		}

		return e.cluster.Retire(ctx, j)
	default:
		return errNativeJournal
	}
}

// Abort remains repeatable until retirement intent is durably recorded.
func (e *NativeEngine) Abort(ctx context.Context) error {
	j, err := e.cluster.Load(ctx)
	if err != nil {
		return err
	}

	if j == nil {
		return errors.New("no native recycle to abort")
	}

	if err = validateNativeJournal(j); err != nil {
		return err
	}

	switch j.Phase {
	case "retire", "complete":
		return ErrPastPointOfNoReturn
	case "prepare", "stop", "snapshot", "switch", "start", "verify", "ready", "abort":
	default:
		return errNativeJournal
	}

	if j.Phase != "abort" {
		j.AbortFrom = j.Phase
	}

	if !slices.Contains([]string{"prepare", "stop", "snapshot", "switch", "start", "verify", "ready"}, j.AbortFrom) {
		return errNativeJournal
	}

	j.Phase = "abort"
	if err = e.cluster.Save(ctx, j); err != nil {
		return err
	}

	actions := []func(context.Context, *NativeJournal) error{e.cluster.Stop, e.cluster.Restore, e.cluster.Start, e.cluster.DeleteReplacement}
	if j.AbortFrom == "prepare" {
		actions = []func(context.Context, *NativeJournal) error{e.cluster.DeleteReplacement}
	}

	for _, action := range actions {
		if err = action(ctx, j); err != nil {
			return err
		}

		if err = e.cluster.Save(ctx, j); err != nil {
			return err
		}
	}

	return e.cluster.Complete(ctx, j)
}

func (e *NativeEngine) loadOrPlan(ctx context.Context) (*NativeJournal, error) {
	j, err := e.cluster.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load native journal: %w", err)
	}

	if j == nil {
		j, err = e.cluster.Plan(ctx)
		if err != nil {
			return nil, err
		}

		if j != nil {
			j.Phase = "prepare"
		}
	}

	if err = validateNativeJournal(j); err != nil {
		return nil, err
	}

	return j, nil
}
