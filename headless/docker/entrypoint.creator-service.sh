#!/bin/sh
set -eu

: "${USER_ID:?USER_ID is required}"
if [ -n "${VAULT_KEY_FILE:-}" ]; then
    VAULT_KEY_BASE64="$(cat "$VAULT_KEY_FILE")"
fi
: "${VAULT_KEY_BASE64:?VAULT_KEY_BASE64 or VAULT_KEY_FILE is required}"

BINS_DIR="${BINS_DIR:-/opt/wlb/bin}"
SESSIONS_DIR="${SESSIONS_DIR:-/data/sessions}"
RESOURCES="${RESOURCES:-default}"
EGRESS_CONFIG="${EGRESS_CONFIG:-}"
SERVICE_COOKIES="${SERVICE_COOKIES:-/data/cookies-wbstream.json}"
VAULT_DIR="${VAULT_DIR:-/data/vault}"
WORK_PLATFORM="${WORK_PLATFORM:-telemost}"
SERVICE_WRITE_FILE="${SERVICE_WRITE_FILE:-/data/service-call.txt}"

mkdir -p "$VAULT_DIR" "$SESSIONS_DIR"
rm -f "$SERVICE_WRITE_FILE"

set -- \
    --user-id "$USER_ID" \
    --service-cookies "$SERVICE_COOKIES" \
    --vault-dir "$VAULT_DIR" \
    --vault-key-base64 "$VAULT_KEY_BASE64" \
    --bins-dir "$BINS_DIR" \
    --sessions-dir "$SESSIONS_DIR" \
    --resources "$RESOURCES" \
    --work-platform "$WORK_PLATFORM" \
    --write-file "$SERVICE_WRITE_FILE"

[ -n "$EGRESS_CONFIG" ] && set -- "$@" --egress-config "$EGRESS_CONFIG"
[ -n "${SERVICE_ROOM:-}" ] && set -- "$@" --service-room "$SERVICE_ROOM"

exec /usr/local/bin/headless-creator-service "$@"
