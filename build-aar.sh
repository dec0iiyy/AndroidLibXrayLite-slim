#!/bin/sh
set -eu

TAG=${1:?usage: build-aar.sh vMAJOR.MINOR.PATCH [xray-core tag, branch or commit]}
XRAY_REF=${2:-}
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/versions.env"
: "${ANDROID_NDK_HOME:?ANDROID_NDK_HOME must point to an Android NDK}"
GO=${GO:-go}
WORK=${LIBV2RAY_WORK:-$(mktemp -d)}
SRC=${LIBV2RAY_SRC:-$WORK/AndroidLibXrayLite}
OUT=${LIBV2RAY_OUT:-$HERE/dist}
MAX_ARM64_BYTES=${MAX_ARM64_BYTES:-30000000}
VERSION=${XRAY_REF:-$TAG}
VERSION=${VERSION#v}${LIBV2RAY_REVISION:+-r$LIBV2RAY_REVISION}-slim
TARGETS=android/arm64,android/arm,android/amd64
LDFLAGS='-s -w -buildid= -checklinkname=0 -extldflags=-Wl,--pack-dyn-relocs=android'
export GOFLAGS=-modcacherw

sha256() {
    if command -v sha256sum > /dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

resolve_xray() {
    for pattern in "refs/tags/$1^{}" "refs/tags/$1" "refs/heads/$1"; do
        commit=$(git ls-remote https://github.com/XTLS/Xray-core "$pattern" | cut -f1)
        if [ -n "$commit" ]; then
            printf '%s' "$commit"
            return 0
        fi
    done
    case "$1" in
        '' | *[!0-9a-f]*) return 1 ;;
    esac
    printf '%s' "$1"
}

if [ -d "$SRC/.git" ]; then
    git -C "$SRC" reset -q --hard
    git -C "$SRC" clean -qfdx
else
    git clone --depth 1 --branch "$TAG" https://github.com/2dust/AndroidLibXrayLite "$SRC"
fi
cd "$SRC"
rm -rf "$SRC/assets"

GO_VERSION_FOUND=$("$GO" version | cut -d' ' -f3)
NDK_REVISION=$(sed -n 's/^Pkg.Revision *= *//p' "$ANDROID_NDK_HOME/source.properties")
printf 'libv2ray: %s, NDK %s\n' "$GO_VERSION_FOUND" "$NDK_REVISION"
[ "$GO_VERSION_FOUND" = "go$GO_VERSION" ] || printf 'libv2ray: warning: Go %s is pinned in versions.env, found %s\n' "$GO_VERSION" "$GO_VERSION_FOUND" >&2
[ "$NDK_REVISION" = "$NDK_VERSION" ] || printf 'libv2ray: warning: NDK %s is pinned in versions.env, found %s\n' "$NDK_VERSION" "$NDK_REVISION" >&2
"$GO" mod download

if [ -n "$XRAY_REF" ]; then
    COMMIT=$(resolve_xray "$XRAY_REF") || { printf 'libv2ray: cannot resolve Xray-core %s\n' "$XRAY_REF" >&2; exit 1; }
    "$GO" get "github.com/xtls/xray-core@$COMMIT"
    "$GO" mod tidy
fi

GOMOBILE=$("$GO" list -m -f '{{.Version}}' golang.org/x/mobile)
GOBIN=$WORK/bin "$GO" install "golang.org/x/mobile/cmd/gomobile@$GOMOBILE" "golang.org/x/mobile/cmd/gobind@$GOMOBILE"
PATH=$WORK/bin:$PATH
gomobile init

"$GO" run "$HERE/slim/main.go" -src "$SRC" -config "$HERE/slim" -xray "$WORK/xray-core" -tags "$GO_TAGS"

mkdir -p "$SRC/verify"
cp "$HERE/verify/main.go" "$SRC/verify/main.go"
GOFLAGS="$GOFLAGS -mod=mod" "$GO" run ./verify | tee "$WORK/verify.log"
XRAY_RELEASE=$(sed -n 's/^xray-core //p' "$WORK/verify.log")

gomobile bind -target="$TARGETS" -androidapi 24 -trimpath -tags "$GO_TAGS" -ldflags="$LDFLAGS" -o "$WORK/libv2ray.aar" ./

for abi in arm64-v8a armeabi-v7a x86_64; do
    unzip -l "$WORK/libv2ray.aar" | grep -q "jni/$abi/libgojni.so" || { printf 'libv2ray: no %s in the AAR\n' "$abi" >&2; exit 1; }
done
unzip -p "$WORK/libv2ray.aar" jni/arm64-v8a/libgojni.so > "$WORK/libgojni-arm64.so"
ARM64_BYTES=$(wc -c < "$WORK/libgojni-arm64.so" | tr -d ' ')
if [ "$ARM64_BYTES" -gt "$MAX_ARM64_BYTES" ]; then
    printf 'libv2ray: libgojni.so for arm64 is %s bytes, the budget is %s. Something heavy is linked again.\n' "$ARM64_BYTES" "$MAX_ARM64_BYTES" >&2
    exit 1
fi

XRAY_MODULE=$("$GO" list -m -f '{{.Version}}' github.com/xtls/xray-core)
mkdir -p "$OUT"
cp "$WORK/libv2ray.aar" "$OUT/libv2ray-$VERSION.aar"
cat > "$OUT/libv2ray-$VERSION.pom" << EOF2
<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>io.github.dec0iiyy</groupId>
  <artifactId>libv2ray</artifactId>
  <version>$VERSION</version>
  <packaging>aar</packaging>
  <name>libv2ray (slim)</name>
  <description>Slim build of AndroidLibXrayLite: Xray-core $XRAY_RELEASE for Android without TUN, WireGuard, MASQUE and mKCP</description>
  <url>https://github.com/dec0iiyy/AndroidLibXrayLite-slim</url>
  <licenses>
    <license>
      <name>GNU Lesser General Public License, version 3</name>
      <url>https://www.gnu.org/licenses/lgpl-3.0.txt</url>
    </license>
  </licenses>
  <scm>
    <url>https://github.com/dec0iiyy/AndroidLibXrayLite-slim</url>
  </scm>
</project>
EOF2
sh "$HERE/licenses.sh" "$SRC" > "$OUT/THIRD_PARTY_LICENSES.txt"
(cd "$OUT" && sha256 "libv2ray-$VERSION.aar" "libv2ray-$VERSION.pom" > SHA256SUMS)
AAR_SHA=$(cut -d' ' -f1 < "$OUT/SHA256SUMS" | head -n 1)
SIZES=$("$GO" run "$HERE/tools/sizes/main.go" -top 12 "$WORK/libgojni-arm64.so")

{
    printf 'Xray-core %s (%s), wrapper `%s`, built with %s and NDK %s.\n\n' "$XRAY_RELEASE" "$XRAY_MODULE" "$TAG" "$GO_VERSION_FOUND" "$NDK_REVISION"
    printf '`libgojni.so` (arm64-v8a): %s bytes. ABIs: arm64-v8a, armeabi-v7a, x86_64. `minSdk` 24.\n\n' "$ARM64_BYTES"
    printf 'Go build tags: `%s`.\n\n' "$GO_TAGS"
    printf 'Gradle: `implementation("io.github.dec0iiyy:libv2ray:%s@aar")`, see the README for the repository snippet.\n\n' "$VERSION"
    printf '### Verify the download\n\n'
    printf 'SHA-256 of the AAR:\n\n```\n%s  libv2ray-%s.aar\n```\n\n' "$AAR_SHA" "$VERSION"
    printf 'Build provenance: `gh attestation verify libv2ray-%s.aar --repo dec0iiyy/AndroidLibXrayLite-slim`\n\n' "$VERSION"
    printf 'Gradle dependency verification (`gradle/verification-metadata.xml`):\n\n'
    printf '```xml\n<component group="io.github.dec0iiyy" name="libv2ray" version="%s">\n' "$VERSION"
    printf '   <artifact name="libv2ray-%s.aar">\n      <sha256 value="%s" origin="GitHub release"/>\n   </artifact>\n</component>\n```\n\n' "$VERSION" "$AAR_SHA"
    printf '### Largest packages in the arm64-v8a library\n\n%s\n' "$SIZES"
} > "$OUT/RELEASE_NOTES.md"

printf 'libv2ray: %s\n' "$OUT/libv2ray-$VERSION.aar"
