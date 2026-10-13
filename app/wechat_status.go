package main

import "strconv"

type wechatConnectionStatus struct {
	Running             bool `json:"running"`
	CaptureConnections  int  `json:"captureConnections"`
	PreviousConnections int  `json:"previousProxyConnections"`
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
