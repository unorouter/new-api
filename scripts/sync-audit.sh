#!/usr/bin/env bash
# Invariants an upstream merge must not break. Runs in CI on every sync branch
# and locally from upstream-sync.sh. Exit 1 = a decision to make, not noise.
#
#   scripts/sync-audit.sh            check
#   scripts/sync-audit.sh --accept   after reviewing new snapshot lines, record them
#   scripts/sync-audit.sh --routes <upstream-ref>   upstream route paths prod does not register
set -uo pipefail
export LC_ALL=C   # sort and comm must agree between a laptop and the CI runner
cd "$(dirname "$0")/.."
D=scripts/sync-audit
bad=0
fail() { echo "FAIL  $*"; bad=1; }

if [ "${1:-}" = --routes ]; then
  ref=${2:?upstream ref}
  paths() { grep -ohE '"/[A-Za-z0-9_:/.*-]*"' | sort -u; }
  up=$(for f in $(git ls-tree -r --name-only "$ref" router/ | grep '\.go$' | grep -v _test); do git show "$ref:$f"; done | paths)
  ours=$(cat router/*.go | paths)
  comm -23 <(echo "$up") <(echo "$ours") | grep -vxFf <(grep -v '^#' "$D/routes-ignore.txt") || true
  exit 0
fi

# --- hard checks -------------------------------------------------------------
m=$(git grep -nE '^(<<<<<<<|>>>>>>>) ' -- '*.go' '*.ts' '*.tsx' '*.json' '*.yaml' '*.yml' go.mod | head -5)
[ -z "$m" ] || fail "conflict markers:"$'\n'"$m"
ls go.work* >/dev/null 2>&1 && fail "go.work present: it hides module errors the Docker build hits"
[ -z "$(git ls-files web/classic | head -1)" ] || fail "web/classic is tracked again"
[ ! -e relaykit/dto/user_settings.go ] || fail "relaykit/dto/user_settings.go is back: UserSetting lives in root types/ only"

while IFS= read -r line; do
  case "$line" in ''|'#'*) continue ;; esac
  f=${line%% :: *}; p=${line#* :: }
  grep -qF -- "$p" "$f" 2>/dev/null || fail "dropped prod block: $f no longer contains: $p"
done < "$D/keep.txt"
while IFS= read -r line; do
  case "$line" in ''|'#'*) continue ;; esac
  f=${line%% :: *}; p=${line#* :: }
  grep -qF -- "$p" "$f" 2>/dev/null && fail "removed code is back: $f contains: $p"
done < "$D/forbid.txt"

# Go does not flag an unused exported func, so a merge can delete the wiring of a
# check while the check and its tests stay green.
for fn in RequirePermission authz.Can RootAuth AdminAuth ModAuth NoPAT SessionOnly SecureVerificationRequired IsTrustedNetwork; do
  n=$(grep -rn --include=*.go -F "$fn(" . | grep -v _test.go | grep -vc "func ${fn##*.}(")
  [ "$n" -gt 0 ] || fail "enforcement helper $fn has no caller left"
done

# --- snapshots: additions fail until reviewed and accepted ----------------------
norm() { sed -E 's/[[:space:]]+/ /g; s/^ //; s/ $//' | sort -u; }
snap_unguarded() { grep -hE 'AdminAuth\(\)|RootAuth\(\)|SyncAuth\(|BotAuth\(' router/*.go | grep -v _test | grep -vE 'NoPAT|SessionOnly' | norm; }
snap_headers() { grep -rhoE '(GetHeader|Header\.Get)\("[^"]+"\)' --include=*.go middleware relay service | grep -oE '"[^"]+"' | tr 'A-Z' 'a-z' | norm; }
snap_admin() { grep -rnE 'IsAdmin\(|IsRoot\(|RoleAdminUser|RoleRootUser' --include=*.go middleware relay service model | grep -v _test.go | sed -E 's/^([^:]+):[0-9]+:/\1: /' | norm; }

for s in unguarded headers admin; do
  cur=$(snap_$s)
  if [ "${1:-}" = --accept ]; then echo "$cur" > "$D/$s.snap"; continue; fi
  new=$(comm -13 "$D/$s.snap" <(echo "$cur"))
  [ -z "$new" ] && continue
  case $s in
    unguarded) fail "privileged route group without NoPAT/SessionOnly (read-only? then accept):" ;;
    headers)   fail "relay path reads a new request header (caller controlled input):" ;;
    admin)     fail "new admin/root branch outside the controllers (what does it do for a token owned by an admin?):" ;;
  esac
  echo "$new" | sed 's/^/        /'
done
[ "${1:-}" = --accept ] && { echo "snapshots recorded"; exit 0; }

[ "$bad" = 0 ] && echo "sync audit ok"
exit "$bad"
