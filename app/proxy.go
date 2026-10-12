package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, key := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(key))
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
}
func (e *Engine) serveProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		e.connect(w, r)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "Use this port as an HTTP proxy", 400)
		return
	}
	e.forward(w, r)
}
func (e *Engine) forward(w http.ResponseWriter, r *http.Request) {
	if e.handleWechatBridge(w, r) {
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	stripHop(out.Header)
	out.Header.Set("Accept-Encoding", "identity")
	if out.URL.Scheme == "" {
		out.URL.Scheme = "https"
	}
	if out.URL.Host == "" {
		out.URL.Host = r.Host
	}
	e.refreshWechatRequest(out)
	resp, err := e.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "Upstream request failed", 502)
		return
	}
	defer resp.Body.Close()
	e.mu.RLock()
	active := e.capture
	e.mu.RUnlock()
	if active {
		if hostIn(out.URL.Hostname(), "channels.weixin.qq.com") {
			e.wechatRequests.Add(1)
		}
		e.inspectWechatResponse(out, resp)
	}
	if active && r.Method == "GET" && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		size := resp.ContentLength
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if _, _, total, ok := parseContentRange(cr); ok {
				size = total
			}
		}
		e.remember(out.URL.String(), r.Header, resp.Header.Get("Content-Type"), size, resp.Header.Get("Content-Disposition"), false)
	}
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type singleListener struct {
	conn   net.Conn
	sent   bool
	closed chan struct{}
	once   sync.Once
}

func (l *singleListener) Accept() (net.Conn, error) {
	if !l.sent {
		l.sent = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *singleListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *singleListener) Addr() net.Addr { return l.conn.LocalAddr() }

type notifyConn struct {
	net.Conn
	close func()
}

func (c *notifyConn) Close() error { err := c.Conn.Close(); c.close(); return err }
func (e *Engine) track(c net.Conn, add bool) {
	e.connsMu.Lock()
	defer e.connsMu.Unlock()
	if add {
		e.conns[c] = true
	} else {
		delete(e.conns, c)
	}
}
func (e *Engine) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || host == "" || port == "" {
		http.Error(w, "Invalid CONNECT target", 400)
		return
	}
	e.mu.RLock()
	intercept := e.capture && e.config.HTTPS
	e.mu.RUnlock()
	if !intercept {
		up, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
		if err != nil {
			http.Error(w, "Connection failed", 502)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		client, rw, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		e.track(client, true)
		e.track(up, true)
		defer func() { client.Close(); up.Close(); e.track(client, false); e.track(up, false) }()
		rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		rw.Flush()
		done := make(chan struct{}, 1)
		go func() { io.Copy(up, rw); up.Close(); done <- struct{}{} }()
		io.Copy(client, up)
		client.Close()
		<-done
		return
	}
	cert, err := e.ca.ForHost(host)
	if err != nil {
		http.Error(w, "Certificate creation failed", 500)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	e.track(client, true)
	defer func() { client.Close(); e.track(client, false) }()
	rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	rw.Flush()
	tlsConn := tls.Server(&bufferedConn{Conn: client, reader: rw.Reader}, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	tlsConn.SetDeadline(time.Now().Add(15 * time.Second))
	if err = tlsConn.HandshakeContext(e.ctx); err != nil {
		if hostIn(host, "channels.weixin.qq.com") && e.ctx.Err() == nil {
			e.wechatTLSFailures.Add(1)
		}
		return
	}
	tlsConn.SetDeadline(time.Time{})
	listener := &singleListener{closed: make(chan struct{})}
	listener.conn = &notifyConn{Conn: tlsConn, close: func() { listener.Close() }}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.URL.Scheme = "https"
		req.URL.Host = r.Host
		e.forward(w, req)
	}), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 45 * time.Second}
	err = server.Serve(listener)
	if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		return
	}
}
