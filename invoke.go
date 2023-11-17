package invoke

import (
	"os/exec"

	"github.com/ruffel/invoke/providers/local"
)

type Invoker struct {
	provider Provider
}

func New() (*Invoker, error) {
	return &Invoker{provider: local.Provider()}, nil
}

func NewWithProvider(p Provider) (*Invoker, error) {
	return &Invoker{provider: p}, nil
}

func (i *Invoker) Run(c *exec.Cmd) error {
	return nil
}
