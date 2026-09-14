package recycle

import "testing"

// TestPhaseOrder pins the sequence. The ordering is not cosmetic: the handover
// must happen while the replacement exists and the original is merely stopped,
// so the disk goes straight from one owner to the next and never belongs to
// nothing.
func TestPhaseOrder(t *testing.T) {
	t.Parallel()

	want := []Phase{
		PhasePlanned,
		PhaseQuiesced,
		PhaseSecured,
		PhaseBuilt,
		PhaseHandedOver,
		PhaseRetired,
		PhaseAdopted,
		PhaseProvisioned,
		PhaseVerified,
	}

	got := PhaseOrder()

	if len(got) != len(want) {
		t.Fatalf("PhaseOrder returned %d phases, want %d: %v", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("phase[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestNextPhase walks the machine forward.
func TestNextPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		from Phase
		want Phase
		ok   bool
	}{
		{PhasePlanned, PhaseQuiesced, true},
		{PhaseQuiesced, PhaseSecured, true},
		{PhaseSecured, PhaseBuilt, true},
		{PhaseBuilt, PhaseHandedOver, true},
		{PhaseHandedOver, PhaseRetired, true},
		{PhaseRetired, PhaseAdopted, true},
		{PhaseAdopted, PhaseProvisioned, true},
		{PhaseProvisioned, PhaseVerified, true},
		{PhaseVerified, "", false},
		{Phase("nonsense"), "", false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(string(tc.from), func(t *testing.T) {
			t.Parallel()

			got, ok := NextPhase(tc.from)

			if ok != tc.ok {
				t.Fatalf("NextPhase(%q) ok = %v, want %v", tc.from, ok, tc.ok)
			}

			if got != tc.want {
				t.Errorf("NextPhase(%q) = %q, want %q", tc.from, got, tc.want)
			}
		})
	}
}

// TestPointOfNoReturn is the assertion the operator's safety depends on.
//
// Everything up to and including the handover can be undone: the disk is moved
// back and the replacement deleted. Once the original VM is destroyed its boot
// disk and its snapshots go with it, and the only route back is restoring an
// archive. Abort must refuse past that line rather than pretend.
func TestPointOfNoReturn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase        Phase
		canStillStop bool
	}{
		{PhasePlanned, true},
		{PhaseQuiesced, true},
		{PhaseSecured, true},
		{PhaseBuilt, true},
		{PhaseHandedOver, true},
		{PhaseRetired, false},
		{PhaseAdopted, false},
		{PhaseProvisioned, false},
		{PhaseVerified, false},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(string(tc.phase), func(t *testing.T) {
			t.Parallel()

			if got := CanAbort(tc.phase); got != tc.canStillStop {
				t.Errorf("CanAbort(%q) = %v, want %v", tc.phase, got, tc.canStillStop)
			}
		})
	}
}

// TestReconstructPhase covers recovery when the journal is gone, which is the
// case that will actually happen: an interrupted run on a workstation whose
// state file was never written, or was lost.
//
// The three observable facts about the cluster — does the original exist, does
// the replacement exist, and who owns the data disk — determine the phase
// unambiguously.
func TestReconstructPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		obs   Observation
		want  Phase
		wantK bool
	}{
		{
			name:  "nothing has happened yet",
			obs:   Observation{OriginalExists: true, ReplacementExists: false, DiskOwner: DiskOnOriginal},
			want:  PhaseSecured,
			wantK: true,
		},
		{
			name:  "replacement built, disk still on the original",
			obs:   Observation{OriginalExists: true, ReplacementExists: true, DiskOwner: DiskOnOriginal},
			want:  PhaseBuilt,
			wantK: true,
		},
		{
			name:  "disk handed over, original still standing",
			obs:   Observation{OriginalExists: true, ReplacementExists: true, DiskOwner: DiskOnReplacement},
			want:  PhaseHandedOver,
			wantK: true,
		},
		{
			name:  "original retired, replacement holds the disk",
			obs:   Observation{OriginalExists: false, ReplacementExists: true, DiskOwner: DiskOnReplacement},
			want:  PhaseRetired,
			wantK: true,
		},
		{
			// The detach path was in play and the re-attach did not finish.
			// Recoverable: attach the volume by id and carry on.
			name:  "original gone, disk orphaned",
			obs:   Observation{OriginalExists: false, ReplacementExists: true, DiskOwner: DiskOrphaned},
			want:  PhaseRetired,
			wantK: true,
		},
		{
			// Nothing to resume. This is the disaster case and it needs the
			// operator to name the surviving volume explicitly.
			name:  "neither VM exists",
			obs:   Observation{OriginalExists: false, ReplacementExists: false, DiskOwner: DiskOrphaned},
			want:  "",
			wantK: false,
		},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ReconstructPhase(tc.obs)

			if ok != tc.wantK {
				t.Fatalf("ReconstructPhase(%+v) ok = %v, want %v", tc.obs, ok, tc.wantK)
			}

			if got != tc.want {
				t.Errorf("ReconstructPhase(%+v) = %q, want %q", tc.obs, got, tc.want)
			}
		})
	}
}

// TestCompensationFor pins what undoing each phase means. An abort walks these
// backwards from wherever the journal left off.
func TestCompensationFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase Phase
		want  Compensation
	}{
		{PhasePlanned, CompensationNone},
		{PhaseQuiesced, CompensationStartOriginal},
		{PhaseSecured, CompensationNone},
		{PhaseBuilt, CompensationDeleteReplacement},
		{PhaseHandedOver, CompensationReturnDisk},
		{PhaseRetired, CompensationImpossible},
		{PhaseAdopted, CompensationImpossible},
	}

	for _, tc := range tests {
		tc := tc

		t.Run(string(tc.phase), func(t *testing.T) {
			t.Parallel()

			if got := CompensationFor(tc.phase); got != tc.want {
				t.Errorf("CompensationFor(%q) = %q, want %q", tc.phase, got, tc.want)
			}
		})
	}
}

// TestAbortPlan asserts compensations come back in reverse order, so the disk
// is returned before the replacement is deleted and the original is only
// restarted once nothing else needs it stopped.
func TestAbortPlan(t *testing.T) {
	t.Parallel()

	got, err := AbortPlan(PhaseHandedOver)
	if err != nil {
		t.Fatalf("AbortPlan(handed-over): %v", err)
	}

	want := []Compensation{
		CompensationReturnDisk,
		CompensationDeleteReplacement,
		CompensationStartOriginal,
	}

	if len(got) != len(want) {
		t.Fatalf("AbortPlan returned %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAbortPlan_RefusesPastThePointOfNoReturn(t *testing.T) {
	t.Parallel()

	for _, phase := range []Phase{PhaseRetired, PhaseAdopted, PhaseProvisioned, PhaseVerified} {
		phase := phase

		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()

			_, err := AbortPlan(phase)
			if err == nil {
				t.Fatalf("AbortPlan(%q) returned no error; the original VM is already destroyed", phase)
			}
		})
	}
}
