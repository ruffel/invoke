package ssh

import (
	"context"
	"errors"

	"github.com/ruffel/invoke"
)

// Upload copies a local path to the remote host. SFTP-backed transfer lands
// in a following change; until then the method reports that plainly.
func (e *Environment) Upload(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	if err := e.checkOpen("upload"); err != nil {
		return err
	}

	return errors.New("ssh: upload: not implemented yet")
}

// Download copies a remote path to the local filesystem. SFTP-backed
// transfer lands in a following change.
func (e *Environment) Download(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	if err := e.checkOpen("download"); err != nil {
		return err
	}

	return errors.New("ssh: download: not implemented yet")
}
