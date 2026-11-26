package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	xlog "github.com/xtls/xray-core/common/log"
	"google.golang.org/protobuf/encoding/protowire"
)

// silentChange matches the warnings Xray prints when it accepts a config but
// ignores or drops part of it. The config still starts, but behaves
// differently, so it counts as a failure. "deprecated" alone is only shown.
var silentChange = regexp.MustCompile(`(?i)\bis ignored\b|\bhas been removed\b|\bno longer (supported|used|works)\b`)

// recorder prints Xray's log messages and keeps them for the fixture check.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) Handle(msg xlog.Message) {
	line := msg.String()
	fmt.Println(line)
	r.mu.Lock()
	r.lines = append(r.lines, line)
	r.mu.Unlock()
}

func (r *recorder) matching(re *regexp.Regexp) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []string
	for _, line := range r.lines {
		if re.MatchString(line) {
			found = append(found, line)
		}
	}
	return found
}

//go:embed configs/*.json
var fixtures embed.FS

// Codes that the fixtures may use as geosite:code and geoip:code.
var (
	geoSites = map[string][]string{
		"CATEGORY-RU":      {"ru", "yandex.net"},
		"CATEGORY-ADS-ALL": {"ads.example.com"},
		"PRIVATE":          {"localhost", "local"},
		"GOOGLE":           {"google.com"},
	}
	geoIPs = map[string][]string{
		"RU":      {"5.255.255.0/24", "77.88.8.0/24"},
		"PRIVATE": {"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"},
	}
)

// writeGeoFiles writes geosite.dat and geoip.dat in the v2fly format to dir.
// The format is built with protowire, so it does not depend on where Xray-core
// keeps its generated types.
func writeGeoFiles(dir string) error {
	var sites []byte
	for _, code := range sortedKeys(geoSites) {
		entry := protowire.AppendTag(nil, 1, protowire.BytesType)
		entry = protowire.AppendString(entry, code)
		for _, domain := range geoSites[code] {
			var d []byte
			d = protowire.AppendTag(d, 1, protowire.VarintType)
			d = protowire.AppendVarint(d, 2) // Domain_Domain: the domain and its subdomains
			d = protowire.AppendTag(d, 2, protowire.BytesType)
			d = protowire.AppendString(d, domain)
			entry = protowire.AppendTag(entry, 2, protowire.BytesType)
			entry = protowire.AppendBytes(entry, d)
		}
		sites = protowire.AppendTag(sites, 1, protowire.BytesType)
		sites = protowire.AppendBytes(sites, entry)
	}
	var ips []byte
	for _, code := range sortedKeys(geoIPs) {
		entry := protowire.AppendTag(nil, 1, protowire.BytesType)
		entry = protowire.AppendString(entry, code)
		for _, cidr := range geoIPs[code] {
			prefix := netip.MustParsePrefix(cidr)
			var c []byte
			c = protowire.AppendTag(c, 1, protowire.BytesType)
			c = protowire.AppendBytes(c, prefix.Addr().AsSlice())
			c = protowire.AppendTag(c, 2, protowire.VarintType)
			c = protowire.AppendVarint(c, uint64(prefix.Bits()))
			entry = protowire.AppendTag(entry, 2, protowire.BytesType)
			entry = protowire.AppendBytes(entry, c)
		}
		ips = protowire.AppendTag(ips, 1, protowire.BytesType)
		ips = protowire.AppendBytes(ips, entry)
	}
	if err := os.WriteFile(filepath.Join(dir, "geosite.dat"), sites, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "geoip.dat"), ips, 0o644)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkFixtures loads every fixture, starts it and stops it again. A feature
// that the build left out fails here with "not registered".
func checkFixtures(stage string, report func(string, error)) error {
	assets, err := os.MkdirTemp("", "verify-geo")
	if err != nil {
		return err
	}
	defer os.RemoveAll(assets)
	if err := writeGeoFiles(assets); err != nil {
		return err
	}
	os.Setenv("xray.location.asset", assets)
	names, err := fixtures.ReadDir("configs")
	if err != nil {
		return err
	}
	for _, entry := range names {
		if path.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := fixtures.ReadFile("configs/" + entry.Name())
		if err != nil {
			return err
		}
		var config obj
		if err := json.Unmarshal(raw, &config); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if inbounds, ok := config["inbounds"].([]any); ok {
			for _, in := range inbounds {
				in.(obj)["port"] = freePort("tcp")
			}
		}
		logs := &recorder{}
		xlog.RegisterHandler(logs)
		instance, err := start(config)
		if err == nil {
			err = instance.Close()
		}
		if changed := logs.matching(silentChange); err == nil && len(changed) > 0 {
			err = fmt.Errorf("Xray accepts the config but ignores part of it:\n    %s", strings.Join(changed, "\n    "))
		}
		report(stage+":config/"+entry.Name(), err)
	}
	return nil
}
