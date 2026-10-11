// Command readme rewrites the parts of README.md that belong to one release:
// the size table, the Xray-core and Go versions, and the version and SHA-256
// in the examples. It reads the AAR that was just built and the AAR that
// upstream published for the same wrapper tag, and it edits only the text
// between <!-- release:NAME --> and <!-- /release:NAME --> markers. It also
// draws the size chart, in a light and a dark variant, into the assets
// directory.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var abis = []string{"arm64-v8a", "armeabi-v7a", "x86_64"}

type entry struct {
	raw, packed uint64
}

type archive struct {
	size    int64
	entries map[string]entry
}

type release struct {
	wrapper, version, xray, goVersion, ndk, sha string
	slim, upstream                              archive
}

func main() {
	readme := flag.String("readme", "README.md", "README to update")
	assets := flag.String("assets", "docs", "directory for the size charts")
	dist := flag.String("dist", "dist", "directory with libv2ray-VERSION-slim.aar and SHA256SUMS")
	upstream := flag.String("upstream", "", "AAR that upstream published for the same tag")
	notes := flag.String("notes", "dist/RELEASE_NOTES.md", "release notes written by build-aar.sh")
	versions := flag.String("versions", "versions.env", "pinned toolchain versions")
	flag.Parse()
	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "usage: readme -upstream libv2ray.aar [-readme README.md] [-assets docs] [-dist dist] [-notes dist/RELEASE_NOTES.md] [-versions versions.env]")
		os.Exit(2)
	}
	if err := run(*readme, *assets, *dist, *upstream, *notes, *versions); err != nil {
		fmt.Fprintln(os.Stderr, "readme:", err)
		os.Exit(1)
	}
}

func run(readmePath, assetsDir, dist, upstreamPath, notesPath, versionsPath string) error {
	r, err := load(dist, upstreamPath, notesPath, versionsPath)
	if err != nil {
		return err
	}
	text, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}
	updated, err := rewrite(string(text), r)
	if err != nil {
		return err
	}
	if err := writeCharts(assetsDir, r); err != nil {
		return err
	}
	if updated == string(text) {
		fmt.Printf("%s is up to date for %s\n", readmePath, r.version)
		return nil
	}
	fmt.Printf("%s updated for %s\n", readmePath, r.version)
	return os.WriteFile(readmePath, []byte(updated), 0o644)
}

func load(dist, upstreamPath, notesPath, versionsPath string) (release, error) {
	var r release
	aars, err := filepath.Glob(filepath.Join(dist, "libv2ray-*-slim.aar"))
	if err != nil || len(aars) != 1 {
		return r, fmt.Errorf("want exactly one libv2ray-*-slim.aar in %s, found %d", dist, len(aars))
	}
	name := filepath.Base(aars[0])
	r.version = strings.TrimSuffix(strings.TrimPrefix(name, "libv2ray-"), ".aar")
	if r.slim, err = openArchive(aars[0]); err != nil {
		return r, err
	}
	if r.upstream, err = openArchive(upstreamPath); err != nil {
		return r, err
	}
	if r.sha, err = checksum(aars[0], filepath.Join(dist, "SHA256SUMS"), name); err != nil {
		return r, err
	}
	notes, err := os.ReadFile(notesPath)
	if err != nil {
		return r, err
	}
	head := regexp.MustCompile("^Xray-core (\\S+) \\(.*\\), wrapper `(v[^`]+)`, built with go(\\S+) and NDK (\\S+)\\.").FindStringSubmatch(string(notes))
	if head == nil {
		return r, fmt.Errorf("the first line of %s has an unexpected format", notesPath)
	}
	r.xray, r.wrapper, r.goVersion = head[1], head[2], head[3]
	pinned, err := readEnv(versionsPath)
	if err != nil {
		return r, err
	}
	if r.ndk = pinned["NDK_VERSION"]; r.ndk == "" {
		return r, fmt.Errorf("NDK_VERSION is missing in %s", versionsPath)
	}
	return r, nil
}

func openArchive(path string) (archive, error) {
	info, err := os.Stat(path)
	if err != nil {
		return archive{}, err
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return archive{}, fmt.Errorf("%s: %w", path, err)
	}
	defer z.Close()
	a := archive{size: info.Size(), entries: map[string]entry{}}
	for _, f := range z.File {
		a.entries[f.Name] = entry{raw: f.UncompressedSize64, packed: f.CompressedSize64}
	}
	return a, nil
}

func checksum(path, sums, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	listed, err := os.ReadFile(sums)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(listed), sha+"  "+name) {
		return "", fmt.Errorf("%s does not list %s with SHA-256 %s", sums, name, sha)
	}
	return sha, nil
}

func readEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	return values, nil
}

func rewrite(text string, r release) (string, error) {
	sizes, err := sizesBlock(r)
	if err != nil {
		return "", err
	}
	percent, err := percentLess(r)
	if err != nil {
		return "", err
	}
	blocks := map[string]string{
		"percent": percent,
		"sizes":   sizes,
		"xray":    r.xray,
		"go":      r.goVersion,
		"gradle":  fmt.Sprintf("```kotlin\n// app/build.gradle.kts\ndependencies {\n    implementation(\"io.github.dec0iiyy:libv2ray:%s@aar\")\n}\n```", r.version),
		"file":    fmt.Sprintf("`libv2ray-%s.aar`", r.version),
		"verification": fmt.Sprintf("```xml\n<component group=\"io.github.dec0iiyy\" name=\"libv2ray\" version=\"%[1]s\">\n"+
			"   <artifact name=\"libv2ray-%[1]s.aar\">\n      <sha256 value=\"%[2]s\" origin=\"GitHub release\"/>\n   </artifact>\n</component>\n```", r.version, r.sha),
		"local": fmt.Sprintf("`./build-aar.sh %[1]s` or `./build-aar.sh %[1]s main`", r.wrapper),
		"build": fmt.Sprintf("```sh\nANDROID_NDK_HOME=$ANDROID_HOME/ndk/%s ./build-aar.sh %s\n```", r.ndk, r.wrapper),
	}
	for name, body := range blocks {
		pattern := regexp.MustCompile("(?s)(<!-- release:" + name + " -->)(.*?)(<!-- /release:" + name + " -->)")
		match := pattern.FindStringSubmatch(text)
		if match == nil {
			return "", fmt.Errorf("README has no <!-- release:%s --> block", name)
		}
		inner := body
		if strings.HasPrefix(match[2], "\n") {
			inner = "\n" + body + "\n"
		}
		text = strings.Replace(text, match[0], match[1]+inner+match[3], 1)
	}
	return text, nil
}

func mb(n uint64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

func library(a archive, abi string) (entry, error) {
	e, ok := a.entries["jni/"+abi+"/libgojni.so"]
	if !ok {
		return entry{}, fmt.Errorf("no jni/%s/libgojni.so in an AAR", abi)
	}
	return e, nil
}

func percentLess(r release) (string, error) {
	lowest, highest := math.MaxFloat64, 0.0
	for _, abi := range abis {
		up, err := library(r.upstream, abi)
		if err != nil {
			return "", err
		}
		slim, err := library(r.slim, abi)
		if err != nil {
			return "", err
		}
		less := 100 * (1 - float64(slim.packed)/float64(up.packed))
		lowest, highest = math.Min(lowest, less), math.Max(highest, less)
	}
	low, high := int(math.Round(lowest)), int(math.Round(highest))
	if low == high {
		return fmt.Sprintf("%d%%", low), nil
	}
	return fmt.Sprintf("%d–%d%%", low, high), nil
}

func sizesBlock(r release) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "`libgojni.so` per ABI, upstream `%s` against `%s`. \"Compressed\" is the deflated size of the file inside the AAR, which is roughly what an APK download carries.\n\n",
		r.wrapper, r.version)
	b.WriteString("| ABI | upstream | slim | upstream, compressed | slim, compressed |\n|---|---|---|---|---|\n")
	for _, abi := range abis {
		up, err := library(r.upstream, abi)
		if err != nil {
			return "", err
		}
		slim, err := library(r.slim, abi)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", abi, mb(up.raw), mb(slim.raw), mb(up.packed), mb(slim.packed))
	}
	paragraph, err := aarParagraph(r)
	if err != nil {
		return "", err
	}
	b.WriteString("\n" + paragraph)
	return b.String(), nil
}

func aarParagraph(r release) (string, error) {
	var parts []string
	var geo entry
	for name, e := range r.upstream.entries {
		if _, kept := r.slim.entries[name]; kept || strings.HasSuffix(name, "/") {
			continue
		}
		switch {
		case name == "jni/x86/libgojni.so":
			parts = append(parts, fmt.Sprintf("an x86 library (%s compressed)", mb(e.packed)))
		case strings.HasPrefix(name, "assets/") && strings.HasSuffix(name, ".dat"):
			geo.raw += e.raw
			geo.packed += e.packed
		default:
			return "", fmt.Errorf("the upstream AAR has %s, which the README does not know how to describe", name)
		}
	}
	if geo.raw > 0 {
		parts = append(parts, fmt.Sprintf("`geoip.dat` / `geosite.dat` (%s, %s compressed)", mb(geo.raw), mb(geo.packed)))
	}
	line := fmt.Sprintf("The AAR files are %s (upstream) and %s (slim)", mb(uint64(r.upstream.size)), mb(uint64(r.slim.size)))
	switch len(parts) {
	case 0:
		return line + ".", nil
	case 1:
		line += ", but that is not a like-for-like comparison. The upstream AAR also contains " + parts[0] + ". **The slim AAR does not**"
	default:
		line += ", but that is not a like-for-like comparison. The upstream AAR also contains " + strings.Join(parts, " and ") + ". **The slim AAR contains neither**"
	}
	if geo.raw > 0 {
		return line + ": ship your own geo files if your config uses them.", nil
	}
	return line + ".", nil
}

type theme struct {
	name, text, muted, upstream, slim string
}

var themes = []theme{
	{name: "light", text: "#1f2328", muted: "#656d76", upstream: "#afb8c1", slim: "#1a7f37"},
	{name: "dark", text: "#e6edf3", muted: "#8d96a0", upstream: "#484f58", slim: "#3fb950"},
}

func writeCharts(dir string, r release) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, t := range themes {
		svg, err := chart(r, t)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "sizes-"+t.name+".svg"), []byte(svg), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func chart(r release, t theme) (string, error) {
	const (
		width    = 760
		left     = 24
		barsFrom = 132
		barsMax  = 470
		firstRow = 92
		rowStep  = 60
		barThick = 18
	)
	var largest uint64
	for _, abi := range abis {
		up, err := library(r.upstream, abi)
		if err != nil {
			return "", err
		}
		largest = max(largest, up.packed)
	}
	height := firstRow + len(abis)*rowStep + 36
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc" font-family="-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif">`+"\n", width, height, width, height)
	fmt.Fprintf(&b, "<title id=\"title\">Native library per ABI, compressed</title>\n")
	fmt.Fprintf(&b, "<desc id=\"desc\">Compressed libgojni.so per ABI, upstream %s against slim %s. Smaller is better.</desc>\n", r.wrapper, r.version)
	fmt.Fprintf(&b, `<text x="%d" y="36" font-size="18" font-weight="600" fill="%s">Native library per ABI, compressed</text>`+"\n", left, t.text)
	fmt.Fprintf(&b, `<text x="%d" y="58" font-size="13" fill="%s">upstream %s against slim %s, smaller is better</text>`+"\n", left, t.muted, r.wrapper, r.version)
	for i, abi := range abis {
		up, err := library(r.upstream, abi)
		if err != nil {
			return "", err
		}
		slim, err := library(r.slim, abi)
		if err != nil {
			return "", err
		}
		y := firstRow + i*rowStep
		upWidth := float64(barsMax) * float64(up.packed) / float64(largest)
		slimWidth := float64(barsMax) * float64(slim.packed) / float64(largest)
		less := 100 * (1 - float64(slim.packed)/float64(up.packed))
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="14" font-weight="600" fill="%s">%s</text>`+"\n", left, y+barThick+4, t.text, abi)
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%.1f" height="%d" rx="4" fill="%s"/>`+"\n", barsFrom, y, upWidth, barThick, t.upstream)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="13" fill="%s">%s</text>`+"\n", float64(barsFrom)+upWidth+8, y+barThick-5, t.muted, mb(up.packed))
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%.1f" height="%d" rx="4" fill="%s"/>`+"\n", barsFrom, y+barThick+6, slimWidth, barThick, t.slim)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="13" font-weight="600" fill="%s">%s  <tspan fill="%s">−%.0f%%</tspan></text>`+"\n", float64(barsFrom)+slimWidth+8, y+2*barThick+1, t.text, mb(slim.packed), t.slim, less)
	}
	legend := firstRow + len(abis)*rowStep + 4
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="12" height="12" rx="3" fill="%s"/><text x="%d" y="%d" font-size="13" fill="%s">upstream</text>`+"\n", left, legend, t.upstream, left+18, legend+11, t.muted)
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="12" height="12" rx="3" fill="%s"/><text x="%d" y="%d" font-size="13" fill="%s">slim</text>`+"\n", left+100, legend, t.slim, left+118, legend+11, t.muted)
	b.WriteString("</svg>\n")
	return b.String(), nil
}
