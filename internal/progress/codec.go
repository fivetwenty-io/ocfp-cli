package progress

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	// ErrHeaderFirst is returned when Encode runs before Header.
	ErrHeaderFirst = errors.New("progress: the header must be written first")

	// ErrNoHeader is returned when a stream does not begin with a header record.
	ErrNoHeader = errors.New("progress: stream does not begin with a header record")

	errHeaderWritten = errors.New("progress: the header was already written")
	errHeaderKind    = errors.New("progress: use Header to write a header record")
)

// Clock supplies the time stamped on each record.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock returns a Clock backed by the wall clock.
func SystemClock() Clock { return systemClock{} }

// Encoder writes records as lines of JSON. It is safe for concurrent use, and
// a line is always written whole.
type Encoder struct {
	mu     sync.Mutex
	w      io.Writer
	clock  Clock
	seq    int64
	headed bool
}

// NewEncoder returns an Encoder that writes to w and stamps records from c.
func NewEncoder(w io.Writer, c Clock) *Encoder {
	return &Encoder{w: w, clock: c}
}

// Header writes the header record, which carries seq 0. The WireContract
// field is always set to the current WireContract.
func (e *Encoder) Header(h Header) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.headed {
		return errHeaderWritten
	}

	h.WireContract = WireContract

	if err := e.write(KindHeader, h); err != nil {
		return err
	}

	e.headed = true

	return nil
}

// Encode writes one record of the given kind. It returns ErrHeaderFirst until
// Header has been called.
func (e *Encoder) Encode(kind Kind, body any) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.headed {
		return ErrHeaderFirst
	}

	if kind == KindHeader {
		return errHeaderKind
	}

	return e.write(kind, body)
}

// write must be called with e.mu held.
func (e *Encoder) write(kind Kind, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s body: %w", kind, err)
	}

	line, err := json.Marshal(Record{Kind: kind, Seq: e.seq, Time: e.clock.Now().UTC(), Body: raw})
	if err != nil {
		return fmt.Errorf("encode %s record: %w", kind, err)
	}

	if _, err := e.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write %s record: %w", kind, err)
	}

	e.seq++

	return nil
}

// Decoder reads records from a stream.
type Decoder struct {
	r      *bufio.Reader
	header *Header
	err    error
}

// NewDecoder returns a Decoder that reads from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReader(r)}
}

// Header reads the first record and returns its header body. It returns
// ErrNoHeader when the stream is empty or begins with another kind of record.
func (d *Decoder) Header() (Header, error) {
	if d.header != nil {
		return *d.header, nil
	}

	if d.err != nil {
		return Header{}, d.err
	}

	rec, err := d.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = ErrNoHeader
		}

		d.err = err

		return Header{}, err
	}

	if rec.Kind != KindHeader {
		d.err = ErrNoHeader

		return Header{}, d.err
	}

	var h Header
	if err := json.Unmarshal(rec.Body, &h); err != nil {
		d.err = fmt.Errorf("decode header body: %w", err)

		return Header{}, d.err
	}

	d.header = &h

	return h, nil
}

// Decode returns the next record after the header. When the header has not
// been read yet, Decode reads and checks it first, so a stream without a
// header is refused with ErrNoHeader. At the end of the stream it returns
// io.EOF.
func (d *Decoder) Decode() (Record, error) {
	if _, err := d.Header(); err != nil {
		return Record{}, err
	}

	return d.next()
}

func (d *Decoder) next() (Record, error) {
	for {
		line, err := d.r.ReadBytes('\n')
		if len(line) > 0 && string(line) != "\n" {
			var rec Record
			if jerr := json.Unmarshal(line, &rec); jerr != nil {
				return Record{}, fmt.Errorf("decode record: %w", jerr)
			}

			return rec, nil
		}

		if err != nil {
			return Record{}, err
		}
	}
}
