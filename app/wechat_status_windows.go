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
