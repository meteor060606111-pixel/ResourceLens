package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWechatCachedPageReloadFetchesObservedScript(t *testing.T) {
	e := testEngine(t, 1)
	e.capture = true
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(304)
			return
		}
		if r.URL.Path == "/web/pages/feed" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<script src="http://res.wx.qq.com/new-finder-layout/chunk.publish.js?v=a%2Fb&amp;order=1" nonce="unchanged"></script><script src="https://example.org/unrelated.js"></script>`)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, `import "./virtual_svg-icons-register.publish.js";class Model {get media(){return this.objectDesc.media}}`)
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	e.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, originURL.Host)
	}
	proxy, _ := url.Parse("http://" + e.proxyListener.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	request, _ := http.NewRequest("GET", "http://channels.weixin.qq.com/web/pages/feed", nil)
	request.Header.Set("If-None-Match", "old-page")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("cached page was not refreshed")
	}
	if !strings.Contains(string(page), `nonce="unchanged"`) || !strings.Contains(string(page), `src="https://example.org/unrelated.js"`) {
		t.Fatal("changed unrelated attributes or assets")
	}
	parts := wechatScriptAttribute.FindAllSubmatch(page, -1)
	if len(parts) != 2 {
		t.Fatal("lost script tags")
	}
	asset := strings.ReplaceAll(string(parts[0][1]), "&amp;", "&")
	assetURL, _ := url.Parse(asset)
	if !assetURL.Query().Has("__rlwv") || assetURL.Query().Get("v") != "a/b" || assetURL.Query().Get("order") != "1" {
		t.Fatal("lost original query or missing fresh asset version")
	}
	request, _ = http.NewRequest("GET", asset, nil)
	request.Header.Set("If-None-Match", "old-script")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	script, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(script), "__resourceLensWechatCollect?.(this.objectDesc)") || !strings.Contains(string(script), "__resourceLensWechatInstalled") {
		t.Fatal("refreshed script was not observed")
	}
	if strings.Contains(string(script), e.token) {
		t.Fatal("exposed application API token")
	}
	if !strings.Contains(string(script), "virtual_svg-icons-register.publish.js?__rlwv=") {
		t.Fatal("nested metadata module can still use stale browser cache")
	}
	if e.wechatHTMLPatches.Load() != 1 || e.wechatScriptPatches.Load() != 1 {
		t.Fatal("missing capture observations")
	}
	a := e.remember("https://finder.video.qq.com/stodownload?encfilekey=one-file&start=0", nil, "video/mp4", 99, "", false)
	b := e.remember("https://finder.video.qq.com/stodownload?encfilekey=one-file&start=100&token=refreshed", nil, "video/mp4", 99, "", false)
	if a == nil || b == nil || a.ID != b.ID || len(e.resources) != 1 {
		t.Fatal("one encrypted media produced duplicate resources")
	}
}

func TestWechatDetailHookPreservesNestedReturn(t *testing.T) {
	e := testEngine(t, 1)
	request, _ := http.NewRequest("GET", "https://res.wx.qq.com/web-finder/details.js", nil)
	original := `class Details{async finderGetCommentDetail(value){return Promise.resolve({objectDesc:{value: value}});}async other(){return 7}}`
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/javascript"}}, Body: io.NopCloser(strings.NewReader(original))}
	e.inspectWechatResponse(request, response)
	modified, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	code := `globalThis.location={hostname:"channels.weixin.qq.com"};` + string(modified) + `;let observed;globalThis.__resourceLensWechatCollect=value=>observed=value;const actual=await new Details().finderGetCommentDetail("fixture");if(actual.objectDesc.value!=="fixture"||observed!==actual)throw Error("changed original result or lost callback");`
	file := filepath.Join(t.TempDir(), "detail-check.mjs")
	if err = os.WriteFile(file, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, toolPath(tools, "node"), file)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("modified detail callback: %v %s", err, output)
	}
}
