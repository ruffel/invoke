package invoke_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	assert.Nil(t, cfg.Mode, "the default Mode must be nil: preserve the source mode")
	assert.Equal(t, invoke.SymlinkPreserve, cfg.Symlinks)
	assert.False(t, cfg.SkipSpecial)
	assert.Nil(t, cfg.Progress)
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

	wantMode := fs.FileMode(0o600)

	assert.Equal(t, &wantMode, cfg.Mode)
	assert.Equal(t, invoke.SymlinkFollow, cfg.Symlinks)
	assert.True(t, cfg.SkipSpecial)

	require.NotNil(t, cfg.Progress, "Progress not set")

	cfg.Progress(invoke.TransferProgress{Path: "a.txt", Current: 5, Total: invoke.UnknownTotal})

	require.Len(t, seen, 1)

	assert.Equal(t, "a.txt", seen[0].Path)
	assert.Equal(t, invoke.UnknownTotal, seen[0].Total)
}

func TestWithModeZeroIsExplicit(t *testing.T) {
	t.Parallel()

	// Mode 0000 is a valid, expressible override — distinct from "no
	// override", which is the nil pointer.
	cfg := invoke.NewTransferConfig(invoke.WithMode(0))

	wantMode := fs.FileMode(0)

	assert.Equal(t, &wantMode, cfg.Mode, "WithMode(0) must set an explicit zero mode")
}
