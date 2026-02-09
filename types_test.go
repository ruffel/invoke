package invoke

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResult_Success(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result Result
		want   bool
	}{
		{
			name:   "success",
			result: Result{ExitCode: 0, Error: nil},
			want:   true,
		},
		{
			name:   "non-zero exit",
			result: Result{ExitCode: 1, Error: nil},
			want:   false,
		},
		{
			name:   "with error",
			result: Result{ExitCode: 0, Error: errors.New("test error")},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.result.Success())
		})
	}
}

func TestResult_Failed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result Result
		want   bool
	}{
		{
			name:   "success",
			result: Result{ExitCode: 0, Error: nil},
			want:   false,
		},
		{
			name:   "failed",
			result: Result{ExitCode: 1, Error: nil},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.result.Failed())
		})
	}
}

func TestTargetOS_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		os   TargetOS
		want string
	}{
		{"linux", OSLinux, "linux"},
		{"windows", OSWindows, "windows"},
		{"darwin", OSDarwin, "darwin"},
		{"unknown", OSUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.os.String())
		})
	}
}

func TestTargetOS_ShellCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		os     TargetOS
		script string
		want   *Command
	}{
		{
			name:   "linux shell",
			os:     OSLinux,
			script: "echo hello",
			want:   &Command{Cmd: "sh", Args: []string{"-c", "echo hello"}},
		},
		{
			name:   "windows shell (powershell)",
			os:     OSWindows,
			script: "echo hello",
			want:   &Command{Cmd: "powershell", Args: []string{"-NoProfile", "-NonInteractive", "-Command", "echo hello"}},
		},
		{
			name:   "darwin shell",
			os:     OSDarwin,
			script: "echo hello",
			want:   &Command{Cmd: "sh", Args: []string{"-c", "echo hello"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.os.ShellCommand(tt.script))
		})
	}
}

func TestParseTargetOS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		osStr string
		want  TargetOS
	}{
		{"linux", "linux", OSLinux},
		{"windows", "windows", OSWindows},
		{"windows_nt", "windows_nt", OSWindows},
		{"darwin", "darwin", OSDarwin},
		{"macos", "macos", OSDarwin},
		{"unknown", "freebsd", OSUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ParseTargetOS(tt.osStr))
		})
	}
}

func TestTargetOS_DetectLocalOS(t *testing.T) {
	t.Parallel()

	// This will vary by platform, just ensure it doesn't panic
	got := DetectLocalOS()
	assert.GreaterOrEqual(t, int(got), int(OSUnknown))
	assert.LessOrEqual(t, int(got), int(OSDarwin))
}

func TestCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  Command
		want string
	}{
		{
			name: "command only",
			cmd:  Command{Cmd: "ls"},
			want: "ls",
		},
		{
			name: "command with args",
			cmd:  Command{Cmd: "ls", Args: []string{"-la", "/tmp"}},
			want: "ls -la /tmp",
		},
		{
			name: "args with spaces",
			cmd:  Command{Cmd: "echo", Args: []string{"hello world", "foo"}},
			want: "echo \"hello world\" foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.cmd.String())
		})
	}
}

func TestCommand_ParseCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cmdStr  string
		want    Command
		wantErr bool
	}{
		{
			name:   "simple command",
			cmdStr: "ls",
			want:   Command{Cmd: "ls", Args: []string{}},
		},
		{
			name:   "command with args",
			cmdStr: "ls -la /tmp",
			want:   Command{Cmd: "ls", Args: []string{"-la", "/tmp"}},
		},
		{
			name:   "quoted args",
			cmdStr: `echo "hello world" foo`,
			want:   Command{Cmd: "echo", Args: []string{"hello world", "foo"}},
		},
		{
			name:   "extra spaces",
			cmdStr: "  ls   -la   /tmp  ",
			want:   Command{Cmd: "ls", Args: []string{"-la", "/tmp"}},
		},
		{
			name:    "empty command",
			cmdStr:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCommand(tt.cmdStr)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, &tt.want, got)
			}
		})
	}
}

func TestNewCommand(t *testing.T) {
	t.Parallel()

	cmd := NewCommand("ls", "-la", "/tmp")
	assert.Equal(t, "ls", cmd.Cmd)
	assert.Equal(t, []string{"-la", "/tmp"}, cmd.Args)
}

func TestExitError_Error(t *testing.T) {
	t.Parallel()

	t.Run("with command", func(t *testing.T) {
		t.Parallel()

		e := &ExitError{
			Command:  &Command{Cmd: "ls", Args: []string{"-la"}},
			ExitCode: 1,
		}
		assert.Equal(t, "command \"ls -la\" exited with code 1", e.Error())
	})

	t.Run("without command", func(t *testing.T) {
		t.Parallel()

		e := &ExitError{
			ExitCode: 1,
		}

		assert.NotPanics(t, func() {
			assert.Equal(t, "command exited with code 1", e.Error())
		})
	})
}
