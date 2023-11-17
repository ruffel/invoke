package mock

import (
	"os/exec"

	"github.com/stretchr/testify/mock"
)

type Mock struct {
	mock.Mock
}

func Provider() *Mock {
	return &Mock{}
}

func (m *Mock) Run(c *exec.Cmd) error {
	args := m.MethodCalled("Run")

	return args.Error(1) //nolint:wrapcheck
}
