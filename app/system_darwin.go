package main

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func hideCommand(cmd *exec.Cmd) {}

// Each media operation owns a process group, including browser/FFmpeg children.
func runManagedCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	defer syscall.Kill(-pid, syscall.SIGKILL)
	finished, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			syscall.Kill(-pid, syscall.SIGKILL)
		case <-finished:
		}
	}()
	err := cmd.Wait()
	close(finished)
	<-stopped
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func openInExplorer(location string, selectFile bool) error {
	if !filepath.IsAbs(location) {
		return errors.New("保存位置必须是完整路径")
	}
	info, err := os.Stat(location)
	if err != nil {
		return fmt.Errorf("保存位置不存在或无法访问：%s", location)
	}
	if selectFile == info.IsDir() {
		return errors.New("保存位置类型无效，请重新选择下载目录")
	}
	args := []string{location}
	if selectFile {
		args = []string{"-R", location}
	}
	return exec.Command("/usr/bin/open", args...).Run()
}

func startupError(err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	text := err.Error() + "\n\n请检查配置目录 data/app.log。"
	exec.CommandContext(ctx, "/usr/bin/osascript", "-e", "display alert \"拾流无法启动\" message "+appleScriptString(text)+" as critical").Run()
}

func commitDownload(from, to string) error {
	// Link is atomic and refuses an existing destination, unlike os.Rename.
	if err := os.Link(from, to); err != nil {
		return err
	}
	return os.Remove(from)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func appleScriptString(s string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\r", "\\r").Replace(s) + "\""
}

func macCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s：%s", filepath.Base(path), strings.TrimSpace(string(output)))
	}
	return output, nil
}

func networkSetup(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := macCommand(ctx, "/usr/sbin/networksetup", args...)
	return string(output), err
}

func networkFields(output string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return fields
}

func parseProxyEndpoint(output string) (ProxyEndpoint, error) {
	f := networkFields(output)
	if f["Enabled"] != "Yes" && f["Enabled"] != "No" {
		return ProxyEndpoint{}, errors.New("无法读取 macOS 网络代理状态")
	}
	port, _ := strconv.Atoi(f["Port"])
	return ProxyEndpoint{Enabled: f["Enabled"] == "Yes", Host: f["Server"], Port: port, Authenticated: f["Authenticated Proxy Enabled"] == "1" || f["Authenticated Proxy Enabled"] == "Yes"}, nil
}

func proxyState() (ProxySnapshot, error) {
	list, err := networkSetup("-listallnetworkservices")
	if err != nil {
		return ProxySnapshot{}, err
	}
	result := ProxySnapshot{HasEnabled: true, HasServer: true, Enabled: 1}
	for _, name := range strings.Split(list, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || strings.HasPrefix(name, "*") || strings.Contains(name, "asterisk") {
			continue
		}
		service := NetworkProxy{Name: name}
		for _, item := range []struct {
			option string
			target *ProxyEndpoint
		}{{"-getwebproxy", &service.HTTP}, {"-getsecurewebproxy", &service.HTTPS}} {
			output, err := networkSetup(item.option, name)
			if err != nil {
				return result, err
			}
			*item.target, err = parseProxyEndpoint(output)
			if err != nil {
				return result, err
			}
		}
		output, err := networkSetup("-getautoproxyurl", name)
		if err != nil {
			return result, err
		}
		f := networkFields(output)
		service.AutoURL, service.AutoEnabled = f["URL"], f["Enabled"] == "Yes"
		if service.AutoURL == "(null)" {
			service.AutoURL = ""
		}
		output, err = networkSetup("-getproxyautodiscovery", name)
		if err != nil {
			return result, err
		}
		service.Discovery = strings.EqualFold(networkFields(output)["Auto Proxy Discovery"], "On")
		addr := fmt.Sprintf("%s:%d", service.HTTP.Host, service.HTTP.Port)
		if len(result.Services) == 0 {
			result.Server = addr
		}
		if !service.HTTP.Enabled || !service.HTTPS.Enabled || service.HTTP.Host != service.HTTPS.Host || service.HTTP.Port != service.HTTPS.Port || result.Server != addr || service.AutoEnabled || service.Discovery {
			result.Enabled = 0
		}
		result.Services = append(result.Services, service)
	}
	if len(result.Services) == 0 {
		return result, errors.New("未找到启用的网络服务，请在系统设置中启用 Wi-Fi 或以太网")
	}
	return result, nil
}

func proxyCommands(s ProxySnapshot) ([]string, error) {
	commands := []string{}
	state := func(enabled bool) string {
		if enabled {
			return "on"
		}
		return "off"
	}
	command := func(args ...string) {
		quoted := []string{"/usr/sbin/networksetup"}
		for _, a := range args {
			quoted = append(quoted, shellQuote(a))
		}
		commands = append(commands, strings.Join(quoted, " "))
	}
	for _, service := range s.Services {
		if service.Name == "" {
			return nil, errors.New("网络服务名称为空")
		}
		for _, item := range []struct {
			set, enable string
			endpoint    ProxyEndpoint
		}{{"-setwebproxy", "-setwebproxystate", service.HTTP}, {"-setsecurewebproxy", "-setsecurewebproxystate", service.HTTPS}} {
			p := item.endpoint
			if p.Authenticated {
				return nil, errors.New("不能改写认证代理")
			}
			if p.Host != "" && p.Port > 0 && p.Port <= 65535 {
				command(item.set, service.Name, p.Host, strconv.Itoa(p.Port))
			} else if p.Enabled {
				return nil, errors.New("代理地址或端口无效")
			}
			command(item.enable, service.Name, state(p.Enabled))
		}
		if service.AutoURL != "" {
			command("-setautoproxyurl", service.Name, service.AutoURL)
		}
		command("-setautoproxystate", service.Name, state(service.AutoEnabled))
		command("-setproxyautodiscovery", service.Name, state(service.Discovery))
	}
	if len(commands) == 0 {
		return nil, errors.New("没有可设置的网络服务")
	}
	return commands, nil
}

func applyProxy(s ProxySnapshot) error {
	commands, err := proxyCommands(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err = macCommand(ctx, "/usr/bin/osascript", "-e", "do shell script "+appleScriptString("set -e; "+strings.Join(commands, "; "))+" with administrator privileges")
	if err != nil {
		return fmt.Errorf("macOS 网络代理设置未完成，请允许系统授权：%w", err)
	}
	return nil
}

func loginKeychain() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db"), err
}

func (e *Engine) isCertificateInstalled() bool {
	keychain, err := loginKeychain()
	if err != nil || e.ca == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := macCommand(ctx, "/usr/bin/security", "find-certificate", "-a", "-c", e.ca.cert.Subject.CommonName, "-p", keychain)
	if err != nil {
		return false
	}
	found := false
	for len(output) > 0 {
		block, rest := pem.Decode(output)
		if block == nil {
			break
		}
		output = rest
		if block.Type == "CERTIFICATE" && string(block.Bytes) == string(e.ca.cert.Raw) {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	// Presence alone is insufficient: verify that this exact CA is trusted for SSL.
	_, err = macCommand(ctx, "/usr/bin/security", "verify-cert", "-c", e.ca.certPath, "-p", "ssl", "-L")
	return err == nil
}

func (e *Engine) installCertificate() error {
	keychain, err := loginKeychain()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err = macCommand(ctx, "/usr/bin/security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", keychain, e.ca.certPath)
	if err != nil {
		return fmt.Errorf("钥匙串证书安装未完成，请允许 macOS 信任授权：%w", err)
	}
	if !e.isCertificateInstalled() {
		return errors.New("证书尚未获得 SSL 信任，请在钥匙串访问中确认拾流证书的信任设置")
	}
	return nil
}

func (e *Engine) removeCertificate() error {
	keychain, err := loginKeychain()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err = macCommand(ctx, "/usr/bin/security", "remove-trusted-cert", e.ca.certPath); err != nil {
		return err
	}
	_, err = macCommand(ctx, "/usr/bin/security", "delete-certificate", "-Z", e.ca.Thumbprint, keychain)
	return err
}
