package main

import (
	"strings"
	"testing"
)

func TestMacReleaseSelectionRequiresArchitectureDigestAndStableTag(t *testing.T) {
	release := func(version, arch string) githubRelease {
		name := "ResourceLens-" + version + "-macos-" + arch + ".dmg"
		tag := "v" + version + "-macos"
		return githubRelease{Tag: tag, Assets: []githubReleaseAsset{{Name: name, URL: "https://github.com/" + officialRepository + "/releases/download/" + tag + "/" + name, Size: 2 << 20, Digest: "sha256:" + strings.Repeat("a", 64)}}}
	}
	good := release("1.8.0", "arm64")
	preview := release("9.0.0", "arm64")
	preview.Prerelease = true
	draft := release("8.0.0", "arm64")
	draft.Draft = true
	unsigned := release("7.0.0", "arm64")
	unsigned.Assets[0].Digest = ""
	hijacked := release("6.0.0", "arm64")
	hijacked.Assets[0].URL = "https://example.com/fake.dmg"
	windows := release("5.0.0", "arm64")
	windows.Tag = "v5.0.0"
	info, err := selectMacRelease([]githubRelease{preview, draft, unsigned, hijacked, windows, release("2.0.0", "amd64"), good}, "arm64", "1.7.0")
	if err != nil || info.Version != "1.8.0" || !info.Available || info.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("wrong update selected: %#v %v", info, err)
	}
	info, err = selectMacRelease([]githubRelease{good}, "arm64", "1.9.0")
	if err != nil || info.Available || info.Status != "latest" {
		t.Fatal("offered downgrade", info, err)
	}
}
