package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ProxySnapshot struct {
	Enabled      uint64         `json:"enabled"`
	Server       string         `json:"server"`
	HasEnabled   bool           `json:"hasEnabled"`
	HasServer    bool           `json:"hasServer"`
	OwnedAddress string         `json:"ownedAddress"`
	Services     []NetworkProxy `json:"services,omitempty"`
}

// macOS keeps separate settings for each network service and protocol.
type NetworkProxy struct {
	Name        string        `json:"name"`
	HTTP        ProxyEndpoint `json:"http"`
	HTTPS       ProxyEndpoint `json:"https"`
	AutoURL     string        `json:"autoUrl,omitempty"`
	AutoEnabled bool          `json:"autoEnabled"`
	Discovery   bool          `json:"discovery"`
}
type ProxyEndpoint struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Authenticated bool   `json:"authenticated,omitempty"`
}

func captureProxySnapshot(old ProxySnapshot, addr string) (ProxySnapshot, error) {
	next := ProxySnapshot{Enabled: 1, HasEnabled: true, Server: addr, HasServer: true}
	for _, service := range old.Services {
		if service.HTTP.Authenticated || service.HTTPS.Authenticated {
			return next, fmt.Errorf("网络服务 %s 使用认证代理，请先在系统设置中关闭认证代理再捕获", service.Name)
		}
		var port int
		if _, err := fmt.Sscanf(addr, "127.0.0.1:%d", &port); err != nil {
			return next, err
		}
		endpoint := ProxyEndpoint{Enabled: true, Host: "127.0.0.1", Port: port}
		next.Services = append(next.Services, NetworkProxy{Name: service.Name, HTTP: endpoint, HTTPS: endpoint, AutoURL: service.AutoURL})
	}
	return next, nil
}

func proxyRestoreSnapshot(current, old ProxySnapshot) (ProxySnapshot, bool) {
	if len(old.Services) == 0 {
		return old, current.Server == old.OwnedAddress && current.Enabled == 1
	}
	// Restore each endpoint still owned by this capture session. This also
	// recovers a crash between the HTTP and HTTPS networksetup commands.
	result := ProxySnapshot{}
	for _, before := range old.Services {
		for _, now := range current.Services {
			if now.Name != before.Name {
				continue
			}
			httpAddr := fmt.Sprintf("%s:%d", now.HTTP.Host, now.HTTP.Port)
			httpsAddr := fmt.Sprintf("%s:%d", now.HTTPS.Host, now.HTTPS.Port)
			ownsHTTP := now.HTTP.Enabled && httpAddr == old.OwnedAddress
			ownsHTTPS := now.HTTPS.Enabled && httpsAddr == old.OwnedAddress
			if !ownsHTTP && !ownsHTTPS || now.HTTP.Authenticated || now.HTTPS.Authenticated {
				continue
			}
			merged := now
			if ownsHTTP {
				merged.HTTP = before.HTTP
			}
			if ownsHTTPS {
				merged.HTTPS = before.HTTPS
			}
			// Restore PAC only if both endpoints are ours or the other endpoint
			// was never changed. Preserve explicitly edited PAC/discovery state.
			if (ownsHTTP || now.HTTP == before.HTTP) && (ownsHTTPS || now.HTTPS == before.HTTPS) && now.AutoURL == before.AutoURL {
				if !now.AutoEnabled {
					merged.AutoEnabled = before.AutoEnabled
				}
				if !now.Discovery {
					merged.Discovery = before.Discovery
				}
			}
			result.Services = append(result.Services, merged)
		}
	}
	return result, len(result.Services) > 0
}

func (e *Engine) recoveryPath() string { return filepath.Join(e.root, "data", "proxy-recovery.json") }
func (e *Engine) recoverProxy() error {
	b, err := os.ReadFile(e.recoveryPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var old ProxySnapshot
	if err = json.Unmarshal(b, &old); err != nil {
		return fmt.Errorf("代理恢复记录损坏：%w", err)
	}
	current, err := e.readProxy()
	if err != nil {
		return err
	}
	if restore, owned := proxyRestoreSnapshot(current, old); owned {
		if err = e.writeProxy(restore); err != nil {
			return err
		}
	}
	return os.Remove(e.recoveryPath())
}
func (e *Engine) enableSystemProxyLocked() error {
	old, err := e.readProxy()
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", e.config.ProxyPort)
	next, err := captureProxySnapshot(old, addr)
	if err != nil {
		return err
	}
	old.OwnedAddress = addr
	b, _ := json.MarshalIndent(old, "", "  ")
	if err = atomicWrite(e.recoveryPath(), b, 0600); err != nil {
		return err
	}
	if err = e.writeProxy(next); err != nil {
		// Keep the durable record if rollback also fails (including cancelled
		// macOS authorization), so the next launch can finish recovery.
		if restoreErr := e.writeProxy(old); restoreErr == nil {
			os.Remove(e.recoveryPath())
		}
		return err
	}
	e.proxySnapshot = &old
	return nil
}
func (e *Engine) restoreProxyLocked() error {
	if e.proxySnapshot == nil {
		return nil
	}
	current, err := e.readProxy()
	if err != nil {
		return err
	}
	if restore, owned := proxyRestoreSnapshot(current, *e.proxySnapshot); owned {
		if err = e.writeProxy(restore); err != nil {
			return err
		}
	}
	if err = os.Remove(e.recoveryPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	e.proxySnapshot = nil
	return nil
}
