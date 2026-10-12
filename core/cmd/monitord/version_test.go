package main

import (
	"strings"
	"testing"
)

func TestApplyBuildIdentityDecoratesUntagged(t *testing.T) {
	origV, origC, origR := version, commit, releaseBuild
	t.Cleanup(func() { version, commit, releaseBuild = origV, origC, origR })

	version, commit, releaseBuild = "3.0.15", "abcdef0123456789", "0"
	applyBuildIdentity()
	if version != "3.0.15-dev+abcdef012345" {
		t.Fatalf("version %q", version)
	}

	version, commit, releaseBuild = "3.0.15", "abcdef0123456789", "1"
	applyBuildIdentity()
	if version != "3.0.15" {
		t.Fatalf("release decorated: %q", version)
	}

	version, commit, releaseBuild = "3.0.15-dev+already", "ffffffffffffffff", "0"
	applyBuildIdentity()
	if !strings.HasPrefix(version, "3.0.15-dev+already") || strings.Contains(version, "ffff") {
		t.Fatalf("double decorate: %q", version)
	}
}
