#!/bin/sh
set -eu

USER_IDS="${USER_IDS:-${USER_ID:-}}"
[ -n "$USER_IDS" ] || { echo "USER_IDS is required" >&2; exit 1; }
if [ -n "${VAULT_KEY_FILE:-}" ]; then
    VAULT_KEY_BASE64="$(cat "$VAULT_KEY_FILE")"
fi
: "${VAULT_KEY_BASE64:?VAULT_KEY_BASE64 or VAULT_KEY_FILE is required}"

BINS_DIR="${BINS_DIR:-/opt/wlb/bin}"
SESSIONS_DIR="${SESSIONS_DIR:-/data/sessions}"
RESOURCES="${RESOURCES:-default}"
EGRESS_CONFIG="${EGRESS_CONFIG:-}"
SERVICE_COOKIES="${SERVICE_COOKIES:-/data/cookies-yandex.json}"
VAULT_DIR="${VAULT_DIR:-/data/vault}"
WORK_PLATFORM="${WORK_PLATFORM:-telemost}"
MAX_ACTIVE_USERS="${MAX_ACTIVE_USERS:-2}"
WORK_TTL="${WORK_TTL:-30m}"
SERVICE_WRITE_FILE="${SERVICE_WRITE_FILE:-/data/service-call.txt}"

mkdir -p "$VAULT_DIR" "$SESSIONS_DIR"
rm -f "$SERVICE_WRITE_FILE"

set -- \
    --service-control \
    --service-user-ids "$USER_IDS" \
    --cookies "$SERVICE_COOKIES" \
    --vault-dir "$VAULT_DIR" \
    --vault-key-base64 "$VAULT_KEY_BASE64" \
    --bins-dir "$BINS_DIR" \
    --sessions-dir "$SESSIONS_DIR" \
    --resources "$RESOURCES" \
    --work-platform "$WORK_PLATFORM" \
    --max-active-users "$MAX_ACTIVE_USERS" \
    --work-ttl "$WORK_TTL" \
    --write-file "$SERVICE_WRITE_FILE"

[ -n "$EGRESS_CONFIG" ] && set -- "$@" --egress-config "$EGRESS_CONFIG"
[ -n "${SERVICE_ROOM:-}" ] && set -- "$@" --tm-link "$SERVICE_ROOM"

exec /opt/wlb/bin/headless-telemost-creator "$@"
