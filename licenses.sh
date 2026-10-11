#!/bin/sh
set -eu

SRC=${1:?libv2ray checkout}
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/versions.env"
GO=${GO:-go}
RULE='================================================================================'

cd "$SRC"
GOOS=android GOARCH=arm64 CGO_ENABLED=1 "$GO" list -tags "$GO_TAGS" -deps \
    -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./ |
    sort -u |
    while read -r path version dir; do
        [ -d "$dir" ] || continue
        printf '%s\n%s %s\n%s\n' "$RULE" "$path" "$version" "$RULE"
        found=0
        for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE* "$dir"/PATENTS*; do
            [ -f "$f" ] || continue
            printf '\n--- %s ---\n' "$(basename "$f")"
            cat "$f"
            found=1
        done
        [ "$found" = 1 ] || printf '\n(no license file in the module root)\n'
        printf '\n'
    done

printf '%s\nGo standard library\n%s\n\n' "$RULE" "$RULE"
cat "$("$GO" env GOROOT)/LICENSE"
