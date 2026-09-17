#!/usr/bin/env bash
# Boots the gateway as MASTER against an empty Postgres and asserts it serves.
# Master runs the migrations and InitChannelCache that slaves skip, so this is
# the only check that catches a merge that compiles but cannot start.
#
#   scripts/boot-smoke.sh image ghcr.io/unorouter/new-api:<tag>   # CI
#   scripts/boot-smoke.sh binary ./new-api                        # local
set -euo pipefail

MODE=${1:?usage: boot-smoke.sh image|binary <ref>}
REF=${2:?usage: boot-smoke.sh image|binary <ref>}
PG=newapi-smoke-pg
APP=newapi-smoke-app
NET=newapi-smoke-net
PORT=${SMOKE_PORT:-13900}
LOG=$(mktemp)
APP_PID=""

cleanup() {
  [ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null || true
  docker rm -f "$APP" "$PG" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
# SMOKE_KEEP leaves only the database behind, for a caller that wants to inspect it
trap '[ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null; [ -n "${SMOKE_KEEP:-}" ] || cleanup; rm -f "$LOG"' EXIT
cleanup

docker network create "$NET" >/dev/null
docker run -d --name "$PG" --network "$NET" -p 127.0.0.1:15439:5432 \
  -e POSTGRES_PASSWORD=smoke -e POSTGRES_DB=newapi "${SMOKE_PG_IMAGE:-postgres:15-alpine}" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$PG" pg_isready -U postgres -d newapi >/dev/null 2>&1 && break
  sleep 1
done
sleep 2   # the entrypoint restarts postgres once after initdb

# Optional: an executable that loads a schema or data into the container named in $1
# before the gateway starts, to rehearse a migration against a real schema.
if [ -n "${SMOKE_SEED:-}" ]; then "$SMOKE_SEED" "$PG"; fi

ENVS=(SESSION_SECRET=smoke-session-secret CRYPTO_SECRET=smoke-crypto-secret NODE_TYPE=master PORT="$PORT")
if [ "$MODE" = image ]; then
  args=(); for e in "${ENVS[@]}"; do args+=(-e "$e"); done
  docker run -d --name "$APP" --network "$NET" -p "127.0.0.1:$PORT:$PORT" "${args[@]}" \
    -e SQL_DSN="postgresql://postgres:smoke@$PG:5432/newapi?sslmode=disable" "$REF" >/dev/null
  logs() { docker logs "$APP" 2>&1; }
  alive() { [ "$(docker inspect -f '{{.State.Running}}' "$APP" 2>/dev/null)" = true ]; }
else
  env "${ENVS[@]}" SQL_DSN="postgresql://postgres:smoke@127.0.0.1:15439/newapi?sslmode=disable" \
    "$REF" --log-dir "$(mktemp -d)" >"$LOG" 2>&1 &
  APP_PID=$!
  logs() { cat "$LOG"; }
  alive() { kill -0 "$APP_PID" 2>/dev/null; }
fi

code() { curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT$1" || true; }
fail() { echo "BOOT SMOKE FAILED: $1"; if [ -n "${SMOKE_LOG_OUT:-}" ]; then logs > "$SMOKE_LOG_OUT"; fi; logs | tail -60; exit 1; }

ok=0
for _ in $(seq 1 90); do
  alive || break
  if [ "$(code /api/status)" = 200 ]; then ok=1; break; fi
  sleep 1
done
[ "$ok" = 1 ] || fail "/api/status never returned 200"
sleep 5   # the cache and task goroutines that kill master start after the listener
alive || fail "process died after serving"
if logs | grep -E '^panic:|\[FATAL\]|SQLSTATE'; then fail "fatal line in the boot log"; fi
[ "$(code /api/user/self)" = 401 ] || fail "/api/user/self without a session is not 401"
if [ -n "${SMOKE_LOG_OUT:-}" ]; then logs > "$SMOKE_LOG_OUT"; fi
echo "boot smoke ok: migrations ran, master served /api/status"
