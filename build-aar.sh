#!/bin/sh
set -eu

TAG=${1:?usage: build-aar.sh vMAJOR.MINOR.PATCH [xray-core-tag]}
XRAY_TAG=${2:-}
HERE=$(cd "$(dirname "$0")" && pwd)
: "${ANDROID_NDK_HOME:?ANDROID_NDK_HOME must point to an Android NDK}"
GO=${GO:-go}
WORK=${LIBV2RAY_WORK:-$(mktemp -d)}
SRC=${LIBV2RAY_SRC:-$WORK/AndroidLibXrayLite}
OUT=${LIBV2RAY_OUT:-$HERE/dist}
MAX_ARM64_BYTES=${MAX_ARM64_BYTES:-30000000}
VERSION=${XRAY_TAG:-$TAG}
VERSION=${VERSION#v}-slim
TARGETS=android/arm64,android/arm,android/amd64
LDFLAGS='-s -w -buildid= -checklinkname=0 -extldflags=-Wl,--pack-dyn-relocs=android'
export GOFLAGS=-modcacherw

sha256() {
    if command -v sha256sum > /dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

if [ -d "$SRC/.git" ]; then
    git -C "$SRC" reset -q --hard
    git -C "$SRC" clean -qfdx
else
    git clone --depth 1 --branch "$TAG" https://github.com/2dust/AndroidLibXrayLite "$SRC"
fi
cd "$SRC"
rm -rf "$SRC/assets"
printf 'libv2ray: %s\n' "$("$GO" version)"
"$GO" mod download

if [ -n "$XRAY_TAG" ]; then
    COMMIT=$(git ls-remote https://github.com/XTLS/Xray-core "refs/tags/$XRAY_TAG^{}" | cut -f1)
    [ -n "$COMMIT" ] || COMMIT=$(git ls-remote https://github.com/XTLS/Xray-core "refs/tags/$XRAY_TAG" | cut -f1)
    [ -n "$COMMIT" ] || { printf 'libv2ray: no Xray-core tag %s\n' "$XRAY_TAG" >&2; exit 1; }
    "$GO" get "github.com/xtls/xray-core@$COMMIT"
    "$GO" mod tidy
fi

GOMOBILE=$("$GO" list -m -f '{{.Version}}' golang.org/x/mobile)
GOBIN=$WORK/bin "$GO" install "golang.org/x/mobile/cmd/gomobile@$GOMOBILE" "golang.org/x/mobile/cmd/gobind@$GOMOBILE"
PATH=$WORK/bin:$PATH
gomobile init

"$GO" run "$HERE/slim/main.go" -src "$SRC" -config "$HERE/slim" -xray "$WORK/xray-core"

mkdir -p "$SRC/verify"
cp "$HERE/verify/main.go" "$SRC/verify/main.go"
GOFLAGS="$GOFLAGS -mod=mod" "$GO" run ./verify | tee "$WORK/verify.log"
XRAY_RELEASE=$(sed -n 's/^xray-core //p' "$WORK/verify.log")

gomobile bind -target="$TARGETS" -androidapi 24 -trimpath -tags grpcnotrace -ldflags="$LDFLAGS" -o "$WORK/libv2ray.aar" ./

for abi in arm64-v8a armeabi-v7a x86_64; do
    unzip -l "$WORK/libv2ray.aar" | grep -q "jni/$abi/libgojni.so" || { printf 'libv2ray: no %s in the AAR\n' "$abi" >&2; exit 1; }
done
ARM64_BYTES=$(unzip -p "$WORK/libv2ray.aar" jni/arm64-v8a/libgojni.so | wc -c | tr -d ' ')
if [ "$ARM64_BYTES" -gt "$MAX_ARM64_BYTES" ]; then
    printf 'libv2ray: libgojni.so for arm64 is %s bytes, the budget is %s. Something heavy is linked again.\n' "$ARM64_BYTES" "$MAX_ARM64_BYTES" >&2
    exit 1
fi

XRAY_MODULE=$("$GO" list -m -f '{{.Version}}' github.com/xtls/xray-core)
NDK_REVISION=$(sed -n 's/^Pkg.Revision *= *//p' "$ANDROID_NDK_HOME/source.properties")
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

cat > "$OUT/RELEASE_NOTES.md" << EOF2
Xray-core $XRAY_RELEASE ($XRAY_MODULE), wrapper \`$TAG\`, built with $("$GO" version | cut -d' ' -f3) and NDK $NDK_REVISION.

\`libgojni.so\` (arm64-v8a): $ARM64_BYTES bytes. ABIs: arm64-v8a, armeabi-v7a, x86_64. \`minSdk\` 24.

Gradle: \`implementation("io.github.dec0iiyy:libv2ray:$VERSION@aar")\`, see the README for the repository snippet. Verify the download with \`SHA256SUMS\`.
EOF2

printf 'libv2ray: %s\n' "$OUT/libv2ray-$VERSION.aar"
