package events

import "testing"

func TestReaderDoesNotCreateAbsentBus(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, err := OpenReader("missing-reader-test")
	if err == nil {
		t.Fatal("reader must not create an absent bus")
	}
}
