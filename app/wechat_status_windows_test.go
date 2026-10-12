package main

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWechatNativeConnectionInspection(t *testing.T) {
	for _, test := range []struct {
		name, network, address string
		family                 uint32
	}{
		{"IPv4", "tcp4", "127.0.0.1:0", windows.AF_INET},
		{"IPv6", "tcp6", "[::1]:0", windows.AF_INET6},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen(test.network, test.address)
			if err != nil {
				if test.family == windows.AF_INET6 {
					t.Skip("IPv6 loopback unavailable")
				}
				t.Fatal(err)
			}
			defer listener.Close()
			client, err := net.Dial(test.network, listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			port := listener.Addr().(*net.TCPAddr).Port
			table, err := ownerTCPTable(test.family)
			if err != nil {
				t.Fatal(err)
			}
			owners := map[uint32]bool{uint32(os.Getpid()): true}
			capture, previous := countWechatLoopback(table, test.family, owners, port, nil)
			if capture != 1 || previous != 0 {
				t.Fatalf("live connection was not recognized: %d %d", capture, previous)
			}
			capture, previous = countWechatLoopback(table, test.family, owners, 0, map[int]bool{port: true})
			if capture != 0 || previous != 1 {
				t.Fatalf("previous proxy was not recognized: %d %d", capture, previous)
			}
			capture, previous = countWechatLoopback(table, test.family, map[uint32]bool{}, port, map[int]bool{port: true})
			if capture != 0 || previous != 0 {
				t.Fatal("included another process's connection")
			}
		})
	}
}
