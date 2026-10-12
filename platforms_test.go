package main

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestPublicPlatformTargetFixtures(t *testing.T) {
	html := []byte(`<script>window.__INITIAL_STATE__={"note":{"noteDetailMap":{"0123456789abcdef01234567":{"note":{"noteId":"0123456789abcdef01234567","title":"undefined is part of title","user":{"nickname":"author"},"optional":undefined,"video":{"media":{"stream":{"h264":[{"masterUrl":"https://cdn.example.com/high.mp4?token=old","backupUrls":["https://backup.example.com/high.mp4?token=other"],"qualityType":"1080P","width":1080,"height":1920,"fps":30,"avgBitrate":2000000,"audioCodec":"aac","size":20000000,"duration":10000}]}}}}},"other":{"note":{"noteId":"other","title":"recommendation"}}}}};</script>`)
	data := publicPageData(html)
	if len(data) != 1 {
		t.Fatalf("parsed %d state records", len(data))
	}
	v := publicXHSVideo(data[0], "0123456789abcdef01234567", "https://www.xiaohongshu.com/explore/0123456789abcdef01234567?xsec_token=required")
	if v == nil || v.Title != "undefined is part of title" || len(v.Formats) != 2 || v.Duration != 10 {
		t.Fatalf("failed to parse target video: %+v", v)
	}
	if publicXHSVideo(data[0], "different", "https://www.xiaohongshu.com/explore/different") != nil {
		t.Fatal("substituted recommendation")
	}
	f := publicFormats(v)
	if len(f) != 1 || f[0].Width != 1080 || f[0].Bitrate != 2000 {
		t.Fatalf("mirror ladder duplicate: %+v", f)
	}
	if platformChoiceID(v.Formats[0]) != platformChoiceID(v.Formats[1]) {
		t.Fatal("signed mirrors must share one quality ID")
	}
	ks := decodeDouyinJSON([]byte(`{"recommended":{"photoId":"3xother000000000","photoUrl":"https://cdn.example.com/recommendation.mp4"},"requested":{"photoId":"3xtarget00000000","caption":"target","userName":"author","videoResource":{"h264":{"adaptationSet":[{"representation":[{"url":"https://cdn.example.com/clip.mp4","qualityType":"720P","width":720,"height":1280,"fps":60,"bitrate":3000000}]}]}}}}`))
	k := findPublicKSVideo(ks, "3xtarget00000000", "https://www.kuaishou.com/short-video/3xtarget00000000", 0)
	if k == nil || k.Title != "target" || len(k.Formats) != 1 || douyinNumber(k.Formats[0]["height"]) != 1280 {
		t.Fatalf("failed to parse exact KS target: %+v", k)
	}
	if findPublicKSVideo(ks, "3xmissing0000000", "https://www.kuaishou.com/short-video/3xmissing0000000", 0) != nil {
		t.Fatal("substituted KS recommendation")
	}
}
func TestPublicPlatformShareNormalization(t *testing.T) {
	raw, err := normalizeVideoInput("标题  https://www.xiaohongshu.com/explore/0123456789abcdef01234567?xsec_token=abc%3D  复制链接")
	if err != nil || !strings.HasSuffix(raw, "xsec_token=abc%3D") {
		t.Fatal(raw, err)
	}
	u, _ := url.Parse(raw)
	if publicPlatformID(u) != "0123456789abcdef01234567" {
		t.Fatal("invalid note ID")
	}
	if _, err := normalizeVideoInput("https://www.kuaishou.com/short-video/3xtarget00000000 https://www.xiaohongshu.com/explore/0123456789abcdef01234567"); err == nil {
		t.Fatal("ambiguous works accepted")
	}
}
func TestDouyinQualityChoiceStableAcrossMirrors(t *testing.T) {
	a := map[string]any{"width": 1080, "height": 1920, "fps": 60, "tbr": 2500, "vcodec": "h265", "url": "https://a.example.com/video?sign=one", "format_id": "public-0"}
	b := map[string]any{}
	for k, v := range a {
		b[k] = v
	}
	b["url"] = "https://b.example.com/video?sign=two"
	b["format_id"] = "public-7"
	if douyinChoiceID(a) != douyinChoiceID(b) {
		t.Fatal("choice depends on signed mirror")
	}
	b["height"] = 1280
	if douyinChoiceID(a) == douyinChoiceID(b) {
		t.Fatal("different rendition has same choice")
	}
}
func TestPublicPlatformRecordedPage(t *testing.T) {
	path := os.Getenv("RESOURCELENS_XHS_PUBLIC_HTML")
	if path == "" {
		t.Skip("set RESOURCELENS_XHS_PUBLIC_HTML for separately fetched public page evidence")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v *douyinPublicVideo
	for _, data := range publicPageData(body) {
		if v = publicXHSVideo(data, "6411cf99000000001300b6d9", "https://www.xiaohongshu.com/explore/6411cf99000000001300b6d9"); v != nil {
			break
		}
	}
	if v == nil {
		// Public fixtures may later disappear or require a current share token.
		// Verify the live path explains that gate, rather than treating its HTML
		// or recommendations as the requested MP4.
		e := &Engine{transport: &http.Transport{}}
		_, err := e.extractPublicPlatform(context.Background(), "https://www.xiaohongshu.com/explore/6411cf99000000001300b6d9")
		if err == nil || !strings.Contains(err.Error(), "播放信息") {
			t.Fatal("actual unavailable page was not identified", err)
		}
		t.Log("actual public page has no target note; live parser correctly reports platform access limitation and saves no MP4")
		return
	}
	if v.Title == "" || len(v.Formats) == 0 {
		t.Fatal("empty actual metadata")
	}
	t.Logf("actual public page: target matched, %d mirror entries, %d distinct quality choices, duration %.3fs", len(v.Formats), len(publicFormats(v)), v.Duration)
	e := &Engine{transport: &http.Transport{}}
	if err := e.verifyPublicPlatform(context.Background(), v, ""); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual public stream header verified: %dx%d, total %d bytes; only prefix requested", int(douyinNumber(v.Formats[0]["width"])), int(douyinNumber(v.Formats[0]["height"])), v.Size)
}
