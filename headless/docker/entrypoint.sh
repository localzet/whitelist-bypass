#!/bin/sh
set -eu

BINS_DIR="${BINS_DIR:-/opt/wlb/bin}"
SESSIONS_DIR="${SESSIONS_DIR:-/data/sessions}"
RESOURCES="${RESOURCES:-default}"
EGRESS_CONFIG="${EGRESS_CONFIG:-}"

if [ "${SERVICE_MODE:-}" = "creator-service" ]; then
    : "${USER_ID:?USER_ID is required}"
    if [ -n "${VAULT_KEY_FILE:-}" ]; then
        VAULT_KEY_BASE64="$(cat "$VAULT_KEY_FILE")"
    fi
    : "${VAULT_KEY_BASE64:?VAULT_KEY_BASE64 or VAULT_KEY_FILE is required}"
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
fi

: "${VK_TOKEN:?VK_TOKEN is required}"
: "${VK_GROUP_ID:?VK_GROUP_ID is required}"

VK_COOKIES_DEFAULT="/data/cookies-vk.json"
TM_COOKIES_DEFAULT="/data/cookies-yandex.json"
WB_COOKIES_DEFAULT="/data/cookies-wbstream.json"
DION_COOKIES_DEFAULT="/data/cookies-dion.json"
VK_COOKIES="${VK_COOKIES:-}"
TM_COOKIES="${TM_COOKIES:-}"
WB_COOKIES="${WB_COOKIES:-}"
DION_COOKIES="${DION_COOKIES:-}"
[ -z "$VK_COOKIES" ] && [ -f "$VK_COOKIES_DEFAULT" ] && VK_COOKIES="$VK_COOKIES_DEFAULT"
[ -z "$TM_COOKIES" ] && [ -f "$TM_COOKIES_DEFAULT" ] && TM_COOKIES="$TM_COOKIES_DEFAULT"
[ -z "$WB_COOKIES" ] && [ -f "$WB_COOKIES_DEFAULT" ] && WB_COOKIES="$WB_COOKIES_DEFAULT"
[ -z "$DION_COOKIES" ] && [ -f "$DION_COOKIES_DEFAULT" ] && DION_COOKIES="$DION_COOKIES_DEFAULT"

mkdir -p "$SESSIONS_DIR"

set -- \
    --token "$VK_TOKEN" \
    --group-id "$VK_GROUP_ID" \
    --bins-dir "$BINS_DIR" \
    --sessions-dir "$SESSIONS_DIR" \
    --resources "$RESOURCES"

[ -n "${VK_USER_IDS:-}" ] && set -- "$@" --user-id "$VK_USER_IDS"
[ -n "$VK_COOKIES" ] && set -- "$@" --vk-cookies "$VK_COOKIES"
[ -n "$TM_COOKIES" ] && set -- "$@" --tm-cookies "$TM_COOKIES"
[ -n "$WB_COOKIES" ] && set -- "$@" --wb-cookies "$WB_COOKIES"
[ -n "$DION_COOKIES" ] && set -- "$@" --dion-cookies "$DION_COOKIES"
[ -n "${UPSTREAM_SOCKS:-}" ] && set -- "$@" --upstream-socks "$UPSTREAM_SOCKS"
[ -n "${UPSTREAM_USER:-}" ] && set -- "$@" --upstream-user "$UPSTREAM_USER"
[ -n "${UPSTREAM_PASS:-}" ] && set -- "$@" --upstream-pass "$UPSTREAM_PASS"
[ -n "$EGRESS_CONFIG" ] && set -- "$@" --egress-config "$EGRESS_CONFIG"

exec /usr/local/bin/headless-vk-bot "$@"
