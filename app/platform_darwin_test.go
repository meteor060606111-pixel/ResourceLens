package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMacProxyParsingAndRestoreKeepsUserChanges(t *testing.T) {
	p, err := parseProxyEndpoint("Enabled: Yes\nServer: 127.0.0.1\nPort: 18999\nAuthenticated Proxy Enabled: 0\n")
	if err != nil || !p.Enabled || p.Port != 18999 || p.Host != "127.0.0.1" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parseProxyEndpoint("unexpected response"); err == nil {
		t.Fatal("unrecognized response accepted")
	}
	before := NetworkProxy{Name: "Wi-Fi", HTTP: ProxyEndpoint{Enabled: true, Host: "localhost", Port: 7890}, AutoEnabled: true, AutoURL: "https://example.com/proxy.pac", Discovery: true}
	ethernet := before
	ethernet.Name = "USB Ethernet"
	old := ProxySnapshot{Services: []NetworkProxy{before, ethernet}, OwnedAddress: "127.0.0.1:18999"}
	next, err := captureProxySnapshot(old, old.OwnedAddress)
	if err != nil || len(next.Services) != 2 || next.Services[0].AutoEnabled || next.Services[0].Discovery {
		t.Fatal(next, err)
	}
	changed := next
	changed.Services = append([]NetworkProxy(nil), next.Services...)
	changed.Services[1].HTTP.Host = "user-proxy.example.com"
	restore, owned := proxyRestoreSnapshot(changed, old)
	if !owned || len(restore.Services) != 2 || restore.Services[0] != before || restore.Services[1].HTTP.Host != "user-proxy.example.com" || restore.Services[1].HTTPS != ethernet.HTTPS {
		t.Fatal("user override or PAC/discovery restoration failed", restore)
	}
	partial := ProxySnapshot{Services: []NetworkProxy{before}}
	partial.Services[0].HTTP = next.Services[0].HTTP
	recovered, owned := proxyRestoreSnapshot(partial, old)
	if !owned || len(recovered.Services) != 1 || recovered.Services[0] != before {
		t.Fatal("partially applied capture was not recovered", recovered)
	}
	old.Services[0].HTTP.Authenticated = true
	if _, err := captureProxySnapshot(old, old.OwnedAddress); err == nil {
		t.Fatal("authenticated proxy would be overwritten")
	}
}

func TestMacProxyCommandsQuoteNetworkNames(t *testing.T) {
	s := ProxySnapshot{Services: []NetworkProxy{{Name: "USB ' Ethernet; $(touch /tmp/never-created)", HTTP: ProxyEndpoint{Enabled: true, Host: "127.0.0.1", Port: 18999}, HTTPS: ProxyEndpoint{Enabled: true, Host: "127.0.0.1", Port: 18999}}}}
	commands, err := proxyCommands(s)
	if err != nil || len(commands) == 0 {
		t.Fatal(err)
	}
	if !strings.Contains(commands[0], shellQuote(s.Services[0].Name)) {
		t.Fatal("network name was not quoted", commands[0])
	}
	if appleScriptString("a\"b\\c\nd") != "\"a\\\"b\\\\c\\nd\"" {
		t.Fatal("AppleScript string escaping changed")
	}
}

func TestMacDownloadCommitNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "part"), filepath.Join(dir, "video.mp4")
	os.WriteFile(source, []byte("new"), 0600)
	os.WriteFile(destination, []byte("original"), 0600)
	if err := commitDownload(source, destination); err == nil {
		t.Fatal("existing download was overwritten")
	}
	data, _ := os.ReadFile(destination)
	if string(data) != "original" {
		t.Fatal("existing file changed")
	}
	os.Remove(destination)
	if err := commitDownload(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("staging file not removed")
	}
}

func TestMacSingleInstanceActivationAndRestart(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rl-instance-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("RESOURCELENS_INSTANCE_DIR", dir)
	first, existing, err := acquireNamedInstance("isolated-test")
	if err != nil || existing {
		t.Fatal(existing, err)
	}
	defer first.Close()
	activated := make(chan struct{}, 1)
	first.listen(func() { activated <- struct{}{} })
	second, existing, err := acquireNamedInstance("isolated-test")
	if err != nil || !existing || second != nil {
		t.Fatal(existing, err)
	}
	select {
	case <-activated:
	case <-time.After(2 * time.Second):
		t.Fatal("existing window was not activated")
	}
	first.Close()
	third, existing, err := acquireNamedInstance("isolated-test")
	if err != nil || existing {
		t.Fatal("lock was not released", err)
	}
	third.Close()
}

func TestMacManagedCancellationStopsChildProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $! > \"$1\"; wait", "resourcelens-test", pidFile)
	finished := make(chan error, 1)
	go func() { finished <- runManagedCommand(ctx, cmd) }()
	pid := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not stop")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child process survived cancellation")
}

func TestMacToolNamesAndDataPaths(t *testing.T) {
	if executableName("ffmpeg") != "ffmpeg" || updatePackageExtension() != ".dmg" {
		t.Fatal("Windows tool names used on macOS")
	}
	root, err := defaultApplicationRoot()
	if err != nil || !strings.HasSuffix(root, "/Library/Application Support/ResourceLens") {
		t.Fatal(root, err)
	}
	if !strings.HasSuffix(defaultDownloadDirectory(root), "/Downloads/ResourceLens") {
		t.Fatal("default downloads path")
	}
	custom := t.TempDir()
	if defaultDownloadDirectory(custom) != filepath.Join(custom, "downloads") {
		t.Fatal("explicit test/data root was ignored")
	}
}

func TestMacOwnedCacheAllowsBaseAliasesAndRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "data", "preview-cache")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if !ownedDirectory(base, inside) {
		t.Fatal("OS /var alias rejected")
	}
	out := t.TempDir()
	link := filepath.Join(base, "data", "redirected")
	if err := os.Symlink(out, link); err != nil {
		t.Fatal(err)
	}
	if ownedDirectory(base, link) {
		t.Fatal("symlink outside owned cache accepted")
	}
}

func TestMacNativeOriginKeepsAPIAccessRestricted(t *testing.T) {
	e := testEngine(t, 1)
	if !e.allowedOrigin("wails://wails") {
		t.Fatal("native WKWebView rejected")
	}
	for _, origin := range []string{"null", "wails://evil", "wails://wails.evil", "https://example.com"} {
		if e.allowedOrigin(origin) {
			t.Fatal("untrusted origin accepted", origin)
		}
	}
}
