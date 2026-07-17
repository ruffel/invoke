package invoke_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/ruffel/invoke"
)

// stubEnv proves the interfaces are implementable without any provider
// machinery; it is a compile-time shape check, not a behavioral fake.
type stubEnv struct{}

var (
	_ invoke.Environment = stubEnv{}
	_ invoke.Process     = stubProcess{}
)

func (stubEnv) Start(_ context.Context, _ invoke.Command, _ invoke.IO) (invoke.Process, error) {
	return stubProcess{}, nil
}

func (stubEnv) LookPath(_ context.Context, name string) (string, error) { return name, nil }

func (stubEnv) Upload(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (stubEnv) Download(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	return nil
}

func (stubEnv) OS() invoke.TargetOS               { return invoke.OSLinux }
func (stubEnv) Capabilities() invoke.Capabilities { return invoke.Capabilities{} }
func (stubEnv) Close() error                      { return nil }

type stubProcess struct{}

func (stubProcess) Wait() (invoke.Result, error) { return invoke.Result{}, nil }
func (stubProcess) Signal(_ invoke.Signal) error { return nil }
func (stubProcess) Close() error                 { return nil }

func TestNewTransferConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := invoke.NewTransferConfig()

	if cfg.Mode != nil {
		t.Errorf("default Mode = %v, want nil (preserve source)", *cfg.Mode)
	}

	if cfg.Symlinks != invoke.SymlinkPreserve {
		t.Errorf("default Symlinks = %v, want SymlinkPreserve", cfg.Symlinks)
	}

	if cfg.SkipSpecial {
		t.Error("default SkipSpecial = true, want false")
	}

	if cfg.Progress != nil {
		t.Error("default Progress != nil")
	}
}

func TestTransferOptions(t *testing.T) {
	t.Parallel()

	var seen []invoke.TransferProgress

	cfg := invoke.NewTransferConfig(
		invoke.WithMode(0o600),
		invoke.WithSymlinks(invoke.SymlinkFollow),
		invoke.WithSkipSpecial(),
		invoke.WithProgress(func(p invoke.TransferProgress) { seen = append(seen, p) }),
	)

	if cfg.Mode == nil || *cfg.Mode != fs.FileMode(0o600) {
		t.Errorf("Mode = %v, want 0600", cfg.Mode)
	}

	if cfg.Symlinks != invoke.SymlinkFollow {
		t.Errorf("Symlinks = %v, want SymlinkFollow", cfg.Symlinks)
	}

	if !cfg.SkipSpecial {
		t.Error("SkipSpecial = false, want true")
	}

	if cfg.Progress == nil {
		t.Fatal("Progress not set")
	}

	cfg.Progress(invoke.TransferProgress{Path: "a.txt", Current: 5, Total: invoke.UnknownTotal})

	if len(seen) != 1 || seen[0].Path != "a.txt" || seen[0].Total != invoke.UnknownTotal {
		t.Errorf("progress callback saw %+v", seen)
	}
}

func TestWithModeZeroIsExplicit(t *testing.T) {
	t.Parallel()

	// Mode 0000 is a valid, expressible override — distinct from "no
	// override", which is the nil pointer.
	cfg := invoke.NewTransferConfig(invoke.WithMode(0))

	if cfg.Mode == nil || *cfg.Mode != 0 {
		t.Errorf("WithMode(0) must set an explicit zero mode, got %v", cfg.Mode)
	}
}
