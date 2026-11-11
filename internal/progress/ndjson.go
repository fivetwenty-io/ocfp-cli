package progress

type ndjsonSink struct{ enc *Encoder }

// NewNDJSONSink returns a Sink that writes each event as an event record.
func NewNDJSONSink(enc *Encoder) Sink {
	return ndjsonSink{enc: enc}
}

func (s ndjsonSink) Emit(ev Event) error {
	return s.enc.Encode(KindEvent, ev)
}

type ndjsonPrompter struct {
	enc         *Encoder
	src         AnswerSource
	interactive bool
}

// NewNDJSONPrompter returns a Prompter that writes each question as a question
// record and then waits for src to supply the answer. An answer is never
// written to the stream. When interactive is false it answers from the default
// without writing anything, and fails when there is none.
func NewNDJSONPrompter(enc *Encoder, src AnswerSource, interactive bool) Prompter {
	return &ndjsonPrompter{enc: enc, src: src, interactive: interactive}
}

func (p *ndjsonPrompter) Ask(q Question) (string, error) {
	if !p.interactive {
		return nonInteractive(q)
	}

	wire := q
	if wire.Sensitive {
		wire.Default = nil
	}

	if err := p.enc.Encode(KindQuestion, wire); err != nil {
		return "", err
	}

	return p.src.Answer(q.ID)
}
