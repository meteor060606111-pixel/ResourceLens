package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type WechatFormat struct {
	ID     string `json:"id"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

func wechatFormats(value any) []WechatFormat {
	items, _ := value.([]any)
	formats := []WechatFormat{}
	seen := map[string]bool{}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := wechatValue(object["fileFormat"])
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") || seen[id] {
			continue
		}
		seen[id] = true
		width, _ := strconv.Atoi(wechatValue(object["width"]))
		height, _ := strconv.Atoi(wechatValue(object["height"]))
		size, _ := strconv.ParseInt(wechatValue(object["fileSize"]), 10, 64)
		formats = append(formats, WechatFormat{id, width, height, size})
		if len(formats) >= 12 {
			break
		}
	}
	// Preserve the server's order when dimensions are absent; the original client
	// also treats the first declared spec as its highest declared playback tier.
	allDimensions := true
	for _, format := range formats {
		if format.Width <= 0 || format.Height <= 0 {
			allDimensions = false
			break
		}
	}
	if allDimensions {
		sort.SliceStable(formats, func(i, j int) bool {
			return int64(formats[i].Width)*int64(formats[i].Height) > int64(formats[j].Width)*int64(formats[j].Height)
		})
	}
	return formats
}

func wechatSourceURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !wechatMediaURL(raw) || u.Query().Get("encfilekey") == "" || u.Query().Get("token") == "" {
		return ""
	}
	parts := []string{}
	for _, part := range strings.Split(u.RawQuery, "&") {
		name, _, _ := strings.Cut(part, "=")
		if name == "encfilekey" || name == "token" {
			parts = append(parts, part)
		}
	}
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}

func wechatFormatURL(raw, format string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := []string{}
	for _, part := range strings.Split(u.RawQuery, "&") {
		name, _, _ := strings.Cut(part, "=")
		if !strings.EqualFold(name, "X-snsvideoflag") {
			parts = append(parts, part)
		}
	}
	parts = append(parts, "X-snsvideoflag="+url.QueryEscape(format))
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}

type wechatVariant struct {
	URL, Label    string
	Size          int64
	Width, Height int
	Source        bool
}

func (e *Engine) probeWechatVariant(ctx context.Context, resource *Resource, raw, label string) (wechatVariant, error) {
	variant := wechatVariant{URL: raw, Label: label, Size: -1}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	copy := *resource
	copy.URL = raw
	request, err := downloadRequest(requestCtx, "GET", &copy)
	if err != nil {
		return variant, err
	}
	request.Header.Set("Range", "bytes=0-32767")
	response, err := (&http.Client{Transport: e.transport}).Do(request)
	if err != nil {
		return variant, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 206 || htmlMIME(response.Header.Get("Content-Type")) {
		return variant, fmt.Errorf("画质地址返回 HTTP %d", response.StatusCode)
	}
	if response.StatusCode == 206 {
		start, _, total, ok := parseContentRange(response.Header.Get("Content-Range"))
		if !ok || start != 0 {
			return variant, errors.New("画质地址的分段范围无效")
		}
		variant.Size = total
	} else {
		variant.Size = response.ContentLength
	}
	prefix, err := io.ReadAll(io.LimitReader(response.Body, 32768))
	if err != nil || len(prefix) < 12 {
		return variant, errors.New("未取得有效的视频文件头")
	}
	if !bytes.Equal(prefix[4:8], []byte("ftyp")) {
		mask, err := wechatDecodeMask(resource.wechatSeed)
		if err != nil {
			return variant, err
		}
		for index := range prefix {
			prefix[index] ^= mask[index]
		}
	}
	if !bytes.Equal(prefix[4:8], []byte("ftyp")) {
		return variant, errors.New("此画质地址不能解码为 MP4")
	}
	variant.Width, variant.Height = wechatMP4Dimensions(prefix, 0)
	return variant, nil
}

func wechatMP4Dimensions(data []byte, depth int) (int, int) {
	if depth > 3 {
		return 0, 0
	}
	for offset := 0; offset+8 <= len(data); {
		size := uint64(binary.BigEndian.Uint32(data[offset:]))
		header := 8
		if size == 1 {
			if offset+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[offset+8:])
			header = 16
		}
		if size == 0 {
			size = uint64(len(data) - offset)
		}
		if size < uint64(header) {
			break
		}
		end := min(uint64(len(data)), uint64(offset)+size)
		switch string(data[offset+4 : offset+8]) {
		case "moov", "trak":
			if width, height := wechatMP4Dimensions(data[offset+header:int(end)], depth+1); width > 0 && height > 0 {
				return width, height
			}
		case "tkhd":
			if uint64(offset)+size <= uint64(len(data)) && size >= uint64(header+8) {
				tail := int(uint64(offset)+size) - 8
				width := int(binary.BigEndian.Uint32(data[tail:]) >> 16)
				height := int(binary.BigEndian.Uint32(data[tail+4:]) >> 16)
				if width > 0 && height > 0 && width <= 16384 && height <= 16384 {
					return width, height
				}
			}
		}
		if size > uint64(len(data)-offset) {
			break
		}
		offset += int(size)
	}
	return 0, 0
}

func (e *Engine) prepareWechatDownload(ctx context.Context, job *Job, resource *Resource, quality string) error {
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	base := resource.WechatPlaybackURL
	if base == "" {
		base = resource.URL
	}
	if resource.WechatSourceSize <= 0 {
		resource.WechatSourceSize = resource.Size
	}
	wanted := wechatQualityLabel(quality)
	e.mu.Lock()
	job.Quality = wanted
	job.Notice = "正在确认画质及实际文件大小"
	e.mu.Unlock()
	candidates := []struct{ URL, Label string }{}
	if strings.HasPrefix(quality, "wx:") {
		selectedURL, label := "", "指定画质"
		switch quality {
		case "wx:playback":
			selectedURL, label = base, "当前播放版本"
		case "wx:source":
			selectedURL, label = wechatSourceURL(base), "平台标注版本"
		default:
			if strings.HasPrefix(quality, "wx:spec:") {
				for _, f := range resource.WechatFormats {
					if "wx:spec:"+f.ID == quality {
						selectedURL, label = wechatFormatURL(base, f.ID), f.ID
						break
					}
				}
			}
		}
		if selectedURL == "" {
			return errors.New("此画质档位已失效，请重新查询")
		}
		v, err := e.probeWechatVariant(ctx, resource, selectedURL, label)
		if err != nil {
			return fmt.Errorf("选定档位无法获取，未更换画质：%w", err)
		}
		if quality == "wx:source" && resource.WechatSourceSize > 0 && v.Size != resource.WechatSourceSize {
			return errors.New("平台标注版本大小不符，未更换为其他版本")
		}
		if v.Width > 0 {
			label = fmt.Sprintf("%d×%d · %s", v.Width, v.Height, label)
		}
		resource.URL, resource.Size, resource.Quality = selectedURL, v.Size, label
		e.mu.Lock()
		job.URL, job.Total, job.Quality, job.Notice, job.Status = selectedURL, v.Size, label, "", "downloading"
		if existing := e.resources[resource.ID]; existing != nil {
			existing.URL, existing.Size, existing.Quality, existing.Width, existing.Height, existing.WechatResolvedQuality = selectedURL, v.Size, label, v.Width, v.Height, quality
		}
		e.mu.Unlock()
		return nil
	}
	if quality != "playback" {
		if original := wechatSourceURL(base); original != "" {
			candidates = append(candidates, struct{ URL, Label string }{original, "超清地址"})
		}
	}
	if quality != "playback" {
		for index, format := range resource.WechatFormats {
			if index >= 4 {
				break
			}
			candidates = append(candidates, struct{ URL, Label string }{wechatFormatURL(base, format.ID), "平台声明档位"})
		}
	}
	candidates = append(candidates, struct{ URL, Label string }{base, "播放版本"})
	variants := []wechatVariant{}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.URL == "" || seen[candidate.URL] {
			continue
		}
		seen[candidate.URL] = true
		variant, err := e.probeWechatVariant(ctx, resource, candidate.URL, candidate.Label)
		if parentCtx.Err() != nil {
			return parentCtx.Err()
		}
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			continue
		}
		variant.Source = resource.WechatSourceSize > 0 && variant.Size == resource.WechatSourceSize
		if quality == "source" && !variant.Source {
			variants = append(variants, variant)
			continue
		}
		variants = append(variants, variant)
		if variant.Source || quality == "playback" {
			break
		}
	}
	if len(variants) == 0 {
		return errors.New("无法取得可解码的此视频画质地址，请在微信重新播放后再下载")
	}
	if quality == "source" {
		found := false
		for _, variant := range variants {
			if variant.Source {
				variants = []wechatVariant{variant}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("未取得与平台标注大小一致的视频（平台标注 %.1f MB，当前可访问 %.1f MB）。已停止，未换成低清文件；可选择“最高可获取”或“播放版本”", float64(resource.WechatSourceSize)/1048576, float64(variants[0].Size)/1048576)
		}
	}
	if quality == "best" {
		sort.SliceStable(variants, func(i, j int) bool {
			a, b := variants[i], variants[j]
			if a.Source != b.Source {
				return a.Source
			}
			if a.Width*a.Height != b.Width*b.Height {
				return a.Width*a.Height > b.Width*b.Height
			}
			return a.Size > b.Size
		})
	}
	selected := variants[0]
	label := selected.Label
	if selected.Source {
		label = "平台标注大小已匹配"
	} else if quality == "best" {
		label = "最高可获取（平台标注版本未取得）"
	}
	if selected.Width > 0 && selected.Height > 0 {
		label = fmt.Sprintf("%d×%d · %s", selected.Width, selected.Height, label)
	}
	resource.URL, resource.Size, resource.Quality, resource.Width, resource.Height, resource.WechatResolvedQuality = selected.URL, selected.Size, label, selected.Width, selected.Height, quality
	e.mu.Lock()
	job.URL, job.Total, job.Quality, job.Notice = selected.URL, selected.Size, label, ""
	job.Status = "downloading"
	if existing := e.resources[resource.ID]; existing != nil {
		existing.URL, existing.Size, existing.Quality, existing.Width, existing.Height, existing.WechatResolvedQuality = selected.URL, selected.Size, label, selected.Width, selected.Height, quality
	}
	e.mu.Unlock()
	e.saveSession()
	return nil
}

func wechatQualityLabel(quality string) string {
	return map[string]string{"source": "匹配平台标注大小", "best": "最高可获取画质", "playback": "播放版本"}[quality]
}
