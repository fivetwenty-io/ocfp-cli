package recycle

import (
	"context"
	"errors"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"reflect"
	"testing"
)

type nativeFake struct {
	journal  *NativeJournal
	calls    []string
	fail     string
	saveFail bool
}

func (f *nativeFake) Plan(context.Context) (*NativeJournal, error) {
	return &NativeJournal{Version: 1, Token: "token", Identity: cpi.BootIdentity{InstanceID: "vm", RootVolumeID: "old"}, Image: "image"}, nil
}
func (f *nativeFake) Load(context.Context) (*NativeJournal, error) {
	if f.fail == "load" {
		return nil, errors.New("corrupt")
	}
	return f.journal, nil
}
func (f *nativeFake) Save(_ context.Context, j *NativeJournal) error {
	if f.saveFail {
		return errors.New("disk full")
	}
	copy := *j
	f.journal = &copy
	return nil
}
func (f *nativeFake) action(s string) error {
	f.calls = append(f.calls, s)
	if f.fail == s {
		return errors.New(s)
	}
	return nil
}
func (f *nativeFake) Complete(context.Context, *NativeJournal) error {
	if err := f.action("complete"); err != nil {
		return err
	}
	f.journal = nil
	return nil
}
func (f *nativeFake) Prepare(_ context.Context, j *NativeJournal) error {
	j.ReplacementID = "new"
	return f.action("prepare")
}
func (f *nativeFake) Stop(context.Context, *NativeJournal) error { return f.action("stop") }
func (f *nativeFake) Snapshot(_ context.Context, j *NativeJournal) error {
	j.SnapshotID = "snap"
	return f.action("snapshot")
}
func (f *nativeFake) Switch(context.Context, *NativeJournal) error  { return f.action("switch") }
func (f *nativeFake) Start(context.Context, *NativeJournal) error   { return f.action("start") }
func (f *nativeFake) Verify(context.Context, *NativeJournal) error  { return f.action("verify") }
func (f *nativeFake) Retire(context.Context, *NativeJournal) error  { return f.action("retire") }
func (f *nativeFake) Restore(context.Context, *NativeJournal) error { return f.action("restore") }
func (f *nativeFake) DeleteReplacement(context.Context, *NativeJournal) error {
	return f.action("delete")
}
func TestNativePauseAndResume(t *testing.T) {
	f := &nativeFake{}
	if err := NewNative(f, true).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "stop", "snapshot", "switch", "start", "verify"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("%v", f.calls)
	}
	if f.journal.Phase != "ready" {
		t.Fatal(f.journal)
	}
	f.calls = nil
	if err := NewNative(f, false).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"verify", "retire", "complete"}) {
		t.Fatal(f.calls)
	}
}
func TestNativeFailsClosed(t *testing.T) {
	for _, phase := range []string{"load", "prepare", "stop", "snapshot", "switch", "start", "verify"} {
		t.Run(phase, func(t *testing.T) {
			f := &nativeFake{fail: phase}
			if NewNative(f, false).Run(t.Context()) == nil {
				t.Fatal("expected failure")
			}
			for _, call := range f.calls {
				if call == "retire" {
					t.Fatal("unsafe retirement")
				}
			}
		})
	}
	f := &nativeFake{saveFail: true}
	if NewNative(f, false).Run(t.Context()) == nil || len(f.calls) > 0 {
		t.Fatal("mutated without journal")
	}
}
func TestNativeAbortAndPartialFailure(t *testing.T) {
	f := &nativeFake{fail: "switch"}
	_ = NewNative(f, false).Run(t.Context())
	f.fail = "restore"
	f.calls = nil
	if NewNative(f, false).Abort(t.Context()) == nil {
		t.Fatal("expected failed restore")
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "restore"}) {
		t.Fatal(f.calls)
	}
	f.fail = ""
	f.calls = nil
	if err := NewNative(f, false).Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"stop", "restore", "start", "delete", "complete"}) {
		t.Fatal(f.calls)
	}
}
func TestNativeRetirementIntentRefusesAbort(t *testing.T) {
	f := &nativeFake{fail: "retire"}
	_ = NewNative(f, false).Run(t.Context())
	f.calls = nil
	if NewNative(f, false).Abort(t.Context()) == nil || len(f.calls) != 0 {
		t.Fatal("unsafe abort")
	}
}

func TestNativePauseAbortAndFailedReverification(t *testing.T) {
	f := &nativeFake{}
	if err := NewNative(f, true).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.fail = "verify"
	if NewNative(f, false).Run(t.Context()) == nil {
		t.Fatal("verification failure ignored")
	}
	if f.journal.Phase != "ready" {
		t.Fatal("marked irreversible before verification")
	}
	f.fail = ""
	if err := NewNative(f, false).Abort(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestNativeUnknownJournalRefusesMutations(t *testing.T) {
	f := &nativeFake{journal: &NativeJournal{Version: 1, Token: "x", Image: "image", Identity: cpi.BootIdentity{InstanceID: "vm", RootVolumeID: "root"}, Phase: "unknown"}}
	if NewNative(f, false).Run(t.Context()) == nil || len(f.calls) > 0 {
		t.Fatal("invalid journal allowed")
	}
	if NewNative(f, false).Abort(t.Context()) == nil || len(f.calls) > 0 {
		t.Fatal("invalid journal abort allowed")
	}
}

func TestNativeCannotPauseAfterRetirementIntent(t *testing.T) {
	f := &nativeFake{fail: "retire"}
	_ = NewNative(f, false).Run(t.Context())
	f.calls = nil
	if NewNative(f, true).Run(t.Context()) == nil || len(f.calls) > 0 {
		t.Fatal("claimed reversible pause after retirement")
	}
}

func TestNativeIncompletePhaseJournalFailsBeforeStop(t *testing.T) {
	f := &nativeFake{journal: &NativeJournal{Version: 1, Token: "token", Phase: "stop", Image: "image", Identity: cpi.BootIdentity{InstanceID: "vm", RootVolumeID: "old"}}}
	if NewNative(f, false).Run(t.Context()) == nil || len(f.calls) > 0 {
		t.Fatal("incomplete journal mutated cloud")
	}
}
