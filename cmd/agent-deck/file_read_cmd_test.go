package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2676: `file read` / `file roots` are the CLI side of the web
// terminal's file links and share internal/filelinks with GET /api/files.

func fileReadTree(t *testing.T) (tmp, root string) {
	t.Helper()
	tmp = t.TempDir()
	root = filepath.Join(tmp, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"root/notes.txt": "hello\n",
		"root/blob.bin":  "a\x00b",
		"secret.txt":     "TOP SECRET",
	} {
		if err := os.WriteFile(filepath.Join(tmp, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../secret.txt", filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	return tmp, root
}

func TestFileReadPrintsAFileUnderARoot(t *testing.T) {
	_, root := fileReadTree(t)
	var out, diag bytes.Buffer
	if err := runFileRead([]string{root}, []string{filepath.Join(root, "notes.txt")}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello\n" {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestFileReadJSON(t *testing.T) {
	_, root := fileReadTree(t)
	for name, want := range map[string]fileReadJSON{
		"notes.txt": {ContentType: "text/plain; charset=utf-8", Inline: true, Encoding: "utf-8", Content: "hello\n", Size: 6},
		"blob.bin":  {ContentType: "application/octet-stream", Inline: false, Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte("a\x00b")), Size: 3},
	} {
		var out, diag bytes.Buffer
		path := filepath.Join(root, name)
		if err := runFileRead([]string{root}, []string{path, "--json"}, &out, &diag); err != nil {
			t.Fatal(err)
		}
		var got fileReadJSON
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v (%q)", name, err, out.String())
		}
		want.Path, want.RealPath = path, path
		if got != want {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
}

func TestFileReadRefusesEscapes(t *testing.T) {
	tmp, root := fileReadTree(t)
	cases := map[string]string{
		root + "/../secret.txt":            "outside_roots",
		filepath.Join(root, "link-out"):    "outside_roots",
		filepath.Join(tmp, "secret.txt"):   "outside_roots",
		root:                               "not_regular",
		"root/notes.txt":                   "not_absolute",
		filepath.Join(root, "missing.txt"): "not_found",
	}
	for path, code := range cases {
		var out, diag bytes.Buffer
		err := runFileRead([]string{root}, []string{path, "--json"}, &out, &diag)
		if !errors.Is(err, errFileJSONWritten) {
			t.Fatalf("%s: err %v, want the JSON refusal", path, err)
		}
		var got fileErrorJSON
		if json.Unmarshal(out.Bytes(), &got) != nil || got.Code != code || strings.Contains(out.String(), "TOP SECRET") {
			t.Errorf("%s: %q, want code %s", path, out.String(), code)
		}
		out.Reset()
		if err := runFileRead([]string{root}, []string{path}, &out, &diag); err == nil || out.Len() != 0 {
			t.Errorf("%s: plain read must fail with no output, got err %v out %q", path, err, out.String())
		}
	}
}

func TestFileReadIsOffWithoutRoots(t *testing.T) {
	_, root := fileReadTree(t)
	var out, diag bytes.Buffer
	err := runFileRead(nil, []string{filepath.Join(root, "notes.txt"), "--json"}, &out, &diag)
	var got fileErrorJSON
	if !errors.Is(err, errFileJSONWritten) || json.Unmarshal(out.Bytes(), &got) != nil || got.Code != "disabled" {
		t.Fatalf("err %v out %q", err, out.String())
	}
}

func TestFileRoots(t *testing.T) {
	var out, diag bytes.Buffer
	if err := runFileRoots(nil, nil, []string{"--json"}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"enabled":false,"roots":[],"refused":[]}` {
		t.Fatalf("off: %q", out.String())
	}
	out.Reset()
	if err := runFileRoots([]string{"/srv/a", "/srv/b"}, nil, []string{"--json"}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"enabled":true,"roots":["/srv/a","/srv/b"],"refused":[]}` {
		t.Fatalf("on: %q", out.String())
	}
	out.Reset()
	if err := runFileRoots(nil, nil, nil, &out, &diag); err != nil || !strings.Contains(out.String(), "file_link_roots") {
		t.Fatalf("human off: %v %q", err, out.String())
	}
}

// The remote forwarder probes `<args> --help` on the remote first; an old
// remote fails that probe and the caller gets the "unsupported" payload.
func TestFileReadHelpExitsZeroForTheRemoteProbe(t *testing.T) {
	for _, args := range [][]string{{"read", "/x", "--json", "--help"}, {"roots", "--json", "--help"}, {"read", "--help"}} {
		var out, diag bytes.Buffer
		if err := runFileCommand("default", args, &out, &diag); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
	var out, diag bytes.Buffer
	if err := runFileCommand("default", []string{"--help"}, &out, &diag); err != nil || !strings.Contains(out.String(), "file read") || !strings.Contains(out.String(), "file roots") {
		t.Fatalf("file --help: %v %q", err, out.String())
	}
}

func TestRemoteFileReadIsAProbedRead(t *testing.T) {
	for _, args := range [][]string{{"file", "read", "/srv/x.txt", "--json"}, {"file", "roots", "--json"}} {
		if !isRemoteReadCapability(args) {
			t.Fatalf("%q must forward through the probed remote read", args)
		}
		var out, diag bytes.Buffer
		runner := &fakeRemoteRead{}
		code, err := runRemoteReadIO(context.Background(), runner, "remote", nil, &out, &diag, args)
		var obj map[string]string
		if code != 0 || err != nil || runner.calls != 1 {
			t.Fatalf("%q: code=%d err=%v", args, code, err)
		}
		if json.Unmarshal(runner.unsupported, &obj) != nil || !strings.Contains(obj["error"], "unsupported remote command") {
			t.Fatalf("an old remote must get a JSON unsupported reply, got %s", runner.unsupported)
		}
	}
	if isRemoteReadCapability([]string{"file", "bundle", "/x"}) {
		t.Fatal("file bundle streams a tar and is not a probed read")
	}
}

func TestFileReadCLIUsesConfiguredRoots(t *testing.T) {
	home := t.TempDir()
	tmp, root := fileReadTree(t)
	cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[web]\nfile_link_roots = ['"+root+"', '/', '$FILELINKS_TEST_UNSET/x']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "file", "roots", "--json")
	if code != 0 || !strings.Contains(stdout, `"enabled":true`) || !strings.Contains(stdout, root) {
		t.Fatalf("file roots: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var roots fileRootsJSON
	if err := json.Unmarshal([]byte(stdout), &roots); err != nil || len(roots.Roots) != 1 || len(roots.Refused) != 2 {
		t.Fatalf("file roots must keep only the real root and report / and the unset $VAR: %q", stdout)
	}
	stdout, stderr, code = runAgentDeck(t, home, "file", "read", filepath.Join(root, "notes.txt"))
	if code != 0 || stdout != "hello\n" {
		t.Fatalf("file read: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, _, code = runAgentDeck(t, home, "file", "read", filepath.Join(tmp, "secret.txt"), "--json")
	if code != 1 || !strings.Contains(stdout, `"code":"outside_roots"`) || strings.Contains(stdout, "TOP SECRET") {
		t.Fatalf("file read outside: exit=%d stdout=%q", code, stdout)
	}
}
