package progress_test

import (
	"encoding/json"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func TestEventKindsAreTheFive(t *testing.T) {
	t.Parallel()

	want := map[progress.EventKind]string{
		progress.EventStepStarted:      "step_started",
		progress.EventStepProgress:     "step_progress",
		progress.EventStepFinished:     "step_finished",
		progress.EventWarning:          "warning",
		progress.EventArtifactProduced: "artifact_produced",
	}

	for k, v := range want {
		if string(k) != v {
			t.Errorf("kind %q, want %q", k, v)
		}
	}
}

type recordingSink struct{ got []progress.Event }

func (s *recordingSink) Emit(ev progress.Event) error {
	s.got = append(s.got, ev)

	return nil
}

func TestEventJSONShape(t *testing.T) {
	t.Parallel()

	var sink progress.Sink = &recordingSink{}

	ev := progress.Event{
		Kind:     progress.EventArtifactProduced,
		Step:     "render",
		Artifact: &progress.Artifact{Name: "n", Path: "/p", Digest: "sha256:ab"},
	}
	if err := sink.Emit(ev); err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	const want = `{"kind":"artifact_produced","step":"render","artifact":{"name":"n","path":"/p","digest":"sha256:ab"}}`
	if string(b) != want {
		t.Fatalf("json = %s\nwant   %s", b, want)
	}
}
