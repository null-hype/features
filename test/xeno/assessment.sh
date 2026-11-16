#!/bin/bash
# Local fixtures only. The Go tests exercise the Jev request/response adapter.
set -euo pipefail

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
cat >"$work/bin/herdr" <<'HERDR'
#!/bin/sh
printf '%s\n' "$*" >"$XENO_TEST_METADATA"
HERDR
cat >"$work/bin/pass-cli" <<'PASS'
#!/bin/sh
printf '%s\n' '[]'
PASS
chmod +x "$work/bin/"*
export PATH="$work/bin:$PATH" HERDR_BIN_PATH="$work/bin/herdr"
export HERDR_PANE_ID=fixture-pane XDG_STATE_HOME="$work/state" XENO_STATE_DIR="$work/xeno"
export XENO_RUN_DIR="$work/run" XENO_TEST_METADATA="$work/metadata"
unset TYPESAFE_API_KEY PROTON_PASS_PERSONAL_ACCESS_TOKEN XENO_PASS_AGENT

# The original verifier/falsifier/jev reporting interface still works.
test "$(xeno verifier confirmed)" = uncontrolled
test "$(xeno falsifier failed)" = unverified
test "$(xeno jev 0.9)" = confirmed
test "$(xeno falsifier refuted)" = refuted

# An empty audit cannot leave a previous optimistic pane judgment behind.
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":"Done"}}' >"$work/trace.jsonl"
if xeno assess "$work/trace.jsonl"; then
    echo 'assessment accepted missing evidence' >&2
    exit 1
fi
jq -e '.version == 1 and .findings[0].severity == "warning" and (.findings[0].message | contains("no ItemRead reasons"))' "$XENO_RUN_DIR/findings.json"
grep -q 'xeno_reason=unverified' "$XENO_TEST_METADATA"
test "$(stat -c %a "$XENO_RUN_DIR/evidence/reason-assessment.json" 2>/dev/null || stat -f %Lp "$XENO_RUN_DIR/evidence/reason-assessment.json")" = 600

# Clear removes Xeno diagnostics while keeping another producer's finding.
jq '.findings += [{"id":"another.producer"}]' "$XENO_RUN_DIR/findings.json" >"$work/other.json"
mv "$work/other.json" "$XENO_RUN_DIR/findings.json"
xeno clear
jq -e '.findings == [{"id":"another.producer"}]' "$XENO_RUN_DIR/findings.json"
grep -q -- '--clear-token xeno_reason' "$XENO_TEST_METADATA"
