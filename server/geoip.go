package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GeoIPInfo describes the country associated with a server-observed source IP.
// Status is always populated so private, invalid, unavailable, and not-found
// cases are explicit rather than silently mislabelled.
type GeoIPInfo struct {
	CountryCode string `json:"country_code,omitempty"`
	CountryName string `json:"country_name,omitempty"`
	Status      string `json:"geoip_status"`
	Provider    string `json:"geoip_provider,omitempty"`
}

type geoCacheEntry struct {
	info GeoIPInfo
}

// GeoIPResolver uses MaxMind's local MMDB database through mmdblookup. The
// command receives arguments directly (never through a shell), and results are
// cached. The cache is cleared when the database file's modification time
// changes, so geoipupdate replacements are picked up without restarting.
type GeoIPResolver struct {
	dbPath    string
	lookupBin string
	timeout   time.Duration

	mu          sync.Mutex
	cache       map[string]geoCacheEntry
	lastDBMTime time.Time
}

func NewGeoIPResolver(dbPath, lookupBin string) *GeoIPResolver {
	if strings.TrimSpace(lookupBin) == "" {
		lookupBin = "mmdblookup"
	}
	return &GeoIPResolver{
		dbPath:    strings.TrimSpace(dbPath),
		lookupBin: strings.TrimSpace(lookupBin),
		timeout:   2 * time.Second,
		cache:     make(map[string]geoCacheEntry),
	}
}

func (g *GeoIPResolver) Lookup(rawIP string) GeoIPInfo {
	ip := net.ParseIP(strings.TrimSpace(rawIP))
	if ip == nil {
		return GeoIPInfo{Status: "invalid_ip"}
	}
	ipText := ip.String()

	switch {
	case ip.IsLoopback():
		return GeoIPInfo{Status: "loopback"}
	case ip.IsPrivate():
		return GeoIPInfo{Status: "private"}
	case ip.IsUnspecified():
		return GeoIPInfo{Status: "unspecified"}
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return GeoIPInfo{Status: "link_local"}
	case ip.IsMulticast():
		return GeoIPInfo{Status: "multicast"}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.dbPath == "" {
		return GeoIPInfo{Status: "disabled"}
	}
	stat, err := os.Stat(g.dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return GeoIPInfo{Status: "database_unavailable", Provider: "MaxMind GeoLite2"}
		}
		return GeoIPInfo{Status: "database_error", Provider: "MaxMind GeoLite2"}
	}
	if !stat.ModTime().Equal(g.lastDBMTime) {
		g.cache = make(map[string]geoCacheEntry)
		g.lastDBMTime = stat.ModTime()
	}
	if cached, ok := g.cache[ipText]; ok {
		return cached.info
	}

	info := g.lookupCountryLocked(ipText)
	g.cache[ipText] = geoCacheEntry{info: info}
	return info
}

func (g *GeoIPResolver) Status() map[string]any {
	status := map[string]any{
		"provider": "MaxMind GeoLite2 Country",
		"db_path":  g.dbPath,
	}
	if g.dbPath == "" {
		status["status"] = "disabled"
		return status
	}
	stat, err := os.Stat(g.dbPath)
	if err != nil {
		status["status"] = "database_unavailable"
		status["error"] = err.Error()
		return status
	}
	status["status"] = "ready"
	status["database_modified"] = stat.ModTime().UTC().Format(time.RFC3339)
	return status
}

func (g *GeoIPResolver) lookupCountryLocked(ip string) GeoIPInfo {
	provider := "MaxMind GeoLite2"
	for _, root := range []string{"country", "registered_country"} {
		code, err := g.lookupLeaf(ip, root, "iso_code")
		if err != nil || code == "" {
			continue
		}
		name, _ := g.lookupLeaf(ip, root, "names", "zh-CN")
		if name == "" {
			name, _ = g.lookupLeaf(ip, root, "names", "en")
		}
		if name == "" {
			name = code
		}
		return GeoIPInfo{
			CountryCode: strings.ToUpper(code),
			CountryName: name,
			Status:      "ok",
			Provider:    provider,
		}
	}
	return GeoIPInfo{Status: "not_found", Provider: provider}
}

var mmdbStringPattern = regexp.MustCompile(`(?s)^\s*"((?:\\.|[^"\\])*)"\s+<utf8_string>\s*$`)

func (g *GeoIPResolver) lookupLeaf(ip string, path ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	args := []string{"--file", g.dbPath, "--ip", ip}
	args = append(args, path...)
	cmd := exec.CommandContext(ctx, g.lookupBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("mmdblookup timeout: %w", ctx.Err())
		}
		return "", fmt.Errorf("mmdblookup: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	match := mmdbStringPattern.FindStringSubmatch(stdout.String())
	if len(match) != 2 {
		return "", fmt.Errorf("unexpected mmdblookup output: %q", strings.TrimSpace(stdout.String()))
	}
	// mmdblookup uses JSON-like escaping for string leaves. Quoting and
	// unquoting lets us correctly handle escaped Unicode and quotes.
	value := `"` + match[1] + `"`
	var decoded string
	if err := jsonUnmarshalString(value, &decoded); err != nil {
		return "", err
	}
	return decoded, nil
}

func jsonUnmarshalString(value string, out *string) error {
	// Kept as a helper to make the parsing path easy to unit-test and to avoid
	// accepting maps/arrays from mmdblookup's non-JSON aggregate output.
	return json.Unmarshal([]byte(value), out)
}
