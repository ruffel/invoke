package ssh

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestCommandInjection(t *testing.T) {
	// This test demonstrates the vulnerability in command construction.
	// It doesn't need a real SSH connection, just unit testing the buildFullCommand logic.
	
	// Case 1: Argument with space (currently handled)
	cmd1 := invoke.NewCommand("echo", "hello; whoami")
	fullCmd1 := buildFullCommand(cmd1, false)
	t.Logf("Cmd1: %s", fullCmd1)
	assert.Contains(t, fullCmd1, "'hello; whoami'", "Should quote args with spaces (single quotes)")

	// Case 2: Argument WITHOUT space but WITH metacharacter (VULNERABLE)
	cmd2 := invoke.NewCommand("echo", "hello;whoami")
	fullCmd2 := buildFullCommand(cmd2, false)
	t.Logf("Cmd2: %s", fullCmd2)
	
	// The naive implementation will output: echo hello;whoami
	// Which executes 'echo hello' then 'whoami'.
	// A correct implementation should output: echo "hello;whoami" or 'hello;whoami'
	
	if fullCmd2 == "echo hello;whoami" {
		t.Log("Vulnerability confirmed: Command was not quoted.")
		t.Fail() // Mark test as failed
	} else {
		// Check if it IS quoted safely
		isQuoted := (fullCmd2 == "echo \"hello;whoami\"") || (fullCmd2 == "echo 'hello;whoami'")
		assert.True(t, isQuoted, "Command argument should be quoted to prevent injection")
	}
}
