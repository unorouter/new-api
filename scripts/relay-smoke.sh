#!/usr/bin/env bash
# Sends a fixed set of relay requests through one gateway binary against a mock
# upstream and records what the client saw and what was billed. Run it for two
# binaries and diff the output dirs: a difference is a behavior change.
#
#   scripts/relay-smoke.sh <binary> <outdir>
#   scripts/relay-smoke.sh compare <binary-a> <binary-b>
set -euo pipefail
cd "$(dirname "$0")/.."

if [ "${1:-}" = compare ]; then
  a=$(mktemp -d); b=$(mktemp -d)
  "$0" "$2" "$a" >/dev/null
  "$0" "$3" "$b" >/dev/null
  if diff -ru "$a" "$b"; then echo "relay smoke: identical client responses and billing ($(ls "$a" | wc -l) records)"; rm -rf "$a" "$b"; else echo; echo "relay smoke: DIFFERENT, see $a vs $b"; exit 1; fi
  exit 0
fi

BIN=$(realpath "${1:?binary}"); OUT=${2:?outdir}; mkdir -p "$OUT"
PORT=${SMOKE_PORT:-13910}; MOCK_PORT=13911
WORK=$(mktemp -d); DB="$WORK/smoke.db"; KEY=smokesmokesmokesmokesmokesmokesmokesmokesmokesmok
pids=()
if ss -ltn | grep -qE ":($PORT|$MOCK_PORT) "; then echo "port $PORT or $MOCK_PORT is already in use"; exit 1; fi
trap 'kill "${pids[@]}" 2>/dev/null || true; wait 2>/dev/null || true; rm -rf "$WORK" || true' EXIT

MOCK_PORT=$MOCK_PORT exec bun scripts/relay-smoke/mock.ts >"$WORK/mock.log" 2>&1 & pids+=($!)
# Token and channel keys are sealed: the pepper below signs the smoke token's
# key_hash, the channels carry no key at all (the mock checks none).
SMOKE_SECRET=$(head -c 32 /dev/zero | tr '\0' 'a' | base64)
KEY_HASH=$(printf %s "$KEY" | openssl dgst -sha256 -mac HMAC -macopt hexkey:$(printf %s "$SMOKE_SECRET" | base64 -d | xxd -p -c 64) | sed 's/^.*= //')
(cd "$WORK" && exec env -u SQL_DSN -u REDIS_CONN_STRING SQLITE_PATH="$DB" SESSION_SECRET=smoke-session CRYPTO_SECRET=smoke-crypto \
  TOKEN_KEY_PEPPER="$SMOKE_SECRET" TOKEN_KEY_ENC_KEY="$SMOKE_SECRET" CHANNEL_KEY_ENC_KEY="$SMOKE_SECRET" \
  NODE_TYPE=master PORT=$PORT ERROR_LOG_ENABLED=true "$BIN" --log-dir "$WORK/logs" >"$WORK/app.log" 2>&1) & pids+=($!)
up=0
for _ in $(seq 1 60); do [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/status" || true)" = 200 ] && { up=1; break; }; sleep 1; done
[ "$up" = 1 ] || { echo "gateway never came up"; tail -30 "$WORK/app.log"; exit 1; }

models='gpt-4o-mini,gpt-4o,gpt-4,text-embedding-3-small,dall-e-3'
sqlite3 -cmd ".timeout 30000" "$DB" <<SQL
INSERT INTO users (id, username, password, display_name, role, status, quota, "group", aff_code) VALUES (1, 'smoke', 'x', 'smoke', 100, 1, 500000000, 'default', 'smk1');
INSERT INTO tokens (id, user_id, key_hash, key_enc, status, name, created_time, accessed_time, expired_time, remain_quota, unlimited_quota, "group") VALUES (1, 1, '$KEY_HASH', '', 1, 'smoke', 1, 1, -1, 500000000, 1, '');
INSERT INTO channels (id, type, key_enc, status, name, base_url, models, "group", priority, weight, auto_ban) VALUES (1, 1, '', 1, 'mock-openai', 'http://127.0.0.1:$MOCK_PORT', '$models', 'default', 0, 0, 0);
INSERT INTO channels (id, type, key_enc, status, name, base_url, models, "group", priority, weight, auto_ban) VALUES (2, 14, '', 1, 'mock-claude', 'http://127.0.0.1:$MOCK_PORT', 'claude-3-5-haiku-20241022', 'default', 0, 0, 0);
SQL
for m in ${models//,/ }; do sqlite3 -cmd ".timeout 30000" "$DB" "INSERT INTO abilities (\"group\", model, channel_id, enabled, priority, weight) VALUES ('default', '$m', 1, 1, 0, 0);"; done
sqlite3 -cmd ".timeout 30000" "$DB" "INSERT INTO abilities (\"group\", model, channel_id, enabled, priority, weight) VALUES ('default', 'claude-3-5-haiku-20241022', 2, 1, 0, 0);"

# ids, timestamps and request ids differ per run and are not behavior
norm() { sed -E 's/"(request_id|system_fingerprint)":"[^"]*"/"\1":"X"/g; s/"created(_at)?":[0-9]+/"created\1":0/g; s/request id: [A-Za-z0-9]+/request id: X/g; s/(chatcmpl|resp|msg)[-_][A-Za-z0-9]+/\1-X/g'; }
req() { # name method path [body] [auth]
  local name=$1 method=$2 path=$3 body=${4:-} auth=${5:-Bearer sk-$KEY}
  { curl -s -m 60 -o "$WORK/body" -w 'HTTP %{http_code}\n' -X "$method" "http://127.0.0.1:$PORT$path" \
      -H "Authorization: $auth" -H "x-api-key: ${auth#Bearer }" -H 'anthropic-version: 2023-06-01' -H 'content-type: application/json' ${body:+-d "$body"} || echo "HTTP curl-failed"
    norm < "$WORK/body"; echo; } > "$OUT/$name.txt"
}
msg='"messages":[{"role":"user","content":"say hello"}]'
req 01-chat            POST /v1/chat/completions "{\"model\":\"gpt-4o-mini\",$msg}"
req 02-chat-stream     POST /v1/chat/completions "{\"model\":\"gpt-4o-mini\",\"stream\":true,$msg}"
req 03-responses       POST /v1/responses        '{"model":"gpt-4o-mini","input":"say hello"}'
req 04-responses-stream POST /v1/responses       '{"model":"gpt-4o-mini","input":"say hello","stream":true}'
req 05-embeddings      POST /v1/embeddings       '{"model":"text-embedding-3-small","input":"hello"}'
req 06-image           POST /v1/images/generations '{"model":"dall-e-3","prompt":"a cat","n":1,"size":"1024x1024"}'
req 07-claude          POST /v1/messages         "{\"model\":\"claude-3-5-haiku-20241022\",\"max_tokens\":64,$msg}"
req 08-claude-stream   POST /v1/messages         "{\"model\":\"claude-3-5-haiku-20241022\",\"max_tokens\":64,\"stream\":true,$msg}"
req 09-chat-via-claude POST /v1/chat/completions "{\"model\":\"claude-3-5-haiku-20241022\",$msg}"
req 10-upstream-500    POST /v1/chat/completions "{\"model\":\"gpt-4o\",$msg}"
req 11-upstream-400    POST /v1/chat/completions "{\"model\":\"gpt-4\",$msg}"
req 12-unknown-model   POST /v1/chat/completions "{\"model\":\"no-such-model\",$msg}"
req 13-bad-key         POST /v1/chat/completions "{\"model\":\"gpt-4o-mini\",$msg}" 'Bearer sk-wrongwrongwrongwrongwrongwrongwrongwrongwrongwro'
req 14-models          GET  /v1/models
req 15-dashboard-noauth GET /api/user/self '' 'Bearer none'

sleep 3   # settlement and log writes are asynchronous
sqlite3 -cmd ".timeout 30000" -separator ' | ' "$DB" "SELECT type, model_name, prompt_tokens, completion_tokens, quota, is_stream, channel_id FROM logs ORDER BY id;" > "$OUT/90-billing-logs.txt"
sqlite3 -cmd ".timeout 30000" "$DB" "SELECT 'user quota ' || quota || ' used ' || used_quota || ' requests ' || request_count FROM users WHERE id=1; SELECT 'channel ' || id || ' status ' || status || ' used ' || used_quota FROM channels ORDER BY id;" > "$OUT/91-balances.txt"
grep -hE '^panic:|\[FATAL\]' "$WORK/app.log" > "$OUT/92-panics.txt" || true
echo "recorded $(ls "$OUT" | wc -l) files in $OUT"
