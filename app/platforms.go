package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const publicPlatformAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var publicInitialAssignment = regexp.MustCompile(`(?:window\.)?(?:__INITIAL_STATE__|__APOLLO_STATE__|__PRELOADED_STATE__)\s*=\s*`)
var xhsItemPath = regexp.MustCompile(`^/(?:explore|discovery/item)/([a-fA-F0-9]{16,32})(?:/|$)`)
var ksItemPath = regexp.MustCompile(`^/(?:short-video|fw/photo|photo)/([a-zA-Z0-9_-]{8,40})(?:/|$)`)

func isPublicPlatformPage(u *url.URL) bool {
	return u != nil && hostIn(u.Hostname(), "kuaishou.com", "kwai.com", "xiaohongshu.com", "xhslink.com")
}
func publicPlatformID(u *url.URL) string {
	if u == nil {
		return ""
	}
	pattern := ksItemPath
	if hostIn(u.Hostname(), "xiaohongshu.com") {
		pattern = xhsItemPath
	}
	if m := pattern.FindStringSubmatch(u.Path); len(m) == 2 {
		return m[1]
	}
	if hostIn(u.Hostname(), "kuaishou.com", "kwai.com") {
		for _, key := range []string{"photoId", "photo_id"} {
			v := u.Query().Get(key)
			if regexp.MustCompile(`^[a-zA-Z0-9_-]{8,40}$`).MatchString(v) {
				return v
			}
		}
	}
	return ""
}
func platformAccessError(name string) error {
	return fmt.Errorf("%s没有向当前公开请求提供目标视频的播放信息，可能需要登录、验证或作品已不可用。可开启资源捕获，在浏览器正常播放后下载捕获的视频流", name)
}

// Initial state is data, never executable code. Only replace unquoted undefined
// tokens; a user's title containing that word must remain byte-for-byte intact.
func initialJSON(text []byte) any {
	out := make([]byte, 0, len(text))
	quote := byte(0)
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' {
			quote = c
			out = append(out, c)
			continue
		}
		if i+9 <= len(text) && string(text[i:i+9]) == "undefined" {
			before := i == 0 || !initialIdentifier(text[i-1])
			after := i+9 == len(text) || !initialIdentifier(text[i+9])
			if before && after {
				out = append(out, []byte("null")...)
				i += 8
				continue
			}
		}
		out = append(out, c)
	}
	return decodeDouyinJSON(out)
}
func initialIdentifier(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$'
}
func publicPageData(body []byte) []any {
	data := douyinPageData(body)
	for _, script := range douyinScript.FindAllSubmatch(body, -1) {
		for _, match := range publicInitialAssignment.FindAllIndex(script[2], -1) {
			if v := initialJSON(script[2][match[1]:]); v != nil {
				data = append(data, v)
			}
		}
	}
	return data
}
func nestedMap(root map[string]any, keys ...string) map[string]any {
	current := root
	for _, key := range keys {
		current, _ = current[key].(map[string]any)
		if current == nil {
			return nil
		}
	}
	return current
}

func platformChoiceID(f map[string]any) string {
	if id := douyinText(f["_public_choice_id"]); id != "" {
		return id
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%g|%g|%g|%g|%s", f["_quality"], douyinNumber(f["width"]), douyinNumber(f["height"]), douyinNumber(f["fps"]), douyinNumber(f["tbr"]), douyinText(f["vcodec"]))))
	return fmt.Sprintf("pf:%x", sum[:10])
}
func publicFormats(v *douyinPublicVideo) []MediaFormat {
	formats := v.Ladder
	if len(formats) == 0 {
		formats = v.Formats
	}
	out := []MediaFormat{}
	seen := map[string]bool{}
	for _, f := range orderedDouyinFormats(formats) {
		id := platformChoiceID(f)
		if seen[id] {
			continue
		}
		seen[id] = true
		w, h := int(douyinNumber(f["width"])), int(douyinNumber(f["height"]))
		label := douyinText(f["_quality"])
		if label == "" {
			label = "播放版本"
		}
		if w > 0 && h > 0 {
			label = fmt.Sprintf("%d×%d · %s", w, h, label)
		}
		fps, rate := douyinNumber(f["fps"]), douyinNumber(f["tbr"])
		if fps > 0 {
			label += fmt.Sprintf(" · %g fps", fps)
		}
		if rate > 0 {
			label += fmt.Sprintf(" · %.0f kbps", rate)
		}
		reason := douyinText(f["_douyin_unavailable"])
		out = append(out, MediaFormat{ID: id, Label: label, Width: w, Height: h, FPS: fps, Bitrate: rate, Size: int64(max(douyinNumber(f["filesize"]), douyinNumber(f["filesize_approx"]))), Approx: douyinNumber(f["filesize"]) <= 0, Available: reason == "", Reason: reason})
	}
	return out
}
func publicFormat(v *douyinPublicVideo, meta map[string]any, urls []any, quality, codec string, defaultWidth, defaultHeight float64) {
	width, height := douyinFirstNumber(meta["width"], meta["Width"], defaultWidth), douyinFirstNumber(meta["height"], meta["Height"], defaultHeight)
	rate := douyinFirstNumber(meta["avgBitrate"], meta["bitrate"], meta["videoBitrate"]) / 1000
	for _, candidate := range urls {
		raw := html.UnescapeString(douyinText(candidate))
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		if !publicMediaURL(raw) {
			continue
		}
		f := map[string]any{"format_id": fmt.Sprintf("public-%d", len(v.Formats)), "url": raw, "ext": "mp4", "vcodec": codec, "acodec": "aac", "width": width, "height": height, "fps": douyinFirstNumber(meta["fps"], meta["frameRate"]), "tbr": rate, "filesize_approx": douyinFirstNumber(meta["size"], meta["fileSize"]), "_quality": quality, "http_headers": map[string]string{"User-Agent": v.UserAgent, "Referer": v.Source}}
		if douyinFlag(meta["is_drm"]) || douyinNumber(meta["drmType"]) > 0 {
			f["_douyin_unavailable"] = "受保护的视频流"
		}
		if strings.ToLower(douyinText(meta["audioCodec"])) == "none" {
			f["_douyin_unavailable"] = "此档位未提供音轨"
		}
		f["_public_choice_id"] = platformChoiceID(f)
		v.Formats = append(v.Formats, f)
	}
}
func publicXHSVideo(data any, id, source string) *douyinPublicVideo {
	root, _ := data.(map[string]any)
	note := nestedMap(root, "note", "noteDetailMap", id, "note")
	if note == nil {
		return nil
	}
	if noteID := douyinText(note["noteId"]); noteID != "" && !strings.EqualFold(noteID, id) {
		return nil
	}
	video := nestedMap(note, "video")
	if video == nil {
		return nil
	}
	title := douyinText(note["title"])
	if title == "" {
		title = douyinText(note["desc"])
	}
	if title == "" {
		title = "小红书视频 " + id
	}
	v := &douyinPublicVideo{ID: id, Title: title, Source: source, UserAgent: publicPlatformAgent, Size: -1}
	if user := nestedMap(note, "user"); user != nil {
		v.Author = douyinText(user["nickname"])
	}
	if images, ok := note["imageList"].([]any); ok && len(images) > 0 {
		if cover, ok := images[0].(map[string]any); ok {
			v.Thumbnail = douyinText(cover["urlDefault"])
			if v.Thumbnail == "" {
				v.Thumbnail = douyinText(cover["urlPre"])
			}
		}
	}
	streams := nestedMap(video, "media", "stream")
	for codec, entries := range streams {
		list, _ := entries.([]any)
		for _, entry := range list {
			meta, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			urls := []any{meta["masterUrl"]}
			if backups, ok := meta["backupUrls"].([]any); ok {
				urls = append(urls, backups...)
			}
			quality := douyinText(meta["qualityType"])
			c := douyinText(meta["videoCodec"])
			if c == "" {
				c = codec
			}
			publicFormat(v, meta, urls, quality, c, 0, 0)
			v.Duration = max(v.Duration, douyinNumber(meta["duration"])/1000)
		}
	}
	if len(v.Formats) == 0 {
		return nil
	}
	return v
}

func publicKSItem(item map[string]any, id, source string) *douyinPublicVideo {
	match := false
	for _, key := range []string{"photoId", "photo_id", "id"} {
		if douyinText(item[key]) == id {
			match = true
			break
		}
	}
	if !match {
		return nil
	}
	if douyinFlag(item["isPrivate"]) || douyinFlag(item["is_drm"]) {
		return nil
	}
	title := douyinText(item["caption"])
	if title == "" {
		title = douyinText(item["title"])
	}
	if title == "" {
		title = "快手视频 " + id
	}
	v := &douyinPublicVideo{ID: id, Title: title, Source: source, UserAgent: publicPlatformAgent, Size: -1, Thumbnail: douyinText(item["coverUrl"]), Duration: douyinFirstNumber(item["duration"], item["videoDuration"]) / 1000}
	if v.Thumbnail == "" {
		v.Thumbnail = douyinText(item["cover"])
	}
	v.Author = douyinText(item["userName"])
	if user := nestedMap(item, "user"); user != nil && v.Author == "" {
		v.Author = douyinText(user["name"])
	}
	width, height := douyinFirstNumber(item["width"], item["videoWidth"]), douyinFirstNumber(item["height"], item["videoHeight"])
	for _, key := range []string{"photoUrl", "videoUrl", "playUrl"} {
		if raw := douyinText(item[key]); raw != "" {
			publicFormat(v, item, []any{raw}, "当前播放版本", "h264", width, height)
		}
	}
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > 12 {
			return
		}
		switch obj := value.(type) {
		case map[string]any:
			var urls []any
			for _, key := range []string{"url", "masterUrl"} {
				if raw := douyinText(obj[key]); raw != "" {
					urls = append(urls, raw)
				}
			}
			if list, ok := obj["urls"].([]any); ok {
				for _, entry := range list {
					if m, ok := entry.(map[string]any); ok {
						urls = append(urls, m["url"])
					} else {
						urls = append(urls, entry)
					}
				}
			}
			if len(urls) > 0 {
				quality := douyinText(obj["qualityType"])
				codec := douyinText(obj["videoCodec"])
				if codec == "" {
					codec = "h264"
				}
				publicFormat(v, obj, urls, quality, codec, width, height)
			}
			for _, nested := range obj {
				walk(nested, depth+1)
			}
		case []any:
			for _, nested := range obj {
				walk(nested, depth+1)
			}
		}
	}
	for _, key := range []string{"videoResource", "representation", "manifest"} {
		walk(item[key], 0)
	}
	if len(v.Formats) == 0 {
		return nil
	}
	return v
}
func findPublicKSVideo(value any, id, source string, depth int) *douyinPublicVideo {
	if depth > 32 {
		return nil
	}
	switch obj := value.(type) {
	case map[string]any:
		if v := publicKSItem(obj, id, source); v != nil {
			return v
		}
		for key, nested := range obj {
			if strings.HasPrefix(key, "Photo:") && strings.TrimPrefix(key, "Photo:") == id {
				if m, ok := nested.(map[string]any); ok {
					copy := map[string]any{"photoId": id}
					for k, v := range m {
						copy[k] = v
					}
					if v := publicKSItem(copy, id, source); v != nil {
						return v
					}
				}
			}
			if v := findPublicKSVideo(nested, id, source, depth+1); v != nil {
				return v
			}
		}
	case []any:
		for _, nested := range obj {
			if v := findPublicKSVideo(nested, id, source, depth+1); v != nil {
				return v
			}
		}
	}
	return nil
}

func (e *Engine) extractPublicPlatform(ctx context.Context, raw string) (*douyinPublicVideo, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	u, _ := url.Parse(raw)
	if !isPublicPlatformPage(u) || u.User != nil {
		return nil, errors.New("请输入快手或小红书单个视频链接")
	}
	name := platformName("", raw)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: e.transport, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 6 || !isPublicPlatformPage(req.URL) || req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
			return errors.New("分享链接跳转到不支持的页面")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", publicPlatformAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New(name + "公开页面请求失败，请检查网络或重新复制分享链接")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s页面返回 HTTP %d", name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return nil, errors.New(name + "页面数据无效")
	}
	target := resp.Request.URL
	id := publicPlatformID(target)
	if id == "" {
		if publicPlatformID(u) != "" {
			return nil, platformAccessError(name)
		}
		return nil, errors.New("请粘贴" + name + "单个视频的分享链接，主页、直播和图文暂不支持")
	}
	expected := publicPlatformID(u)
	if expected != "" && !strings.EqualFold(expected, id) {
		return nil, errors.New("分享链接返回的作品与请求不一致")
	}
	// Preserve the redirect's exact query, including xsec_token. Rebuilding a
	// canonical URL without it would invalidate otherwise accessible XHS shares.
	source := target.String()
	for _, data := range publicPageData(body) {
		var v *douyinPublicVideo
		if hostIn(target.Hostname(), "xiaohongshu.com") {
			v = publicXHSVideo(data, id, source)
		} else {
			v = findPublicKSVideo(data, id, source, 0)
		}
		if v != nil {
			if err := e.verifyPublicPlatform(ctx, v, ""); err != nil {
				return nil, err
			}
			return v, nil
		}
	}
	return nil, platformAccessError(name)
}
func (e *Engine) verifyPublicPlatform(ctx context.Context, v *douyinPublicVideo, choice string) error {
	original := v.Formats
	if len(v.Ladder) > 0 {
		original = v.Ladder
	}
	candidates := []map[string]any{}
	for _, f := range original {
		if choice == "" || choice == "best" || platformChoiceID(f) == choice {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		return errors.New("所选画质已失效，未更换为其他档位")
	}
	v.Formats = candidates
	if err := e.verifyDouyinMedia(ctx, v); err != nil {
		return errors.New("此视频播放地址暂不可用或不是有效 MP4，未更换画质")
	}
	v.Ladder = original
	v.Quality = strings.ReplaceAll(v.Quality, "最高可用原流", "可用播放原流")
	return nil
}
func (e *Engine) resolvePublicPlatformVideo(raw string, headers http.Header) (*Resource, error) {
	ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer cancel()
	v, err := e.extractPublicPlatform(ctx, raw)
	if err != nil {
		return nil, err
	}
	requestHeaders := http.Header{"User-Agent": {v.UserAgent}, "Referer": {v.Source}}
	r := e.remember(v.Source, requestHeaders, "video/mp4", v.Size, "", true)
	if r == nil {
		return nil, errors.New("资源列表已满")
	}
	e.mu.Lock()
	r.Kind, r.Type, r.MIME = "page", "video", "video/mp4"
	r.Name = safeFilename(v.Title + ".mp4")
	if r.NameOverride != "" {
		r.Name = r.NameOverride
	}
	r.Site = platformName("", v.Source)
	r.Size, r.Duration, r.Thumbnail, r.Author, r.Quality = v.Size, v.Duration, v.Thumbnail, v.Author, v.Quality
	r.Width, r.Height = int(douyinNumber(v.Formats[0]["width"])), int(douyinNumber(v.Formats[0]["height"]))
	r.FPS, r.Bitrate = douyinNumber(v.Formats[0]["fps"]), douyinNumber(v.Formats[0]["tbr"])
	r.Formats = publicFormats(v)
	copy := *r
	e.mu.Unlock()
	return &copy, nil
}
func (e *Engine) publicPlatformDownloadInput(ctx context.Context, r *Resource, staging string) ([]string, error) {
	v, err := e.extractPublicPlatform(ctx, r.URL)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(r.SelectedFormat, "pf:") {
		if err = e.verifyPublicPlatform(ctx, v, r.SelectedFormat); err != nil {
			return nil, err
		}
	}
	info := v.extractorInfo()
	info["extractor"] = "PublicPlatform"
	info["extractor_key"] = platformName("", v.Source)
	metadata := filepath.Join(staging, "source.info.json")
	if !pathInside(staging, metadata) {
		return nil, errors.New("视频任务目录无效")
	}
	b, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	if err = atomicWrite(metadata, b, 0600); err != nil {
		return nil, errors.New("保存平台播放信息失败")
	}
	e.updateDouyinSource(r, v.Size, int(douyinNumber(v.Formats[0]["width"])), int(douyinNumber(v.Formats[0]["height"])), v.Quality)
	return []string{"--load-info-json", metadata}, nil
}
