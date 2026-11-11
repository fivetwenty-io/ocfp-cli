package progress

// EventKind names the type of a progress event.
type EventKind string

// The event kinds.
const (
	EventStepStarted      EventKind = "step_started"
	EventStepProgress     EventKind = "step_progress"
	EventStepFinished     EventKind = "step_finished"
	EventWarning          EventKind = "warning"
	EventArtifactProduced EventKind = "artifact_produced"
)

// Artifact describes a file that a step produced.
type Artifact struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Event is one structured report of progress from a step.
type Event struct {
	Kind     EventKind `json:"kind"`
	Step     string    `json:"step"`
	Message  string    `json:"message,omitempty"`
	Current  int64     `json:"current,omitempty"`
	Total    int64     `json:"total,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
	Artifact *Artifact `json:"artifact,omitempty"`
}

// Sink receives progress events. An adapter decides how to show them.
type Sink interface {
	Emit(ev Event) error
}
