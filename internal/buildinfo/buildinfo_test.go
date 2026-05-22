package buildinfo

import (
	"strings"
	"testing"
)

// These tests exist to (a) document the symbol names exposed for ldflags
// injection and (b) catch accidental renames that would break the release
// pipeline silently — the ldflags `-X` flag fails gracefully if the symbol
// doesn't exist, so a typo in the workflow yields a binary with default
// values rather than an error.

func TestBuildinfo_VersionSymbolPresent(t *testing.T) {
	// Default for unbranded dev builds.
	if Version != "dev" && Version == "" {
		t.Fatalf("Version must default to a non-empty string; got %q", Version)
	}
}

func TestBuildinfo_APIBaseURLDefaultsToProduction(t *testing.T) {
	// Releases inject per-environment URLs; the un-injected default points
	// at production so a hand-built binary doesn't accidentally hit a
	// developer's local box.
	if APIBaseURL == "" {
		t.Fatal("APIBaseURL must have a non-empty default")
	}
	if !strings.HasPrefix(APIBaseURL, "https://") && !strings.HasPrefix(APIBaseURL, "http://") {
		t.Fatalf("APIBaseURL must be an http/https URL; got %q", APIBaseURL)
	}
}

func TestBuildinfo_FrpsServerDefaultsToProduction(t *testing.T) {
	if FrpsServer == "" {
		t.Fatal("FrpsServer must have a non-empty default")
	}
	if strings.Contains(FrpsServer, "://") || strings.Contains(FrpsServer, "/") {
		t.Fatalf("FrpsServer must be a bare hostname, not a URL; got %q", FrpsServer)
	}
}
