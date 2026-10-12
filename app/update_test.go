package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateBackupChecksCopiedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "previous.exe")
	if err := os.WriteFile(path, []byte("original program bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sha, size, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyUpdateBackup(path, sha, size); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("different bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifyUpdateBackup(path, sha, size); err == nil {
		t.Fatal("accepted changed backup")
	}
}

func TestUpdateRestoreFailureMessagesMatchActualResult(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			root := t.TempDir()
			backup, target := filepath.Join(root, "previous.exe"), filepath.Join(root, "ResourceLens.exe")
			if err := os.WriteFile(backup, []byte("previous working program"), 0600); err != nil {
				t.Fatal(err)
			}
			if success {
				if err := os.WriteFile(target, []byte("broken updated program"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("preserve directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var status, message string
			cause := errors.New("installation failed")
			err := restoreFailedUpdate(backup, target, "安装程序执行失败", cause, func(s, m string) { status, message = s, m })
			if !errors.Is(err, cause) || status != "failed" {
				t.Fatalf("lost installation result: %v %s", err, status)
			}
			if success {
				if !strings.Contains(message, "已恢复之前的主程序") {
					t.Fatalf("wrong successful restore message: %s", message)
				}
				original, _, _ := fileSHA256(backup)
				restored, _, _ := fileSHA256(target)
				if original != restored {
					t.Fatal("restored program differs from backup")
				}
			} else {
				if strings.Contains(message, "已恢复") || !strings.Contains(message, "恢复之前的主程序失败") || !strings.Contains(message, `data\updates`) {
					t.Fatalf("wrong failed restore message: %s", message)
				}
				if _, err := os.Stat(backup); err != nil {
					t.Fatal("backup lost after restoration failure")
				}
				if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
					t.Fatal("existing target directory was damaged")
				}
			}
		})
	}
}

func TestUpdateVersionAndSourceValidation(t *testing.T) {
	for input, expected := range map[string]string{"1.2.1": "1.2.1", "v1.2.1": "1.2.1", "01.002.3.0": "1.2.3", "1.2.3.4": "1.2.3.4"} {
		value, err := normalizedVersion(input)
		if err != nil || value != expected {
			t.Fatalf("normalize %q = %q, %v", input, value, err)
		}
	}
	for _, bad := range []string{"1.2", "1.2.3-beta", "-1.2.3", "65536.2.3", "1.2.3;cmd"} {
		if _, err := normalizedVersion(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if compareVersions("1.10.0", "1.9.99") != 1 || compareVersions("1.2.0.0", "1.2.0") != 0 || compareVersions("1.2.0", "2.0.0") != -1 {
		t.Fatal("numeric version comparison failed")
	}
	for _, bad := range []string{"http://example.com/feed.json", "file:///feed.json", "https://user:password@example.com/feed.json", "https://example.com/feed.json#fragment", "https://"} {
		if validateUpdateURL(bad) == nil {
			t.Fatalf("accepted update source %q", bad)
		}
	}
	if err := validateUpdateURL("https://example.com/updates.json"); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateLocalRejectsWrongExecutableAndOldInstaller(t *testing.T) {
	root := t.TempDir()
	wrong := filepath.Join(root, "wrong.exe")
	if err := os.WriteFile(wrong, make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectUpdatePackage(wrong); err == nil {
		t.Fatal("accepted executable without PE/version information")
	}
	installer := os.Getenv("RESOURCELENS_PREVIOUS_INSTALLER")
	if installer == "" {
		t.Skip("set RESOURCELENS_PREVIOUS_INSTALLER to the prior 1.1.8 installer for the real package check")
	}
	if _, err := os.Stat(installer); err != nil {
		t.Skip("prior real installer not available")
	}
	info, err := inspectUpdatePackage(installer)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "1.1.8" || len(info.SHA256) != 64 {
		t.Fatalf("unexpected recognized package: %#v", info)
	}
	e := &Engine{root: root}
	if _, err = e.prepareLocalUpdate(installer); err == nil {
		t.Fatal("accepted prior installer as upgrade")
	}
	if _, err = os.Stat(e.updateDir()); !os.IsNotExist(err) {
		t.Fatal("rejected old package must not stage files")
	}
}

func TestUpdateSingleInstanceSignalsExistingOwner(t *testing.T) {
	name := fmt.Sprintf(`Local\ResourceLens.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
	first, existing, err := acquireNamedInstance(name)
	if err != nil || existing {
		t.Fatalf("first instance: %v %v", existing, err)
	}
	defer first.Close()
	activated := make(chan struct{}, 1)
	first.listen(func() { activated <- struct{}{} })
	second, existing, err := acquireNamedInstance(name)
	if err != nil || !existing || second != nil {
		t.Fatalf("second instance: %v %v", existing, err)
	}
	select {
	case <-activated:
	case <-time.After(2 * time.Second):
		t.Fatal("existing owner was not signalled")
	}
}
