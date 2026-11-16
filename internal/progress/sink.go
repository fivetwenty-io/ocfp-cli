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

// WorkRef names one piece of work that a job started outside itself, by its
// kind and its id. A warning that undo hooks did not run lists the work they
// would have undone this way.
type WorkRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// String names the work as its kind and then its id, or as its kind alone
// when it has no id.
func (w WorkRef) String() string {
	if w.ID == "" {
		return w.Kind
	}

	return w.Kind + " " + w.ID
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

	// Undone lists the work whose undo hooks did not run. Only the warning
	// that a killed job's stream carries sets it.
	Undone []WorkRef `json:"undone,omitempty"`
}

// Sink receives progress events. An adapter decides how to show them.
type Sink interface {
	Emit(ev Event) error
}
