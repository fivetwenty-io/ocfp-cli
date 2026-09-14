package recycle

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeCluster records every mutating call in order, so the tests can assert on
// sequence and — more importantly — on what did not happen.
//
// The engine's real behaviour is a sequence of Proxmox calls, so this is where
// the safety properties are pinned. We never assert on HTTP; we assert that
// the original is not destroyed before its disk is safely elsewhere.
type fakeCluster struct {
	calls []string

	originalExists    bool
	replacementExists bool
	diskOwner         DiskOwner

	stopErr     error
	buildErr    error
	handoverErr error
	retireErr   error

	journal *Journal
}

func newFakeCluster() *fakeCluster {
	return &fakeCluster{originalExists: true, diskOwner: DiskOnOriginal}
}

func (f *fakeCluster) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeCluster) Observe(context.Context) (Observation, error) {
	return Observation{
		OriginalExists:    f.originalExists,
		ReplacementExists: f.replacementExists,
		DiskOwner:         f.diskOwner,
	}, nil
}

func (f *fakeCluster) StopOriginal(context.Context) error {
	f.record("StopOriginal")

	return f.stopErr
}

func (f *fakeCluster) StartOriginal(context.Context) error {
	f.record("StartOriginal")

	return nil
}

func (f *fakeCluster) Snapshot(context.Context) error {
	f.record("Snapshot")

	return nil
}

func (f *fakeCluster) Archive(context.Context) error {
	f.record("Archive")

	return nil
}

func (f *fakeCluster) BuildReplacement(context.Context) error {
	f.record("BuildReplacement")

	if f.buildErr != nil {
		return f.buildErr
	}

	f.replacementExists = true

	return nil
}

func (f *fakeCluster) DeleteReplacement(context.Context) error {
	f.record("DeleteReplacement")
	f.replacementExists = false

	return nil
}

func (f *fakeCluster) HandOverDisk(context.Context) error {
	f.record("HandOverDisk")

	if f.handoverErr != nil {
		return f.handoverErr
	}

	f.diskOwner = DiskOnReplacement

	return nil
}

func (f *fakeCluster) ReturnDisk(context.Context) error {
	f.record("ReturnDisk")
	f.diskOwner = DiskOnOriginal

	return nil
}

func (f *fakeCluster) RetireOriginal(context.Context) error {
	f.record("RetireOriginal")

	if f.retireErr != nil {
		return f.retireErr
	}

	f.originalExists = false

	return nil
}

func (f *fakeCluster) AdoptReplacement(context.Context) error {
	f.record("AdoptReplacement")

	return nil
}

func (f *fakeCluster) Provision(context.Context) error {
	f.record("Provision")

	return nil
}

func (f *fakeCluster) Verify(context.Context) error {
	f.record("Verify")

	return nil
}

func (f *fakeCluster) SaveJournal(_ context.Context, j *Journal) error {
	f.record("SaveJournal:" + string(j.Phase))
	f.journal = j

	return nil
}

func (f *fakeCluster) LoadJournal(context.Context) (*Journal, error) {
	return f.journal, nil
}

func (f *fakeCluster) ClearJournal(context.Context) error {
	f.record("ClearJournal")
	f.journal = nil

	return nil
}

func (f *fakeCluster) indexOf(name string) int {
	for i, c := range f.calls {
		if c == name {
			return i
		}
	}

	return -1
}

func (f *fakeCluster) called(name string) bool { return f.indexOf(name) >= 0 }

func TestEngine_RunsEveryPhaseInOrder(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	err := New(f, Options{Archive: true}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		"StopOriginal",
		"Snapshot",
		"Archive",
		"BuildReplacement",
		"HandOverDisk",
		"RetireOriginal",
		"AdoptReplacement",
		"Provision",
		"Verify",
	}

	var got []string

	for _, c := range f.calls {
		if !strings.HasPrefix(c, "SaveJournal") && c != "ClearJournal" {
			got = append(got, c)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestEngine_NeverRetiresBeforeHandover is the single most important assertion
// in this package.
//
// Destroying the original purges every disk its config still references. If
// that happens before the data disk has moved, the disk the whole feature
// exists to preserve is destroyed with the machine.
func TestEngine_NeverRetiresBeforeHandover(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	handover := f.indexOf("HandOverDisk")
	retire := f.indexOf("RetireOriginal")

	if handover < 0 || retire < 0 {
		t.Fatalf("expected both a handover and a retire, got %v", f.calls)
	}

	if retire < handover {
		t.Fatalf("the original was destroyed before its disk moved: %v", f.calls)
	}
}

// TestEngine_HandoverFailureLeavesOriginalIntact asserts a failed handover is
// survivable. The original must still exist and must be running again.
func TestEngine_HandoverFailureLeavesOriginalIntact(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.handoverErr = errors.New("cannot move disk to another VM while the source VM is running")

	err := New(f, Options{}).Run(context.Background())
	if err == nil {
		t.Fatal("expected Run to fail when the handover fails")
	}

	if f.called("RetireOriginal") {
		t.Errorf("the original was destroyed despite a failed handover: %v", f.calls)
	}

	if !f.originalExists {
		t.Error("the original no longer exists after a failed handover")
	}
}

// TestEngine_StopsBeforeHandingOver pins the ordering Proxmox itself demands:
// it refuses to reassign a disk out of a running VM.
func TestEngine_StopsBeforeHandingOver(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f.indexOf("StopOriginal") > f.indexOf("HandOverDisk") {
		t.Errorf("the disk was handed over before the source VM was stopped: %v", f.calls)
	}
}

// TestEngine_BuildsBeforeHandingOver asserts the replacement exists before the
// disk moves, so the disk goes from one live config to another rather than
// spending time owned by nothing.
func TestEngine_BuildsBeforeHandingOver(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f.indexOf("BuildReplacement") > f.indexOf("HandOverDisk") {
		t.Errorf("the disk was handed over before the replacement existed: %v", f.calls)
	}
}

// TestEngine_SkipsArchiveUnlessAsked asserts the slow copy is opt-in.
func TestEngine_SkipsArchiveUnlessAsked(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	if err := New(f, Options{Archive: false}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f.called("Archive") {
		t.Error("an archive was taken without --archive")
	}

	if !f.called("Snapshot") {
		t.Error("no snapshot was taken; the safety copy is not optional")
	}
}

// TestEngine_JournalsEveryPhaseBeforeActing asserts the journal is written
// before the work, not after. A crash mid-phase must leave a journal that
// points at the phase being attempted, or recovery under-reports progress.
func TestEngine_JournalsEveryPhaseBeforeActing(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f.indexOf("SaveJournal:quiesced") > f.indexOf("StopOriginal") {
		t.Errorf("the quiesce was journalled after it ran: %v", f.calls)
	}

	if !f.called("ClearJournal") {
		t.Error("the journal was not cleared after a successful run")
	}
}

// TestEngine_ResumesFromJournal asserts a second run picks up where the first
// left off rather than starting over.
//
// It re-enters the recorded phase, because the journal is written before a
// phase runs and so records what was attempted rather than what succeeded.
// Earlier phases are not repeated: re-stopping a stopped VM is harmless, but
// rebuilding a replacement that already exists is not.
func TestEngine_ResumesFromJournal(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.replacementExists = true
	f.diskOwner = DiskOnReplacement
	f.journal = &Journal{Phase: PhaseHandedOver}

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, shouldNotRun := range []string{"StopOriginal", "Snapshot", "BuildReplacement"} {
		if f.called(shouldNotRun) {
			t.Errorf("%s ran again on a resume from handed-over: %v", shouldNotRun, f.calls)
		}
	}

	if !f.called("HandOverDisk") {
		t.Errorf("the recorded phase was skipped rather than re-entered: %v", f.calls)
	}

	if !f.called("RetireOriginal") {
		t.Errorf("the resume did not continue past the recorded phase: %v", f.calls)
	}
}

// TestEngine_ReconstructsWhenJournalIsMissing covers the workstation that lost
// its state file. The cluster alone has to place the run.
func TestEngine_ReconstructsWhenJournalIsMissing(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.replacementExists = true
	f.diskOwner = DiskOnReplacement
	f.journal = nil

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f.called("BuildReplacement") {
		t.Errorf("a second replacement was built despite one already existing: %v", f.calls)
	}

	if !f.called("RetireOriginal") {
		t.Errorf("the reconstructed run did not continue: %v", f.calls)
	}
}

// TestEngine_AbortWalksCompensationsBackwards asserts an abort returns the
// disk before deleting the replacement it would have to move the disk off.
func TestEngine_AbortWalksCompensationsBackwards(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.replacementExists = true
	f.diskOwner = DiskOnReplacement
	f.journal = &Journal{Phase: PhaseHandedOver}

	if err := New(f, Options{}).Abort(context.Background()); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	returnAt := f.indexOf("ReturnDisk")
	deleteAt := f.indexOf("DeleteReplacement")
	startAt := f.indexOf("StartOriginal")

	if returnAt < 0 || deleteAt < 0 || startAt < 0 {
		t.Fatalf("abort did not run every compensation: %v", f.calls)
	}

	if !(returnAt < deleteAt && deleteAt < startAt) {
		t.Errorf("compensations ran out of order: %v", f.calls)
	}
}

// TestEngine_AbortRefusesPastThePointOfNoReturn asserts we say so plainly
// rather than attempting a recovery that cannot finish.
func TestEngine_AbortRefusesPastThePointOfNoReturn(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.originalExists = false
	f.replacementExists = true
	f.diskOwner = DiskOnReplacement
	f.journal = &Journal{Phase: PhaseRetired}

	err := New(f, Options{}).Abort(context.Background())
	if err == nil {
		t.Fatal("abort succeeded after the original was destroyed")
	}

	if !errors.Is(err, ErrPastPointOfNoReturn) {
		t.Errorf("error = %v, want ErrPastPointOfNoReturn", err)
	}

	for _, c := range f.calls {
		if c == "ReturnDisk" || c == "DeleteReplacement" {
			t.Errorf("abort attempted %s past the point of no return: %v", c, f.calls)
		}
	}
}

// TestEngine_RefusesWhenNothingToRecycle covers the disaster case: neither VM
// exists. The operator has to name the surviving volume explicitly, so the
// engine must not invent a run.
func TestEngine_RefusesWhenNothingToRecycle(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.originalExists = false
	f.replacementExists = false
	f.diskOwner = DiskOrphaned

	err := New(f, Options{}).Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded with no VM to recycle")
	}

	if f.called("BuildReplacement") {
		t.Errorf("a replacement was built with no run to resume: %v", f.calls)
	}
}

// TestEngine_StopBeforeRetireHaltsAtThePointOfNoReturn covers the gate an
// operator wants on a bloc they cannot afford to lose.
//
// It runs everything reversible — stop, snapshot, build, hand the disk over —
// and then stops, leaving the journal so the operator can verify the
// replacement really works before anything is destroyed. Re-running continues
// from there.
func TestEngine_StopBeforeRetireHaltsAtThePointOfNoReturn(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	err := New(f, Options{StopBeforeRetire: true}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !f.called("HandOverDisk") {
		t.Errorf("the run stopped before handing the disk over: %v", f.calls)
	}

	if f.called("RetireOriginal") {
		t.Errorf("the original was destroyed despite StopBeforeRetire: %v", f.calls)
	}

	// The journal must survive, or the operator cannot resume.
	if f.called("ClearJournal") {
		t.Errorf("the journal was cleared on a paused run; a resume would have nothing to read: %v", f.calls)
	}

	if f.journal == nil || f.journal.Phase != PhaseHandedOver {
		t.Errorf("journal = %+v, want it parked at handed-over so a resume re-enters it", f.journal)
	}
}

// TestEngine_ResumesPastThePauseOnASecondRun asserts the gate is a pause, not
// a wall: running again without the flag carries on and destroys the original.
func TestEngine_ResumesPastThePauseOnASecondRun(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()

	if err := New(f, Options{StopBeforeRetire: true}).Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if !f.called("RetireOriginal") {
		t.Errorf("the resume did not continue past the pause: %v", f.calls)
	}

	if !f.called("Verify") {
		t.Errorf("the resume did not finish the run: %v", f.calls)
	}
}

// TestEngine_ResumeRepeatsTheRecordedPhase is a safety regression test for a
// bug caught on a live cluster.
//
// The journal is written BEFORE a phase runs, so a recorded phase may have
// failed. Resuming at the phase AFTER it therefore skips work that never
// happened. Observed for real: a handover failed, the journal said
// "handed-over", and the resume went straight to destroying the original —
// which still held the data disk the whole feature exists to preserve.
//
// A resume must re-enter the recorded phase. Every phase is written to be safe
// to run twice; skipping one is not safe at all.
func TestEngine_ResumeRepeatsTheRecordedPhase(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.replacementExists = true
	f.diskOwner = DiskOnOriginal // the handover did NOT succeed
	f.journal = &Journal{Phase: PhaseHandedOver}

	if err := New(f, Options{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !f.called("HandOverDisk") {
		t.Fatalf("the resume skipped the handover that had failed: %v", f.calls)
	}

	if f.indexOf("HandOverDisk") > f.indexOf("RetireOriginal") {
		t.Errorf("the original was destroyed before the retried handover: %v", f.calls)
	}
}

// TestEngine_RefusesToRetireWhileTheDiskIsStillOnTheOriginal is the backstop.
//
// Whatever the journal claims, destroying the original while it still holds
// the data disk destroys that disk too, because the delete purges every disk
// the config references. The engine checks the cluster rather than trusting
// its own record.
func TestEngine_RefusesToRetireWhileTheDiskIsStillOnTheOriginal(t *testing.T) {
	t.Parallel()

	f := newFakeCluster()
	f.replacementExists = true
	f.diskOwner = DiskOnOriginal
	f.journal = &Journal{Phase: PhaseRetired}
	f.handoverErr = errors.New("still refusing")

	err := New(f, Options{}).Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded with the data disk still on the VM it was about to destroy")
	}

	if f.called("RetireOriginal") {
		t.Errorf("the original was destroyed while it still held the data disk: %v", f.calls)
	}
}
