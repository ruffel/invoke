package invoke_test

import (
	"testing"

	"github.com/ruffel/invoke"
	"github.com/stretchr/testify/assert"
)

func TestIOZeroValue(t *testing.T) {
	t.Parallel()

	var stdio invoke.IO

	assert.Nil(t, stdio.Stdin, "zero IO must have all-nil fields")
	assert.Nil(t, stdio.Stdout, "zero IO must have all-nil fields")
	assert.Nil(t, stdio.Stderr, "zero IO must have all-nil fields")
	assert.Nil(t, stdio.TTY, "zero IO must have all-nil fields")
}

func TestTTYSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tty      invoke.TTY
		wantCols int
		wantRows int
	}{
		{name: "zero value defaults to 80x24", tty: invoke.TTY{}, wantCols: 80, wantRows: 24},
		{name: "explicit size preserved", tty: invoke.TTY{Cols: 120, Rows: 40}, wantCols: 120, wantRows: 40},
		{name: "zero cols defaulted", tty: invoke.TTY{Rows: 50}, wantCols: 80, wantRows: 50},
		{name: "zero rows defaulted", tty: invoke.TTY{Cols: 132}, wantCols: 132, wantRows: 24},
		{name: "negative treated as unset", tty: invoke.TTY{Cols: -1, Rows: -9}, wantCols: 80, wantRows: 24},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cols, rows := tt.tty.Size()

			assert.Equal(t, tt.wantCols, cols)
			assert.Equal(t, tt.wantRows, rows)
		})
	}
}
