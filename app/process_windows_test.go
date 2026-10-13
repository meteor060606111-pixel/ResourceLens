package main

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagedCancellationStopsChildProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestManagedProcessHelper$")
	cmd.Env = append(os.Environ(), "RESOURCE_LENS_TEST_CHILD=1", "RESOURCE_LENS_TEST_CHILD_PID_FILE="+pidFile)
	finished := make(chan error, 1)
	go func() { finished <- runManagedCommand(ctx, cmd) }()
	var child windows.Handle
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		select {
		case err := <-finished:
			t.Fatalf("parent exited before starting child: %v", err)
		default:
		}
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			child, err = windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child did not start")
	}
	defer windows.CloseHandle(child)
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("managed command did not stop")
	}
	if status, err := windows.WaitForSingleObject(child, 3000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child survived: %v %v", status, err)
	}
}
func TestManagedProcessHelper(t *testing.T) {
	if os.Getenv("RESOURCE_LENS_TEST_CHILD") != "1" {
		return
	}
	child := exec.Command("ping.exe", "127.0.0.1", "-n", "60")
	hideCommand(child)
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("RESOURCE_LENS_TEST_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		child.Process.Kill()
		os.Exit(3)
	}
	child.Wait()
	os.Exit(0)
}
