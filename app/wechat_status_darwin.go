package main

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// lsof is restricted to the current user's WeChat processes and loopback TCP
// endpoints. No account data or packet contents are inspected.
func wechatConnections(capturePort int, previousProxy string) (wechatConnectionStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return wechatConnectionStatus{}, err
	}
	pids := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		command := strings.ToLower(strings.Join(parts[1:], " "))
		if strings.Contains(command, "/wechat.app/") || strings.Contains(command, "/weixin.app/") || strings.Contains(command, "/wechatappex.app/") {
			if _, err := strconv.Atoi(parts[0]); err == nil {
				pids = append(pids, parts[0])
			}
		}
	}
	result := wechatConnectionStatus{Running: len(pids) > 0}
	if !result.Running {
		return result, nil
	}
	output, err = exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strings.Join(pids, ","), "-iTCP", "-sTCP:ESTABLISHED", "-Fn").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && len(output) == 0 {
			return result, nil
		}
		return result, err
	}
	previousPorts := map[int]bool{}
	for _, addr := range strings.Split(previousProxy, ";") {
		if _, value, ok := strings.Cut(addr, "="); ok {
			addr = value
		}
		host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
		if err == nil && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
			n, _ := strconv.Atoi(port)
			previousPorts[n] = n != capturePort
		}
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		_, remote, ok := strings.Cut(line[1:], "->")
		if !ok {
			continue
		}
		host, port, err := net.SplitHostPort(strings.TrimSpace(remote))
		if err != nil || !(host == "localhost" || net.ParseIP(host).IsLoopback()) {
			continue
		}
		n, _ := strconv.Atoi(port)
		if n == capturePort {
			result.CaptureConnections++
		} else if previousPorts[n] {
			result.PreviousConnections++
		}
	}
	return result, nil
}
