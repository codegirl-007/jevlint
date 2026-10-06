#!/usr/bin/env bash
# Compares rule decisions across repository-context variants.
#
# Usage: examples/context-evals/run.sh [rule-id]
#
# Each variant config defines the same rule with a different context setting.
# The script runs jevlint eval for each and prints the matched/total counts so
# the effect of a context source can be measured rather than assumed.
set -euo pipefail

cd "$(dirname "$0")/../.."

rule="${1:-function-name-behavior-mismatch}"
dir="examples/context-evals"
variants=(baseline callees callers related-types imports combined)

printf '%-16s %s\n' "variant" "result"
for variant in "${variants[@]}"; do
	output=$(go run ./cmd/jevlint eval \
		--config "$dir/$variant.json" \
		--rule "$rule" \
		--format json 2>/dev/null || true)
	summary=$(printf '%s' "$output" | python3 -c '
import json, sys
try:
    report = json.load(sys.stdin)
except Exception:
    print("no report")
    raise SystemExit
print("%d/%d matched, %d inconclusive" % (report["matched"], report["total"], report["inconclusive"]))
')
	printf '%-16s %s\n' "$variant" "$summary"
done
