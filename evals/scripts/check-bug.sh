#!/usr/bin/env bash
# Checks one seeded bug: usage: evals/scripts/check-bug.sh <bug-id>
set -euo pipefail
id="$1"
root="$(git rev-parse --show-toplevel)"
bug="$root/evals/suites/go-bugs/$id"
work="$(mktemp -d)"
cp -R "$root/evals/fixtures/ledger/." "$work/"
pkgdir="$(sed -n 's/^test_package: *//p' "$bug/meta.yaml")"
[ -n "$pkgdir" ] || { echo "$id: meta.yaml has no test_package line"; exit 1; }
dest="$pkgdir/adlc_hidden_test.go"
pkg="./$pkgdir"
run='^TestHidden'
cd "$work"
step() { printf '%-40s' "$id: $1"; }
step "clean: hidden test passes"
cp "$bug/hidden_test.go" "$dest"
go test -count=1 -run "$run" "$pkg" >/dev/null && echo ok || { echo FAIL; exit 1; }
rm -f -- "$dest"
step "bug.patch applies"
git apply -p1 "$bug/bug.patch" && echo ok
step "bugged: vet + visible tests pass"
{ go vet ./... && go test -count=1 ./...; } >/dev/null 2>&1 && echo ok || { echo FAIL; exit 1; }
step "bugged: hidden test fails"
cp "$bug/hidden_test.go" "$dest"
if go test -count=1 -run "$run" "$pkg" >/dev/null 2>&1; then echo FAIL; exit 1; else echo ok; fi
step "fix.patch applies"
git apply -p1 "$bug/fix.patch" && echo ok
step "fixed: all tests pass"
go test -count=1 ./... >/dev/null && echo ok || { echo FAIL; exit 1; }
