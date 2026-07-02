#!/bin/sh
set -eu

tag=${RELEASE_TAG:-}
case "$tag" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "Release tag must use vMAJOR.MINOR.PATCH format, got: $tag" >&2; exit 1 ;;
esac

version=${tag#v}
old_ifs=$IFS
IFS=.
set -- $version
IFS=$old_ifs
[ "$#" -eq 3 ] || { echo "Release tag must contain exactly three numeric components" >&2; exit 1; }
for component in "$@"; do
    case "$component" in
        ''|*[!0-9]*) echo "Release version components must be numeric" >&2; exit 1 ;;
    esac
    if [ "${#component}" -gt 1 ] && [ "${component#0}" != "$component" ]; then
        echo "Release version components must not contain leading zeroes" >&2
        exit 1
    fi
done
[ "$1" -le 210 ] && [ "$2" -le 99 ] && [ "$3" -le 99 ] || {
    echo "Release version exceeds Android versionCode limits: major <= 210, minor/patch <= 99" >&2
    exit 1
}

build_number=${GITHUB_RUN_NUMBER:-0}
case "$build_number" in
    ''|*[!0-9]*) echo "GITHUB_RUN_NUMBER must be numeric" >&2; exit 1 ;;
esac

{
    echo "RELEASE_VERSION=$version"
    echo "VERSION_MAJOR=$1"
    echo "VERSION_MINOR=$2"
    echo "VERSION_PATCH=$3"
    echo "BUILD_NUMBER=$build_number"
} >> "${GITHUB_ENV:?GITHUB_ENV is required}"
[ -z "${GITHUB_OUTPUT:-}" ] || echo "version=$version" >> "$GITHUB_OUTPUT"

echo "Building release $version (build $build_number)"
