package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/ruffel/invoke"
)

// Upload copies a local path into the container. Archive-backed transfer
// lands in a following change; until then the method reports that
// plainly.
func (e *Environment) Upload(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	if err := e.checkOpen("upload"); err != nil {
		return err
	}

	return errors.New("docker: upload: not implemented yet")
}

// Download copies a path out of the container. Archive-backed transfer
// lands in a following change.
func (e *Environment) Download(_ context.Context, _, _ string, _ ...invoke.TransferOption) error {
	if err := e.checkOpen("download"); err != nil {
		return err
	}

	return errors.New("docker: download: not implemented yet")
}

// randomSuffix returns hex material for a name that will not collide with
// a concurrent command's.
func randomSuffix() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "fallback"
	}

	return hex.EncodeToString(buf[:])
}
