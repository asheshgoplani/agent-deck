package events

import "testing"

func TestProfileReaderPreservesStatusBusPolicy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, err := busDirFor("reader-policy")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenReader("reader-policy")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if !reader.ReadOnly() || reader.keepCorrupt != writer.keepCorrupt {
		t.Fatal("profile reader must preserve the status bus policy without a writer")
	}
}

func TestReaderDoesNotCreateAbsentBus(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, err := OpenReader("missing-reader-test")
	if err == nil {
		t.Fatal("reader must not create an absent bus")
	}
}
