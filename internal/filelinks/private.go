package filelinks

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrPrivate is returned by OpenReadable for a genuinely private name.
var ErrPrivate = errors.New("private file: dotenv, keys, credentials and VCS internals are not read")

// OpenReadable is Open plus the private-name rule that `agent-deck file
// read` and GET /api/files apply: a file is refused when any path component
// below the root (on the symlink-resolved path, compared case-insensitively)
// is private:
//
//   - .env, .env.*, *.env and .envrc (any dotenv file);
//   - secret stores and VCS internals: .git, .ssh, .aws, .gnupg, .docker,
//     .kube, .azure, .password-store, .terraform.d, .npmrc, .netrc, .pypirc,
//     .git-credentials, .pgpass, .my.cnf, .htpasswd, .vault-token,
//     .credentials.json, .secrets, .credentials, .s3cfg, .boto, shell and
//     database histories, gcloud credentials.db / access_tokens.db /
//     legacy_credentials;
//   - key and certificate files: *.pem *.key *.p12 *.pfx *.kdbx *.kdb
//     *.keystore *.jks *.ppk *.gpg *.pgp *.ovpn *.tfstate(.*) *.tfvars, and
//     id_rsa / id_dsa / id_ecdsa / id_ed25519 (with .pub);
//   - credential data: a directory, or a DATA file (.txt .md .json .yaml .yml
//     .toml .ini .cfg .conf .env .properties .xml .csv .log or no extension),
//     whose name contains secret, credential, password, passwd, token,
//     apikey / api_key / api-key or private_key, or starts with
//     client_secret.
//
// Code and other files whose name only contains such a word (tokens.go,
// design-tokens.css, session_send_token_test.go) and ordinary dotfiles
// (.github/, .gitignore, .golangci.yml) are readable. The phone's pages use
// the stricter openpages rule on top of Open.
func OpenReadable(roots []string, path string) (*File, error) {
	f, err := Open(roots, path)
	if err != nil {
		return nil, err
	}
	for _, realRoot := range resolveRoots(roots) {
		if rel, ok := within(realRoot, f.Path); ok {
			if PrivatePath(rel) {
				_ = f.Close()
				return nil, ErrPrivate
			}
			return f, nil
		}
	}
	_ = f.Close()
	return nil, ErrOutside
}

// PrivatePath reports whether rel (relative to its root) has a private
// component (see OpenReadable).
func PrivatePath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		if privateComponent(p, i < len(parts)-1) {
			return true
		}
	}
	return false
}

var privateDotNames = map[string]bool{".git": true, ".ssh": true, ".aws": true, ".gnupg": true, ".docker": true, ".kube": true,
	".azure": true, ".password-store": true, ".terraform.d": true, ".npmrc": true, ".netrc": true, ".pypirc": true,
	".git-credentials": true, ".pgpass": true, ".my.cnf": true, ".htpasswd": true, ".vault-token": true, ".credentials.json": true,
	".envrc": true, ".secrets": true, ".credentials": true, ".s3cfg": true, ".boto": true, ".bash_history": true, ".zsh_history": true,
	".python_history": true, ".psql_history": true, ".mysql_history": true, "credentials.db": true, "access_tokens.db": true,
	"legacy_credentials": true}

var privateSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".kdbx", ".kdb", ".keystore", ".jks", ".ppk", ".gpg", ".pgp", ".ovpn", ".tfstate", ".tfvars", ".env"}

var privatePrefixes = []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "client_secret"}

var privateWords = []string{"secret", "credential", "password", "passwd", "token", "apikey", "api_key", "api-key", "private_key", "privatekey", "private-key", "htpasswd"}

var dataExts = map[string]bool{"": true, ".txt": true, ".md": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true,
	".cfg": true, ".conf": true, ".env": true, ".properties": true, ".xml": true, ".csv": true, ".log": true}

func privateComponent(name string, dir bool) bool {
	n := strings.ToLower(name)
	if n == "" || n == "." || n == ".." {
		return false
	}
	if n == ".env" || strings.HasPrefix(n, ".env.") || privateDotNames[n] {
		return true
	}
	for _, p := range privatePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range privateSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	if strings.Contains(n, ".tfstate.") { // terraform.tfstate.backup, x.tfstate.<ts>.backup
		return true
	}
	if !dir && !dataExts[filepath.Ext(n)] {
		return false
	}
	for _, w := range privateWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}
