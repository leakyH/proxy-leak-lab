package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGeoIPSpecialAddresses(t *testing.T) {
	resolver := NewGeoIPResolver("", "")
	tests := map[string]string{
		"not-an-ip":   "invalid_ip",
		"127.0.0.1":   "loopback",
		"::1":         "loopback",
		"10.0.0.1":    "private",
		"fc00::1":     "private",
		"0.0.0.0":     "unspecified",
		"169.254.1.1": "link_local",
	}
	for ip, want := range tests {
		if got := resolver.Lookup(ip).Status; got != want {
			t.Fatalf("Lookup(%q).Status = %q, want %q", ip, got, want)
		}
	}
}

func TestGeoIPLookupAndCacheInvalidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "GeoLite2-Country.mmdb")
	if err := os.WriteFile(db, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(dir, "counter")
	bin := filepath.Join(dir, "mmdblookup")
	script := `#!/bin/sh
printf x >> "` + counter + `"
last=""
prev=""
for arg in "$@"; do prev="$last"; last="$arg"; done
if [ "$last" = "iso_code" ]; then
  printf '"US" <utf8_string>\n'
elif [ "$last" = "zh-CN" ]; then
  printf '"美国" <utf8_string>\n'
elif [ "$last" = "en" ]; then
  printf '"United States" <utf8_string>\n'
else
  exit 2
fi
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	resolver := NewGeoIPResolver(db, bin)
	got := resolver.Lookup("8.8.8.8")
	if got.Status != "ok" || got.CountryCode != "US" || got.CountryName != "美国" {
		t.Fatalf("unexpected lookup: %+v", got)
	}
	firstCount := fileSize(t, counter)
	_ = resolver.Lookup("8.8.8.8")
	if secondCount := fileSize(t, counter); secondCount != firstCount {
		t.Fatalf("cache miss: command count changed from %d to %d", firstCount, secondCount)
	}

	// Change mtime and verify the cached result is refreshed.
	if err := os.WriteFile(db, []byte("second-database-version"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = resolver.Lookup("8.8.8.8")
	if refreshed := fileSize(t, counter); refreshed <= firstCount {
		t.Fatalf("database change did not invalidate cache: %d <= %d", refreshed, firstCount)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return stat.Size()
}
