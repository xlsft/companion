#!/usr/bin/env bash
# Fails on any vulnerability whose vulnerable code is reachable, except the
# reviewed ones in ALLOW below. Each exception needs a reason and should be
# removed as soon as a fixed version exists.
set -euo pipefail

# GO-2026-5932: golang.org/x/crypto/openpgp, deprecated, no fixed version.
#   Pulled in only by sigstore's test CA (internal/update/updatetest, used by
#   tests and cmd/spikes/update); the app reaches it only through interface
#   dispatch in govulncheck's conservative call graph and never parses PGP.
ALLOW=(GO-2026-5932)

report=$(go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags novulkan -format json ./...)
found=$(printf '%s\n' "$report" | jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' | sort -u)

status=0
while IFS= read -r id; do
	[ -n "$id" ] || continue
	if printf '%s\n' "${ALLOW[@]}" | grep -qx "$id"; then
		echo "allowed (reviewed): $id"
		continue
	fi
	echo "::error::reachable vulnerability $id — see https://pkg.go.dev/vuln/$id"
	status=1
done <<<"$found"
if [ "$status" -ne 0 ]; then
	# Show the human-readable traces for the failures.
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -tags novulkan ./... || true
fi
exit "$status"
