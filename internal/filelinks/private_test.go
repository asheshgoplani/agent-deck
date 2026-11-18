package filelinks

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phone finish, review rework 1 (S2): `file read` and GET /api/files refuse
// genuinely private names under the roots (OpenReadable): .env*, .git/
// internals, .ssh/.aws/.gnupg/…, .npmrc/.netrc/.pypirc, key and certificate
// files, credential and secret DATA files. Ordinary code and config files
// whose name only CONTAINS a word like "token" (tokens.go, design-tokens.css,
// everything under .github/) stay readable.

func writeTree(t *testing.T, root string, rels []string, body string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenReadableRefusesPrivateNames(t *testing.T) {
	root := t.TempDir()
	private := []string{".env", ".ENV", ".env.local", ".env.production", ".git/config", "sub/.Git/config", ".ssh/id_x",
		".aws/credentials", ".gnupg/pubring.kbx", ".npmrc", ".netrc", ".pypirc", ".git-credentials", ".pgpass", ".docker/config.json",
		".kube/config", "id_rsa", "id_ed25519.pub", "server.pem", "tls.key", "cert.p12", "store.jks", "vault.kdbx", "state.tfstate",
		"prod.tfvars", "Secret.TXT", "API_KEY.md", "db_password.txt", "github-token.txt", "aws_credentials.txt", "credentials.json",
		"client_secret_123.json", "secrets/notes.md", "token.json", "my-secret.yaml", ".claude/.credentials.json"}
	writeTree(t, root, private, "SECRET-VALUE-42")
	if err := os.Symlink(".env", filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/.Git", filepath.Join(root, "g")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range append(private, "notes.txt", "g/config", "sub/../.env") {
		f, err := OpenReadable([]string{root}, root+"/"+rel)
		if f != nil {
			f.Close()
		}
		if !errors.Is(err, ErrPrivate) {
			t.Errorf("%s: %v, want ErrPrivate", rel, err)
		}
	}
}

// The 88 tracked files of this repository the round-1 rule refused
// (testdata/repo-names-88.txt): all readable now except the two that are
// credential DATA files by name (gmail testdata credentials.json, token.json).
func TestOpenReadableKeepsOrdinaryRepoFiles(t *testing.T) {
	fh, err := os.Open("testdata/repo-names-88.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var names []string
	for sc := bufio.NewScanner(fh); sc.Scan(); {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			names = append(names, l)
		}
	}
	if len(names) != 88 {
		t.Fatalf("%d names, want 88", len(names))
	}
	root := t.TempDir()
	writeTree(t, root, names, "ordinary")
	refused := 0
	for _, rel := range names {
		f, err := OpenReadable([]string{root}, filepath.Join(root, filepath.FromSlash(rel)))
		if f != nil {
			f.Close()
		}
		dataName := strings.HasSuffix(rel, "/gmail/credentials.json") || strings.HasSuffix(rel, "/gmail/token.json")
		switch {
		case dataName && !errors.Is(err, ErrPrivate):
			t.Errorf("%s: %v, want ErrPrivate (a credential data file)", rel, err)
		case !dataName && err != nil:
			t.Errorf("%s: %v, want readable", rel, err)
		}
		if err != nil {
			refused++
		}
	}
	if refused != 2 {
		t.Errorf("refused %d of 88, want 2", refused)
	}
}
