#!/usr/bin/env bash
# Gate G0: run a real model through the agent loop once, and record the result.
# Needs ANTHROPIC_API_KEY, ADLC_ANTHROPIC_MODEL and the four
# ADLC_ANTHROPIC_PRICE_* variables (see docs/impl/gate-g0.md).
# Spends real money: a few small requests (the hello agent's budget is 0.10 USD).
set -euo pipefail
cd "$(dirname "$0")/.."

missing=0
for v in ANTHROPIC_API_KEY ADLC_ANTHROPIC_MODEL ADLC_ANTHROPIC_PRICE_INPUT \
	ADLC_ANTHROPIC_PRICE_OUTPUT ADLC_ANTHROPIC_PRICE_CACHE_READ ADLC_ANTHROPIC_PRICE_CACHE_WRITE; do
	if [ -z "${!v:-}" ]; then
		echo "gate-g0: $v is not set" >&2
		missing=1
	fi
done
[ "$missing" -eq 0 ] || exit 1

mkdir -p bin/gate-g0
rm -f bin/gate-g0/events.db bin/gate-g0/events.db-wal bin/gate-g0/events.db-shm

make build

echo "== live adapter and harness tests"
set +e
go test -tags live -count=1 -run 'TestLive' ./internal/model/anthropic/ ./internal/harness/ \
	2>&1 | tee bin/gate-g0/live-tests.txt
tests_rc=${PIPESTATUS[0]}

echo "== adlc run (real model)"
./bin/adlc run examples/hello-anthropic.yaml --task "What does greeting.txt say?" \
	--db bin/gate-g0/events.db | tee bin/gate-g0/run.txt
run_rc=${PIPESTATUS[0]}
set -e

run_id=$(awk '/^run:/{print $2}' bin/gate-g0/run.txt)
if [ -n "$run_id" ]; then
	echo "== trace"
	./bin/adlc trace "$run_id" --db bin/gate-g0/events.db | tee bin/gate-g0/trace.txt
	echo "== cost"
	./bin/adlc cost "$run_id" --db bin/gate-g0/events.db | tee bin/gate-g0/cost.txt
fi

echo
echo "gate-g0: live tests exit $tests_rc, adlc run exit $run_rc; outputs in bin/gate-g0/"
[ "$tests_rc" -eq 0 ] && [ "$run_rc" -eq 0 ]
