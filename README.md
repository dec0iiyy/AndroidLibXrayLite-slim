# AndroidLibXrayLite (slim)

A smaller build of [AndroidLibXrayLite](https://github.com/2dust/AndroidLibXrayLite) (`libv2ray`, Xray-core for Android). Same Java API, no TUN / WireGuard / MASQUE / mKCP, and about 18% less APK size per ABI.

This is an **unofficial** build. It is not affiliated with 2dust or the XTLS project.

| | upstream `v26.9.30` | slim `26.9.30-slim` |
|---|---|---|
| `libgojni.so`, arm64-v8a | 36.0 MB | 25.6 MB |
| the same `.so` inside an APK (deflate) | 12.2 MB | 9.4 MB |
| the AAR (3 ABIs, see the note below) | 62.5 MB | 31.3 MB |

Note: the upstream AAR also bundles 27 MB of `geoip.dat` / `geosite.dat`. **The slim AAR does not**: ship your own geo files if your config uses them.

## What is inside

- Xray-core 26.9.30, Go 1.27.1, `gomobile bind -androidapi 24`.
- ABIs: `arm64-v8a`, `armeabi-v7a`, `x86_64` (no 32-bit `x86`). `minSdk` 24. The arm64 and x86_64 libraries are 16 KB page aligned.
- The Java API is identical to upstream (`libv2ray.Libv2ray`, `CoreController`, `CoreCallbackHandler`, `ProcessFinder`), so it is a drop-in replacement.

Kept: VLESS, VMess, Trojan, Shadowsocks (including 2022), Hysteria2, SOCKS, HTTP, freedom, blackhole, dns, dokodemo, loopback; transports tcp, WebSocket, gRPC, HTTPUpgrade, XHTTP, Hysteria; TLS (uTLS, ECH) and REALITY; routing, DNS (including DoH), stats, policy, observatory, reverse.

Removed: the TUN inbound, WireGuard, MASQUE, mKCP and xdrive implementations, metrics/pprof, the gRPC API (commander), loading a config over HTTP and the command line. Their config types stay, so a config that asks for them fails with `... is not registered` at start instead of failing to parse. Together they pull in gVisor, wireguard-go and netlink, which is where most of the size went. Use an external tun2socks (hev-socks5-tunnel, tun2socks) if you need a VPN service.

Build flags: `-trimpath -tags grpcnotrace -ldflags='-s -w -buildid= -checklinkname=0 -extldflags=-Wl,--pack-dyn-relocs=android'`. Packed relocations need Android 6.0 (API 23) or newer.

## Use it

Gradle (Kotlin DSL), straight from the GitHub release, no extra hosting:

```kotlin
// settings.gradle.kts
dependencyResolutionManagement {
    repositories {
        exclusiveContent {
            forRepository {
                ivy {
                    url = uri("https://github.com/dec0iiyy/AndroidLibXrayLite-slim/releases/download")
                    patternLayout { artifact("v[revision]/[module]-[revision].[ext]") }
                    metadataSources { artifact() }
                }
            }
            filter { includeModule("io.github.dec0iiyy", "libv2ray") }
        }
    }
}
```

```kotlin
// app/build.gradle.kts
dependencies {
    implementation("io.github.dec0iiyy:libv2ray:26.9.30-slim@aar")
}
```

Or download `libv2ray-26.9.30-slim.aar` from the [releases](https://github.com/dec0iiyy/AndroidLibXrayLite-slim/releases) page. `SHA256SUMS` is attached to every release.

Keep rules ship inside the AAR (`-keep class go.** { *; }`, `-keep class libv2ray.** { *; }`).

## Verify what you download

Every release has a `SHA256SUMS` file and its release notes contain the SHA-256 of the AAR. Releases built by the `build` workflow also carry a signed build provenance attestation (Sigstore), which ties the file to the workflow run and the commit that produced it:

```sh
gh attestation verify libv2ray-<version>-slim.aar --repo dec0iiyy/AndroidLibXrayLite-slim
shasum -a 256 -c SHA256SUMS
```

To pin the AAR in your own build, add it to `gradle/verification-metadata.xml` (`./gradlew --write-verification-metadata sha256` writes the entry for you; every release note also has a ready snippet):

```xml
<component group="io.github.dec0iiyy" name="libv2ray" version="26.9.30-slim">
   <artifact name="libv2ray-26.9.30-slim.aar">
      <sha256 value="04a1eb09f9cf518a44c0863116835fd27cc06b6e770efbb5a9c03fefb1d2bcd3" origin="GitHub release"/>
   </artifact>
</component>
```

`26.9.30-slim` itself was built on a developer machine with this script and the pinned toolchain before the pipeline existed, so it has a checksum but no attestation. Later releases are built by CI.

## Updating Xray

Nothing has to be rebuilt by hand.

- **Automatic.** The `update` workflow runs daily, looks at the latest AndroidLibXrayLite release and, if there is no matching `-slim` release here, starts the `build` workflow. It builds, runs the tests, attests and publishes the release with checksums, licenses and a size report, and opens an issue if something fails.
- **Canary.** The `canary` workflow runs every Monday and builds the latest wrapper against Xray-core `main` without publishing. If it fails, an issue appears before the next Xray release does, saying that `slim/` needs a change.
- **On demand.** Actions → `build` → Run workflow. `tag` is an AndroidLibXrayLite tag. `xray` is optional: an Xray-core tag, branch or commit (for example `v26.10.1` or `main`) to build into that wrapper before the wrapper itself is updated. Untick `publish` for a dry run that only uploads the files as a workflow artifact.
- **Locally.** `./build-aar.sh v26.9.30` or `./build-aar.sh v26.9.30 v26.10.1`.

The toolchain is pinned in `versions.env` (Go and NDK), so a new release does not change because a runner image did. If an upstream release needs a newer Go, the build stops with a message that says so; raise `GO_VERSION` in a commit and run it again.

Why this keeps working when Xray moves things around:

- The patch is data, not code: `slim/hollow.txt` lists the packages whose implementation is hidden, `slim/forbidden.txt` lists what must never be linked, `slim/distro_all.go.txt` replaces Xray's `main/distro/all`.
- `slim/main.go` applies it to a copy of the module and then compiles. Whenever the remaining code still needs a symbol from a hidden file, it restores that file by itself, so no keep-list is maintained by hand.
- The build stops with a clear message instead of publishing a bad AAR when: a forbidden package is linked again (the message names the importer to add to `hollow.txt`), `verify/` fails (it also checks that WireGuard really is gone), or the arm64 library grows over the size budget (`MAX_ARM64_BYTES`, 30 MB).
- Every release note lists the largest packages in the arm64 library and the change in size since the previous release (`tools/sizes`), so a new heavy dependency shows up in review instead of in your APK.

## Tested

`verify/main.go` starts a client and a server instance in one process and sends HTTP requests through SOCKS5: VLESS over ws, gRPC, XHTTP and HTTPUpgrade with TLS (uTLS fingerprint, pinned certificate), VMess, Trojan, Shadowsocks AEAD and 2022, SOCKS, HTTP and Hysteria2 with salamander. REALITY is only checked by parsing a config (it needs a real destination server). This is not a replacement for testing on a device.

## Build it yourself

You need git, Go and an Android NDK in the versions pinned in `versions.env` (other versions work with a warning).

```sh
ANDROID_NDK_HOME=$ANDROID_HOME/ndk/30.0.16248370 ./build-aar.sh v26.9.30
```

The script clones the upstream tag, installs the matching gomobile, builds the patched copy of Xray-core, runs `verify/`, builds the three ABIs and writes the AAR, a POM, `THIRD_PARTY_LICENSES.txt`, `RELEASE_NOTES.md` (with the size report) and `SHA256SUMS` to `dist/`. Go records the build directory in the binary, so for a release build point `LIBV2RAY_WORK` at a neutral directory.

## License

[LGPL-3.0](LICENSE), inherited from AndroidLibXrayLite. The AAR also contains Xray-core (MPL-2.0) and other Go modules; their licenses are in [THIRD_PARTY_LICENSES.txt](THIRD_PARTY_LICENSES.txt). The build scripts here are the way to rebuild the library from source.
