package main

import (
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wechatConnectionStatus struct {
	Running             bool `json:"running"`
	CaptureConnections  int  `json:"captureConnections"`
	PreviousConnections int  `json:"previousProxyConnections"`
}

// Inspect only process names and loopback TCP endpoints; no command lines,
// account data, payloads, or unrelated connection addresses leave this function.
func wechatConnections(capturePort int, previousProxy string) (wechatConnectionStatus, error) {
	status := wechatConnectionStatus{}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return status, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	pids := map[uint32]bool{}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		switch strings.ToLower(windows.UTF16ToString(entry.ExeFile[:])) {
		case "weixin.exe", "wechat.exe", "wechatappex.exe":
			pids[entry.ProcessID] = true
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return status, err
	}
	status.Running = len(pids) > 0
	if !status.Running {
		return status, nil
	}
	previousPorts := map[int]bool{}
	for _, value := range strings.Split(previousProxy, ";") {
		if i := strings.Index(value, "="); i >= 0 {
			value = value[i+1:]
		}
		host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
		ip := net.ParseIP(host)
		if err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			continue
		}
		port, _ := strconv.Atoi(portText)
		if port > 0 && port != capturePort {
			previousPorts[port] = true
		}
	}
	for _, family := range []uint32{windows.AF_INET, windows.AF_INET6} {
		data, err := ownerTCPTable(family)
		if err != nil {
			return status, err
		}
		capture, previous := countWechatLoopback(data, family, pids, capturePort, previousPorts)
		status.CaptureConnections += capture
		status.PreviousConnections += previous
	}
	return status, nil
}

func ownerTCPTable(family uint32) ([]byte, error) {
	proc := windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	// TCP_TABLE_OWNER_PID_CONNECTIONS = 4. Windows may change the table size
	// between calls, so retry a bounded number of times without assuming a layout size.
	var size uint32
	for attempts := 0; attempts < 3; attempts++ {
		var data []byte
		var pointer uintptr
		if size > 0 {
			if size > 32<<20 {
				return nil, errors.New("TCP table exceeds limit")
			}
			data = make([]byte, size)
			pointer = uintptr(unsafe.Pointer(&data[0]))
		}
		result, _, _ := proc.Call(pointer, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), 4, 0)
		if result == 0 {
			return data, nil
		}
		if syscall.Errno(result) != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, syscall.Errno(result)
		}
	}
	return nil, errors.New("TCP table changed during inspection")
}

func countWechatLoopback(data []byte, family uint32, pids map[uint32]bool, port int, previousPorts map[int]bool) (capture, previous int) {
	if len(data) < 4 {
		return
	}
	rowSize, remoteOffset, remoteSize, portOffset, stateOffset, pidOffset := 24, 12, 4, 16, 0, 20
	if family == windows.AF_INET6 {
		rowSize, remoteOffset, remoteSize, portOffset, stateOffset, pidOffset = 56, 24, 16, 44, 48, 52
	}
	count := min(int(binary.LittleEndian.Uint32(data[:4])), (len(data)-4)/rowSize)
	for i := 0; i < count; i++ {
		row := data[4+i*rowSize : 4+(i+1)*rowSize]
		if binary.LittleEndian.Uint32(row[stateOffset:]) != 5 || !pids[binary.LittleEndian.Uint32(row[pidOffset:])] {
			continue
		}
		if !net.IP(row[remoteOffset : remoteOffset+remoteSize]).IsLoopback() {
			continue
		}
		remotePort := int(binary.BigEndian.Uint16(row[portOffset:]))
		if remotePort == port {
			capture++
		} else if previousPorts[remotePort] {
			previous++
		}
	}
	return
}

func (e *Engine) wechatStatus() map[string]any {
	e.mu.RLock()
	active, cfg, previous := e.capture, e.config, ""
	responses := make([]wechatResponseSummary, 0, len(e.wechatResponses))
	for _, response := range e.wechatResponses {
		responses = append(responses, response)
	}
	if e.proxySnapshot != nil {
		previous = e.proxySnapshot.Server
	}
	e.mu.RUnlock()
	connections, connectionErr := wechatConnections(cfg.ProxyPort, previous)
	proxy, proxyErr := e.readProxy()
	proxyReady := proxyErr == nil && proxy.Enabled == 1 && proxy.Server == "127.0.0.1:"+strconv.Itoa(cfg.ProxyPort)
	requests, patched, metadata, tlsFailures := e.wechatRequests.Load(), e.wechatScriptPatches.Load(), e.wechatMetadataCount.Load(), e.wechatTLSFailures.Load()
	code, message := "waiting", "尚未收到视频号请求。保持捕获开启，在电脑版微信中打开并播放视频；若仍无资源，请正常退出并重新打开微信。"
	switch {
	case !active:
		code, message = "stopped", "先点击开始捕获，再打开电脑版微信的视频号。"
	case !proxyReady:
		code, message = "proxy-changed", "系统代理未指向拾流。请停止后重新开始捕获，再打开微信。"
	case !cfg.HTTPS || !e.certInstalled.Load():
		code, message = "https-unavailable", "视频号需要 HTTPS 捕获，请在设置中开启并安装拾流证书。"
	case !connections.Running && connectionErr == nil:
		code, message = "wechat-closed", "请打开电脑版微信，登录后进入视频号并播放视频。"
	case metadata > 0:
		code, message = "captured", "已捕获视频号媒体信息，可以选择资源下载 MP4。"
	case connections.PreviousConnections > 0 && connections.CaptureConnections == 0 && requests == 0:
		code, message = "previous-proxy", "微信仍连接着原来的本地代理。保持拾流捕获开启，正常退出并重新打开微信，然后进入视频号播放。"
	case tlsFailures > 0 && requests == 0:
		code, message = "tls-failed", "微信请求已到达，但 HTTPS 连接未成功。请确认拾流证书已安装，再退出并重新打开微信。"
	case requests > 0 && patched == 0 && e.wechatHTMLPatches.Load() == 0:
		code, message = "page-not-reloaded", "微信仍在使用之前打开的页面，捕获脚本尚未载入。保持捕获开启，完全退出并重新打开微信，再进入视频号；切换播放画质不能补齐视频信息。"
	case requests > 0:
		code, message = "awaiting-metadata", "已收到视频号请求，正在等待视频信息。请重新进入视频号并播放目标视频。"
	case connections.CaptureConnections > 0:
		code, message = "connected", "微信已连接拾流代理，请进入视频号并播放视频。"
	}
	return map[string]any{"code": code, "message": message, "proxyReady": proxyReady, "connections": connections, "requests": requests, "scriptPatches": patched, "htmlPatches": e.wechatHTMLPatches.Load(), "getterPatches": e.wechatGetterPatches.Load(), "detailPatches": e.wechatDetailPatches.Load(), "mediaRecords": metadata, "tlsFailures": tlsFailures, "connectionCheckAvailable": connectionErr == nil, "responses": responses}
}
