package invoke

import "testing"

// TestTailWriterKeepsNewestBytes pins the tail semantics: whatever the
// write pattern, the buffer holds the newest max bytes, and Write always
// accepts the full slice.
func TestTailWriterKeepsNewestBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		max    int
		writes []string
		want   string
	}{
		{name: "under the bound", max: 8, writes: []string{"abc"}, want: "abc"},
		{name: "accumulates across writes", max: 8, writes: []string{"abc", "def"}, want: "abcdef"},
		{name: "overflow drops the oldest", max: 8, writes: []string{"abcdef", "ghij"}, want: "cdefghij"},
		{name: "single write over the bound", max: 8, writes: []string{"0123456789"}, want: "23456789"},
		{name: "write exactly at the bound", max: 8, writes: []string{"01234567"}, want: "01234567"},
		{name: "empty write changes nothing", max: 8, writes: []string{"abc", ""}, want: "abc"},
		{name: "many small writes", max: 4, writes: []string{"a", "b", "c", "d", "e", "f"}, want: "cdef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := &tailWriter{max: tt.max}

			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				if err != nil || n != len(s) {
					t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", s, n, err, len(s))
				}
			}

			if got := string(w.buf); got != tt.want {
				t.Fatalf("buf = %q, want %q", got, tt.want)
			}
		})
	}
}
