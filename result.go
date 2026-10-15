package invoke

import "time"

// Result describes a completed process.
//
// A Result is only meaningful in combination with the error returned
// alongside it: on a nil error the process exited zero; with an [ExitError]
// the process ran and ExitCode carries its exit status; with any other
// error the process did not run to completion and the Result's fields
// carry no information. Result deliberately has no error field — the
// returned error is the single source of truth.
type Result struct {
	// ExitCode is the process's exit status. It is -1 when the process
	// was terminated by a signal (the accompanying ExitError carries
	// which one).
	ExitCode int

	// Duration is the wall-clock time from process start until its exit
	// was observed. Observation can lag the exit by however long the
	// provider waits for the command's output to drain: the local provider
	// bounds that wait with its termination grace (see the local package's
	// WithTerminationGrace), and the ssh and docker providers wait for the
	// output stream to end.
	Duration time.Duration
}
