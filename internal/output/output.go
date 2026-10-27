// Package output holds what a provider puts between a command's output
// and the caller's writers.
package output

import "io"

// Sink hands a command's output to a caller's writer until that writer
// fails, then discards the rest.
//
// A transport's copy stops at the first write error, and once nothing
// reads the stream the command blocks on its next write and Wait never
// returns. Draining keeps the command able to finish, so its exit status
// is still there to report, and the writer's failure is kept to be
// reported beside it. A nil writer discards everything.
//
// One copy writes a Sink and its failure is read once that copy has
// finished, so nothing here synchronizes.
type Sink struct {
	w   io.Writer
	err error
}

// NewSink returns a Sink delivering to w, or discarding everything when w
// is nil.
func NewSink(w io.Writer) *Sink {
	return &Sink{w: w}
}

// Write delivers b while the writer is still taking output. It never
// fails: a writer that does is remembered, and the rest is discarded.
func (s *Sink) Write(b []byte) (int, error) {
	if s.w != nil && s.err == nil {
		if _, err := s.w.Write(b); err != nil {
			s.err = err
		}
	}

	return len(b), nil
}

// Err reports the writer's first failure, or nil.
func (s *Sink) Err() error {
	return s.err
}

// FirstErr reports the first failure among sinks, in the order given.
func FirstErr(sinks ...*Sink) error {
	for _, s := range sinks {
		if err := s.Err(); err != nil {
			return err
		}
	}

	return nil
}
