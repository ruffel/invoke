package invoke_test

import (
	"testing"

	"github.com/ruffel/invoke"
)

func TestIOZeroValue(t *testing.T) {
	t.Parallel()

	var stdio invoke.IO

	if stdio.Stdin != nil || stdio.Stdout != nil || stdio.Stderr != nil || stdio.TTY != nil {
		t.Errorf("zero IO must have all-nil fields: %+v", stdio)
	}
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
			if cols != tt.wantCols || rows != tt.wantRows {
				t.Errorf("Size() = (%d, %d), want (%d, %d)", cols, rows, tt.wantCols, tt.wantRows)
			}
		})
	}
}
