package recycle

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNothingToRecycle is returned when neither the original nor a replacement
// can be found. There is no run to start and none to resume, and if a data
// volume survived, the operator has to name it explicitly rather than let the
// engine guess which disk it was.
var ErrNothingToRecycle = errors.New(
	"neither the original VM nor a replacement exists; nothing to recycle. " +
		"If the data volume survived, name it explicitly to adopt it")

// Journal is the recycle's progress, saved to bloc state before each phase.
//
// It lives beside everything else about the bloc so an operator who has the
// state file has the recovery map. It is the fast path for resuming, never the
// only one: the engine can place an interrupted run from the cluster alone.
type Journal struct {
	// Phase is the last phase the engine committed to attempting.
	Phase Phase `json:"phase"`

	// Role distinguishes a bastion recycle from an artifacts one.
	Role string `json:"role,omitempty"`

	// OriginalID is the VMID being replaced.
	OriginalID string `json:"originalId,omitempty"`

	// ReplacementID is the VMID of the replacement, once it exists.
	ReplacementID string `json:"replacementId,omitempty"`

	// VolumeID is the data disk being carried across. Recorded before the
	// original is destroyed so recovery can always find the disk by id, even
	// when the fallback handover left it owned by nothing.
	VolumeID string `json:"volumeId,omitempty"`

	// Handover records which mechanism was used, so a resume continues the
	// way it started.
	Handover string `json:"handover,omitempty"`

	// StartedAt is when the run began.
	StartedAt time.Time `json:"startedAt"`
}

// Cluster is everything the engine needs to do and observe.
//
// It is an interface so the engine can be tested on ordering — which is where
// the safety properties live — without standing up a Proxmox server. The real
// implementation is a thin adapter over the CPI managers.
type Cluster interface {
	// Observe reports the three facts that place an interrupted run.
	Observe(ctx context.Context) (Observation, error)

	StopOriginal(ctx context.Context) error
	StartOriginal(ctx context.Context) error

	// Snapshot takes the cheap in-place safety copy.
	Snapshot(ctx context.Context) error

	// Archive takes a copy that survives the VM being destroyed.
	Archive(ctx context.Context) error

	// BuildReplacement creates the replacement under a transient name, stopped.
	BuildReplacement(ctx context.Context) error
	DeleteReplacement(ctx context.Context) error

	// HandOverDisk moves the data disk to the replacement.
	HandOverDisk(ctx context.Context) error
	ReturnDisk(ctx context.Context) error

	// RetireOriginal destroys the original. Past this, nothing can be undone.
	RetireOriginal(ctx context.Context) error

	// AdoptReplacement renames it, rewrites the state entry, and starts it.
	AdoptReplacement(ctx context.Context) error

	Provision(ctx context.Context) error
	Verify(ctx context.Context) error

	SaveJournal(ctx context.Context, j *Journal) error
	LoadJournal(ctx context.Context) (*Journal, error)
	ClearJournal(ctx context.Context) error
}

// Options tunes a run.
type Options struct {
	// Archive also takes a copy that survives the VM being destroyed. The
	// snapshot is never optional; this is, because it is slow and needs space.
	Archive bool

	// Role is recorded in the journal.
	Role string
}

// Engine drives a recycle.
type Engine struct {
	cluster Cluster
	opts    Options
}

// New returns an engine over the given cluster.
func New(cluster Cluster, opts Options) *Engine {
	return &Engine{cluster: cluster, opts: opts}
}

// Run executes the recycle from wherever it currently stands.
//
// A fresh run starts at the beginning. An interrupted one continues from the
// phase the journal records, or, when the journal is gone, from the phase the
// cluster implies. There is deliberately no resume flag: a flag is a thing
// operators forget under pressure, and the engine can always work it out.
func (e *Engine) Run(ctx context.Context) error {
	start, err := e.resolveStartPhase(ctx)
	if err != nil {
		return err
	}

	journal, err := e.cluster.LoadJournal(ctx)
	if err != nil || journal == nil {
		journal = &Journal{Role: e.opts.Role, StartedAt: time.Now()}
	}

	for phase := start; phase != ""; {
		journal.Phase = phase

		// Journal before acting, never after. A crash mid-phase must leave a
		// record pointing at the phase being attempted; a record written
		// afterwards would under-report progress and send a resume back
		// through work that had already half-happened.
		if err := e.cluster.SaveJournal(ctx, journal); err != nil {
			return fmt.Errorf("save journal at %s: %w", phase, err)
		}

		if err := e.runPhase(ctx, phase); err != nil {
			return fmt.Errorf("%s: %w", phase, err)
		}

		next, ok := NextPhase(phase)
		if !ok {
			break
		}

		phase = next
	}

	if err := e.cluster.ClearJournal(ctx); err != nil {
		return fmt.Errorf("clear journal: %w", err)
	}

	return nil
}

// resolveStartPhase decides where to begin.
//
// The journal is preferred because it records intent; the cluster is the
// fallback because state files get lost. When the journal names a phase, the
// run continues from the phase AFTER it, since the journal is written before
// the work and a recorded phase may or may not have completed — every phase
// here is safe to re-enter, but re-building a replacement that already exists
// is not, which is why the observation is consulted too.
func (e *Engine) resolveStartPhase(ctx context.Context) (Phase, error) {
	obs, err := e.cluster.Observe(ctx)
	if err != nil {
		return "", fmt.Errorf("observe cluster: %w", err)
	}

	if !obs.OriginalExists && !obs.ReplacementExists {
		return "", ErrNothingToRecycle
	}

	journal, err := e.cluster.LoadJournal(ctx)
	if err == nil && journal != nil && journal.Phase != "" {
		if next, ok := NextPhase(journal.Phase); ok {
			return next, nil
		}

		// The journal records the final phase; the run already finished.
		return "", nil
	}

	// No journal. Place the run from the cluster and continue from the phase
	// after whatever it implies.
	reached, ok := ReconstructPhase(obs)
	if !ok {
		return "", ErrNothingToRecycle
	}

	// A cluster with only the original and nothing else has not started; begin
	// at the first phase rather than skipping the quiesce.
	if !obs.ReplacementExists {
		return PhasePlanned, nil
	}

	next, ok := NextPhase(reached)
	if !ok {
		return "", nil
	}

	return next, nil
}

//nolint:cyclop // a flat dispatch over the phase enum is clearer than a map of closures
func (e *Engine) runPhase(ctx context.Context, phase Phase) error {
	switch phase {
	case PhasePlanned:
		return nil
	case PhaseQuiesced:
		return e.cluster.StopOriginal(ctx)
	case PhaseSecured:
		if err := e.cluster.Snapshot(ctx); err != nil {
			return err
		}

		if e.opts.Archive {
			return e.cluster.Archive(ctx)
		}

		return nil
	case PhaseBuilt:
		return e.cluster.BuildReplacement(ctx)
	case PhaseHandedOver:
		return e.cluster.HandOverDisk(ctx)
	case PhaseRetired:
		return e.cluster.RetireOriginal(ctx)
	case PhaseAdopted:
		return e.cluster.AdoptReplacement(ctx)
	case PhaseProvisioned:
		return e.cluster.Provision(ctx)
	case PhaseVerified:
		return e.cluster.Verify(ctx)
	default:
		return fmt.Errorf("unknown phase %q", phase) //nolint:err113 // unreachable via the public API
	}
}

// Abort walks the compensations backwards to undo a run in progress.
//
// It refuses once the original has been destroyed, and says why: its boot disk
// and its snapshots went with it, so the only route back is the archive. That
// refusal is the operational definition of the point of no return, and saying
// it plainly is better than attempting a recovery that cannot finish.
func (e *Engine) Abort(ctx context.Context) error {
	journal, err := e.cluster.LoadJournal(ctx)

	reached := Phase("")

	if err == nil && journal != nil {
		reached = journal.Phase
	}

	if reached == "" {
		obs, obsErr := e.cluster.Observe(ctx)
		if obsErr != nil {
			return fmt.Errorf("observe cluster: %w", obsErr)
		}

		var ok bool

		reached, ok = ReconstructPhase(obs)
		if !ok {
			return ErrNothingToRecycle
		}
	}

	plan, err := AbortPlan(reached)
	if err != nil {
		return err
	}

	for _, comp := range plan {
		if err := e.runCompensation(ctx, comp); err != nil {
			return fmt.Errorf("compensation %s: %w", comp, err)
		}
	}

	return e.cluster.ClearJournal(ctx)
}

func (e *Engine) runCompensation(ctx context.Context, comp Compensation) error {
	switch comp {
	case CompensationReturnDisk:
		return e.cluster.ReturnDisk(ctx)
	case CompensationDeleteReplacement:
		return e.cluster.DeleteReplacement(ctx)
	case CompensationStartOriginal:
		return e.cluster.StartOriginal(ctx)
	case CompensationNone:
		return nil
	case CompensationImpossible:
		return ErrPastPointOfNoReturn
	default:
		return nil
	}
}
