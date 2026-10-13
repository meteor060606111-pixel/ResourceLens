package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestWechatBridgeSurvivesRestart(t *testing.T) {
	e := testEngine(t, 1)
	e.capture = true
	oldPath, oldToken := e.wechatBridgePath(), e.token
	proxy, _ := url.Parse("http://" + e.proxyListener.Addr().String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	getEpoch := func() string {
		t.Helper()
		response, err := client.Get("http://channels.weixin.qq.com" + oldPath)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var status struct {
			Capture bool   `json:"capture"`
			Epoch   string `json:"epoch"`
		}
		if response.Header.Get("X-ResourceLens-Bridge") != "wechat-v2" || json.NewDecoder(response.Body).Decode(&status) != nil || !status.Capture {
			t.Fatal("missing local bridge acknowledgement")
		}
		return status.Epoch
	}
	oldEpoch := getEpoch()
	e.Close()
	client.CloseIdleConnections()
	restarted, err := NewEngine(e.root, fstest.MapFS{"index.html": {Data: []byte("test UI")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.capture = true
	if restarted.wechatBridgePath() != oldPath || restarted.token == oldToken {
		t.Fatal("restart broke metadata route or reused API bearer")
	}
	if getEpoch() == oldEpoch {
		t.Fatal("restart did not tell the page to replay media metadata")
	}
	body := `{"objectDesc":{"description":"restart fixture","media":[{"mediaType":4,"url":"https://finder.video.qq.com/stodownload?encfilekey=restart-fixture","decodeKey":"1","fileSize":9000}]}}`
	response, err := client.Post("http://channels.weixin.qq.com"+oldPath, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 204 || restarted.wechatMetadataCount.Load() != 1 {
		t.Fatal("already-loaded page could not replay to restarted app")
	}
	for _, resource := range restarted.resources {
		if resource.CaptureRequired || !resource.WechatVideo || resource.wechatSeed == "" || resource.Name != "restart fixture.mp4" {
			t.Fatal("replayed metadata did not unlock captured media")
		}
	}
}

func TestWechatLoadedPageReconnectReplaysOnlyWhenNeeded(t *testing.T) {
	e := testEngine(t, 1)
	tools, err := e.videoTools()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.ReplaceAll(wechatBridgeScript, "__RESOURCE_LENS_WECHAT_BRIDGE__", `"/__resourcelens__/wechat/fixture"`)
	code := `globalThis.location={hostname:"channels.weixin.qq.com"};
let timer,capture=true,epoch="first",local=true,posts=[];
globalThis.setInterval=fn=>(timer=fn,1);
globalThis.fetch=async (url,options)=>{
 if(options.credentials!=="omit")throw Error("credentials transmitted");
 if(options.method==="GET")return {ok:true,headers:{get:()=>local?"wechat-v2":null},json:async()=>({capture,epoch})};
 posts.push(JSON.parse(options.body));return {ok:true,headers:{get:()=>"wechat-v2"}};
};
` + bootstrap + `
const desc={description:"loaded page fixture",media:[{mediaType:4,url:"https://finder.video.qq.com/stodownload?encfilekey=fixture",decodeKey:"1"}]};
globalThis.__resourceLensWechatCollect(desc);
const settle=()=>new Promise(resolve=>setImmediate(resolve));
await settle();if(posts.length!==1)throw Error("initial metadata missing");
await timer();if(posts.length!==1)throw Error("replayed unchanged page after clearing resource list");
capture=false;await timer();if(posts.length!==1)throw Error("replayed while capture stopped");
capture=true;epoch="second";await timer();if(posts.length!==2)throw Error("capture restart not recovered");
epoch="new application";await timer();if(posts.length!==3)throw Error("application restart not recovered");
local=false;epoch="upstream";await timer();if(posts.length!==3)throw Error("sent metadata without local acknowledgement");
` + bootstrap + `
if(globalThis.__resourceLensWechatInstalled!==2)throw Error("reinstalled legacy bootstrap");
`
	file := filepath.Join(t.TempDir(), "reconnect-check.mjs")
	if err = os.WriteFile(file, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, toolPath(tools, "node"), file).CombinedOutput(); err != nil {
		t.Fatalf("loaded page reconnect: %v %s", err, output)
	}
}
