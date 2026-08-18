#!/bin/sh
# Chart checks that a person would otherwise have to remember to run.
#
# They are here rather than in a CI file so that changing the chart and
# checking the chart are the same gesture — a template guard nobody exercises
# is a comment with extra syntax.
set -eu
cd "$(dirname "$0")"

fail() { echo "FAIL: $1" >&2; exit 1; }

helm lint . >/dev/null || fail "helm lint"

# Accounts off: no database, no volume, nothing to back up.
out=$(helm template t . )
echo "$out" | grep -q -- "--db=" && fail "accounts disabled still passes --db"
echo "$out" | grep -q "kind: PersistentVolumeClaim" && fail "accounts disabled still makes a PVC"

# Accounts on: the flag, the claim and the mount all have to appear, because
# any one of them missing is a celld that starts and then cannot open its
# database.
out=$(helm template t . --set accounts.enabled=true)
for want in -- "--db=/var/lib/agentcell/agentcell.db" "kind: PersistentVolumeClaim" "mountPath: /var/lib/agentcell" "claimName: celld-accounts"; do
  [ "$want" = "--" ] && continue
  echo "$out" | grep -q -- "$want" || fail "accounts enabled is missing: $want"
done

# The first administrator's password comes from a Secret.
out=$(helm template t . --set accounts.enabled=true \
  --set accounts.bootstrapAdmin=you@example.com --set accounts.bootstrapPasswordSecret=boot)
echo "$out" | grep -q -- "--bootstrap-admin=you@example.com" || fail "bootstrap admin flag missing"
echo "$out" | grep -q "AGENTCELL_BOOTSTRAP_PASSWORD" || fail "bootstrap password env missing"

# Two writers on one SQLite file is two account systems wearing one name.
# The chart must refuse to render, not install and fail later.
# previewKeySecret is set so the chart's OTHER multi-replica guard is
# satisfied: without it this would pass for the wrong reason, proving only
# that some rule fired rather than this one.
if helm template t . --set accounts.enabled=true --set celld.replicas=2 \
     --set previewKeySecret=pk >/dev/null 2>&1; then
  fail "accounts + replicas=2 rendered; it must be refused"
fi
helm template t . --set accounts.enabled=true --set celld.replicas=2 --set previewKeySecret=pk 2>&1 \
  | grep -q "SQLite" || fail "the refusal did not explain that SQLite takes one writer"
# An administrator with nowhere to read a password from is the same problem.
if helm template t . --set accounts.enabled=true --set accounts.bootstrapAdmin=you@example.com >/dev/null 2>&1; then
  fail "bootstrapAdmin without a password Secret rendered; it must be refused"
fi

echo "chart OK"
