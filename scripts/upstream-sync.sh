#!/usr/bin/env bash
# Weekly upstream sync. main is never touched until the sync branch is green.
#
#   scripts/upstream-sync.sh start     fetch, branch sync/<date>, merge, resolve the mechanical conflicts
#   scripts/upstream-sync.sh check     every local gate, in the order that fails fastest
#   scripts/upstream-sync.sh push      record the upstream ref, push the branch, CI runs Sync Gate
#   scripts/upstream-sync.sh land      CI green? fast-forward main and push (this deploys)
#   scripts/upstream-sync.sh summary   what upstream shipped in this sync
set -euo pipefail
cd "$(dirname "$0")/.."
REMOTE=new-api
UP=$REMOTE/main
REPO=unorouter/new-api
REF_FILE=scripts/sync-audit/upstream.ref
export GOWORK=off

say() { printf '\n== %s\n' "$*"; }
die() { printf '\nSTOP: %s\n' "$*" >&2; exit 1; }
branch() { git rev-parse --abbrev-ref HEAD; }
on_sync_branch() { case "$(branch)" in sync/*) ;; *) die "not on a sync/* branch (on $(branch))" ;; esac; }
conflicts() { git diff --name-only --diff-filter=U; }

# Upstream has force-pushed main before. The old commit is then gone from their
# history and a plain merge replays everything since the last shared ancestor.
# The rewritten history still holds a commit with the identical tree: anchor on it.
twin_of() {
  git merge-base --is-ancestor "$1" "$UP" && { echo "$1"; return; }
  git log --format='%H %T' "$UP" | awk -v t="$(git rev-parse "$1^{tree}")" '$2==t && !f{print $1; f=1}'
}

reanchor() {
  local last twin
  last=$(cat "$REF_FILE")
  twin=$(twin_of "$last")
  [ "$twin" = "$last" ] && return 0
  say "upstream rewrote history: $last is no longer in $UP"
  [ -n "$twin" ] || die "no commit in $UP has the tree of $last. Find the equivalent by hand, then: git merge -s ours <it>"
  git merge -s ours --no-edit -m "Anchor on rewritten upstream history

$last is $twin there, identical tree" "$twin"
  echo "anchored on $twin (no file changed)"
}

merge_locale() {
  python3 - "$1" "$UP" <<'PY'
import json, subprocess, sys
path, up_ref = sys.argv[1], sys.argv[2]
show = lambda ref: json.loads(subprocess.run(['git', 'show', f'{ref}:{path}'], capture_output=True, text=True, check=True).stdout)
ours, up = show(':2')['translation'], show(up_ref)['translation']
merged = dict(up)
extra = {k: v for k, v in ours.items() if k not in up}
merged.update(extra)
with open(path, 'w') as f:
    json.dump({'translation': merged}, f, ensure_ascii=False, indent=2)
    f.write('\n')
print(f'{path}: upstream order kept, {len(extra)} prod-only keys appended')
PY
}

cmd_start() {
  [ -z "$(git status --porcelain)" ] || die "working tree is not clean"
  [ "$(branch)" = main ] || die "start from main"
  git fetch -q origin main && git fetch -q "$REMOTE" main
  [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "main differs from origin/main: pull or push first"
  [ "$(git rev-list --count "HEAD..$UP")" -gt 0 ] || die "nothing new upstream"

  local b="sync/$(date +%F)"
  git checkout -q -b "$b"
  reanchor
  say "merging $(git rev-list --count "HEAD..$UP") upstream commits into $b"
  git merge --no-commit --no-ff "$UP" >/dev/null 2>&1 || true

  for f in $(conflicts); do
    case "$f" in
      web/classic/*)                   git rm -q --cached -- "$f" 2>/dev/null; rm -f -- "$f"; echo "rm      $f" ;;
      router/api-router.go)            git checkout --ours -- "$f" && git add -- "$f"; echo "ours    $f   (fold new upstream routes in by hand, list below)" ;;
      go.mod|go.sum|relaykit/go.mod|relaykit/go.sum|i18n/locales/*.yaml)
                                       git checkout --theirs -- "$f" && git add -- "$f"; echo "theirs  $f" ;;
      web/src/i18n/locales/*.json)     merge_locale "$f" && git add -- "$f" ;;
    esac
  done

  say "upstream routes prod does not register (add them in fuego style or list them in scripts/sync-audit/routes-ignore.txt)"
  scripts/sync-audit.sh --routes "$UP"

  say "conflicts left for judgment: $(conflicts | wc -l)"
  conflicts
  cat <<'TXT'

Rules: adopt upstream's structure, keep every prod-only feature, re-apply prod
edits on top of rewritten upstream functions. Why a block exists: claude-mem.
Then: git add -A && git commit --no-edit && scripts/upstream-sync.sh check
TXT
}

cmd_check() {
  on_sync_branch
  [ -z "$(conflicts)" ] || die "unresolved conflicts: $(conflicts | tr '\n' ' ')"
  mkdir -p web/dist && [ -e web/dist/index.html ] || touch web/dist/index.html
  say "build";    go build ./... && (cd relaykit && go build ./...)
  # tidy only once the tree compiles: on a broken tree it drops the relaykit require
  say "tidy";     go mod tidy && (cd relaykit && go mod tidy)
  say "audit";    scripts/sync-audit.sh
  say "vet";      go vet ./... && (cd relaykit && go vet ./...)
  say "go test";  make test
  say "frontend"; (cd web && bun install --frozen-lockfile >/dev/null && bun run typecheck && bun run test)
  local bin; bin=$(mktemp)
  say "boot";     go build -o "$bin" . && scripts/boot-smoke.sh binary "$bin"
  rm -f "$bin"
  [ -z "$(git status --porcelain)" ] || { git status --short; die "checks changed files (tidy?): review, commit, run check again"; }
  say "all local gates green: scripts/upstream-sync.sh push"
}

cmd_push() {
  on_sync_branch
  [ -z "$(git status --porcelain)" ] || die "commit first"
  git merge-base --is-ancestor "$UP" HEAD || die "$UP is not merged into this branch"
  git rev-parse "$UP" > "$REF_FILE"
  git add "$REF_FILE" && git commit -q -m "sync: upstream $(git rev-parse --short "$UP")"
  git push -u origin "$(branch)"
  say "watch: gh run watch -R $REPO \$(gh run list -R $REPO -w 'Sync Gate' -b $(branch) -L1 --json databaseId -q '.[0].databaseId')"
}

cmd_land() {
  on_sync_branch
  local b sha concl
  b=$(branch); sha=$(git rev-parse HEAD)
  [ "$sha" = "$(git rev-parse "origin/$b" 2>/dev/null)" ] || die "branch not pushed at HEAD"
  concl=$(gh run list -R "$REPO" -w 'Sync Gate' -c "$sha" -L1 --json conclusion -q '.[0].conclusion')
  [ "$concl" = success ] || die "Sync Gate for $sha is '${concl:-missing}', not success"
  git fetch -q origin main
  if ! git merge-base --is-ancestor origin/main HEAD; then
    die "main moved since start. Run: git merge origin/main && scripts/upstream-sync.sh check && git push, wait for green, land again"
  fi
  git checkout -q main && git merge -q --ff-only "$b" && git push origin main
  say "pushed. Done only when ArgoCD shows the new image:"
  echo "  kubectl -n services get pods -l app=new-api -o jsonpath='{range .items[*]}{.metadata.name}{\"  \"}{.spec.containers[0].image}{\"\\n\"}{end}'"
}

cmd_summary() {
  local from; from=$(twin_of "$(git show "main:$REF_FILE")")
  git log --no-merges --format='%h %s' "${from:?last synced upstream commit not found}..$UP"
}

case "${1:-}" in
  start|check|push|land|summary) "cmd_$1" ;;
  *) sed -n '2,9p' "$0"; exit 1 ;;
esac
