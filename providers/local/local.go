package local

import "os/exec"

type Local struct{}

func Provider() *Local { return &Local{} }

func (p *Local) Run(c *exec.Cmd) error {
	return c.Run() //nolint:wrapcheck
}
