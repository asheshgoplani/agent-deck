#!/usr/bin/env bash
# Self-test for scripts/affected-tests.sh on a throwaway module: a <- b <- c, d alone,
# e with an embedded file, f's test builds ./d. Needs git, go, python3; no network.
set -euo pipefail
script="$(cd "$(dirname "$0")/../.." && pwd)/scripts/affected-tests.sh"
tmp=$(mktemp -d); trap 'cd /; gio trash "$tmp" 2>/dev/null || true' EXIT
cd "$tmp"
git init -q . && git config user.email t@t && git config user.name t
printf 'module example.com/m\n\ngo 1.21\n' > go.mod
mkdir -p a b c d e f docs
printf 'package a\nfunc A() int { return 1 }\n' > a/a.go
printf 'package b\nimport "example.com/m/a"\nfunc B() int { return a.A() }\n' > b/b.go
printf 'package c\nimport "example.com/m/b"\nfunc C() int { return b.B() }\n' > c/c.go
printf 'package d\nfunc D() {}\n' > d/d.go
printf 'package e\nimport _ "embed"\n//go:embed data.txt\nvar X string\n' > e/e.go; echo x > e/data.txt
printf 'package f\nimport "testing"\nfunc TestF(t *testing.T) { _ = "go build ./d" }\n' > f/f_test.go
printf 'package f\n' > f/f.go
echo doc > docs/guide.md
git add -A && git commit -qm base && base=$(git rev-parse HEAD)
fail=0
expect() { # name, expected (space list), actual
  if [ "$2" != "$3" ]; then echo "FAIL $1: want [$2] got [$3]"; fail=1; else echo "ok   $1"; fi
}
sel() { bash "$script" --list "$base" | tr '\n' ' ' | sed 's/ $//'; }
echo '// c' >> a/a.go;      expect "leaf change widens to importers" "./a ./b ./c" "$(sel)"; git checkout -q .
echo '// c' >> c/c.go;      expect "top package only" "./c" "$(sel)"; git checkout -q .
echo y >> e/data.txt;      expect "embedded file" "./e" "$(sel)"; git checkout -q .
echo '// d' >> d/d.go;      expect "test that builds ./d" "./d ./f" "$(sel)"; git checkout -q .
echo z >> docs/guide.md;   expect "unmentioned doc" "" "$(sel)"; git checkout -q .
echo '// g' >> go.mod;      expect "go.mod => full" "./..." "$(sel)"; git checkout -q .
echo 'package q' > q.go; git add q.go; expect "unplaceable .go file => full" "./..." "$(sel)"
exit $fail
