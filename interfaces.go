package invoke

import "os/exec"

type Provider interface {
	Run(c *exec.Cmd) error
}
