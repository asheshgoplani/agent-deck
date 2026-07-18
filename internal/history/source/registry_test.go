package source

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReadRegistryKeepsLiveDropsDead(t *testing.T) {
	dir := t.TempDir()
	self := os.Getpid()
	live := `{"pid":` + strconv.Itoa(self) + `,"sessionId":"aaa","cwd":"/work/app","status":"busy","updatedAt":123,"name":"n"}`
	dead := `{"pid":2147480000,"sessionId":"bbb","cwd":"/work/x","status":"idle"}`
	os.WriteFile(filepath.Join(dir, strconv.Itoa(self)+".json"), []byte(live), 0o644)
	os.WriteFile(filepath.Join(dir, "2147480000.json"), []byte(dead), 0o644)

	reg, err := ReadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg["bbb"]; ok {
		t.Error("dead pid session should be dropped")
	}
	l, ok := reg["aaa"]
	if !ok || l.CWD != "/work/app" || l.RawStatus != "busy" || l.PID != self {
		t.Fatalf("live entry = %+v ok=%v", l, ok)
	}
}

func TestReadRegistryMissingDirEmpty(t *testing.T) {
	reg, err := ReadRegistry(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(reg) != 0 {
		t.Fatalf("missing dir: reg=%v err=%v", reg, err)
	}
}
