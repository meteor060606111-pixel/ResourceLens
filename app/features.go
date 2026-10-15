package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const appVersion = "1.6.3"

type MediaFormat struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Width     int     `json:"width,omitempty"`
	Height    int     `json:"height,omitempty"`
	FPS       float64 `json:"fps,omitempty"`
	Bitrate   float64 `json:"bitrate,omitempty"`
	Size      int64   `json:"size,omitempty"`
	Approx    bool    `json:"approx,omitempty"`
	Available bool    `json:"available"`
	Reason    string  `json:"reason,omitempty"`
	Selector  string  `json:"selector,omitempty"`
}

func platformName(name, raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return name
	}
	switch {
	case hostIn(u.Hostname(), "bilibili.com", "b23.tv"):
		return "哔哩哔哩"
	case hostIn(u.Hostname(), "douyin.com", "iesdouyin.com"):
		return "抖音"
	case hostIn(u.Hostname(), "kuaishou.com", "kwai.com"):
		return "快手"
	case hostIn(u.Hostname(), "xiaohongshu.com", "xhslink.com"):
		return "小红书"
	case wechatMediaURL(raw) || isWeChatPage(u):
		return "微信视频号"
	}
	if name != "" {
		return name
	}
	return u.Hostname()
}

var formatIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,160}$`)

func formatsFromVideoInfo(info videoInfo) []MediaFormat {
	list := []MediaFormat{}
	for _, f := range info.Formats {
		if f.VCodec == "" || f.VCodec == "none" || !formatIDPattern.MatchString(f.ID) {
			continue
		}
		selector := f.ID
		if f.ACodec == "none" || f.ACodec == "" {
			selector += "+bestaudio"
		}
		label := fmt.Sprintf("%d×%d", f.Width, f.Height)
		if f.Width == 0 || f.Height == 0 {
			label = f.Note
			if label == "" {
				label = f.ID
			}
		}
		if f.FPS > 0 {
			label += fmt.Sprintf(" · %.0f fps", f.FPS)
		}
		if f.Bitrate > 0 {
			label += fmt.Sprintf(" · %.0f kbps", f.Bitrate)
		}
		list = append(list, MediaFormat{ID: "fmt:" + f.ID, Label: label, Width: f.Width, Height: f.Height, FPS: f.FPS, Bitrate: f.Bitrate, Size: int64(max(f.Size, f.Approx)), Approx: f.Size <= 0 || f.ACodec == "none" || f.ACodec == "", Available: true, Selector: selector})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Width*a.Height != b.Width*b.Height {
			return a.Width*a.Height > b.Width*b.Height
		}
		if a.FPS != b.FPS {
			return a.FPS > b.FPS
		}
		return a.Bitrate > b.Bitrate
	})
	return list
}
func validateFormatChoice(r *Resource, choice string) error {
	if choice == "best" || choice == "" {
		return nil
	}
	if r.WechatVideo && (choice == "source" || choice == "playback") {
		return nil
	}
	for _, f := range r.Formats {
		if f.ID == choice {
			if !f.Available {
				return fmt.Errorf("此画质不可获取：%s", f.Reason)
			}
			return nil
		}
	}
	return errors.New("该画质档位已失效，请重新查询可用画质")
}
func selectedVideoSelector(r *Resource) string {
	for _, f := range r.Formats {
		if f.ID == r.SelectedFormat {
			return f.Selector
		}
	}
	return ""
}
func (e *Engine) resourceFormats(id string) ([]MediaFormat, error) {
	e.mu.RLock()
	r := e.resources[id]
	if r == nil {
		e.mu.RUnlock()
		return nil, errors.New("资源不存在")
	}
	copy := *r
	copy.privateHeaders = r.privateHeaders.Clone()
	e.mu.RUnlock()
	if copy.CaptureRequired {
		return nil, errors.New("先播放目标视频，等待完整视频信息")
	}
	if copy.WechatVideo {
		ctx, cancel := context.WithTimeout(e.ctx, 70*time.Second)
		defer cancel()
		base := copy.WechatPlaybackURL
		if base == "" {
			base = copy.URL
		}
		candidates := []struct{ id, label, raw string }{{"wx:playback", "当前播放版本", base}}
		if s := wechatSourceURL(base); s != "" {
			candidates = append(candidates, struct{ id, label, raw string }{"wx:source", "平台标注版本", s})
		}
		for _, f := range copy.WechatFormats {
			candidates = append(candidates, struct{ id, label, raw string }{"wx:spec:" + f.ID, f.ID, wechatFormatURL(base, f.ID)})
		}
		fs := []MediaFormat{}
		for _, c := range candidates {
			if ctx.Err() != nil {
				break
			}
			v, err := e.probeWechatVariant(ctx, &copy, c.raw, c.label)
			f := MediaFormat{ID: c.id, Label: c.label, Width: v.Width, Height: v.Height, Size: v.Size, Available: err == nil}
			if err != nil {
				f.Reason = err.Error()
			} else {
				if v.Width > 0 {
					f.Label = fmt.Sprintf("%d×%d · %s", v.Width, v.Height, c.label)
				}
				if c.id == "wx:source" && copy.WechatSourceSize > 0 && v.Size != copy.WechatSourceSize {
					f.Available = false
					f.Reason = "实际大小与平台标注不一致"
				}
			}
			fs = append(fs, f)
		}
		e.mu.Lock()
		if original := e.resources[id]; original != nil {
			original.Formats = fs
		}
		e.mu.Unlock()
		e.saveSession()
		return fs, nil
	}
	if len(copy.Formats) > 0 {
		return copy.Formats, nil
	}
	if copy.Kind == "page" || copy.Kind == "playlist" {
		r, err := e.resolveVideo(copy.URL, copy.privateHeaders)
		if err != nil {
			return nil, err
		}
		return r.Formats, nil
	}
	return []MediaFormat{{ID: "best", Label: copy.Quality, Width: copy.Width, Height: copy.Height, FPS: copy.FPS, Size: copy.Size, Available: true}}, nil
}

type ParseItem struct {
	URL        string `json:"url"`
	Status     string `json:"status"`
	ResourceID string `json:"resourceId,omitempty"`
	Error      string `json:"error,omitempty"`
}
type ParseBatch struct {
	ID        string      `json:"id"`
	CreatedAt time.Time   `json:"createdAt"`
	Items     []ParseItem `json:"items"`
	Status    string      `json:"status"`
	cancel    context.CancelFunc
}

func (e *Engine) startParseBatch(urls []string) (string, error) {
	if len(urls) == 0 || len(urls) > 100 {
		return "", errors.New("每批可添加 1–100 条链接")
	}
	items := []ParseItem{}
	seen := map[string]bool{}
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if len(raw) > 8192 {
			return "", errors.New("单条链接过长")
		}
		if raw != "" && !seen[raw] {
			seen[raw] = true
			items = append(items, ParseItem{URL: raw, Status: "queued"})
		}
	}
	if len(items) == 0 {
		return "", errors.New("没有有效链接")
	}
	id := fmt.Sprintf("batch-%x", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(e.ctx)
	batch := &ParseBatch{ID: id, CreatedAt: time.Now(), Items: items, Status: "parsing", cancel: cancel}
	e.mu.Lock()
	if e.batches == nil {
		e.batches = map[string]*ParseBatch{}
	}
	for _, b := range e.batches {
		if b.Status == "parsing" {
			e.mu.Unlock()
			cancel()
			return "", errors.New("上一批链接还在解析，请等待或停止后再添加")
		}
	}
	if len(e.batches) > 8 {
		var oldest *ParseBatch
		for _, b := range e.batches {
			if oldest == nil || b.CreatedAt.Before(oldest.CreatedAt) {
				oldest = b
			}
		}
		delete(e.batches, oldest.ID)
	}
	e.batches[id] = batch
	e.workers.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.workers.Done()
		defer cancel()
		for i := range items {
			e.mu.Lock()
			if ctx.Err() != nil {
				for n := i; n < len(batch.Items); n++ {
					batch.Items[n].Status = "cancelled"
				}
				e.mu.Unlock()
				break
			}
			batch.Items[i].Status = "parsing"
			raw := batch.Items[i].URL
			e.mu.Unlock()
			r, err := e.addURL(raw)
			e.mu.Lock()
			if err != nil {
				batch.Items[i].Status = "failed"
				batch.Items[i].Error = err.Error()
			} else {
				batch.Items[i].Status = "ready"
				batch.Items[i].ResourceID = r.ID
			}
			e.mu.Unlock()
			e.saveSession()
		}
		e.mu.Lock()
		batch.Status = "completed"
		if ctx.Err() != nil {
			batch.Status = "cancelled"
		}
		batch.cancel = nil
		e.mu.Unlock()
	}()
	return id, nil
}
func downloadDestination(cfg Config, r *Resource) (string, string, error) {
	dir := cfg.DownloadDir
	platform := platformName(r.Site, r.URL)
	author := r.Author
	if author == "" {
		author = "未标注作者"
	}
	switch cfg.ClassifyBy {
	case "platform":
		dir = filepath.Join(dir, safeFilename(platform))
	case "author":
		dir = filepath.Join(dir, safeFilename(platform), safeFilename(author))
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	template := cfg.NameTemplate
	if template == "" {
		template = "{title}"
	}
	ext := filepath.Ext(r.Name)
	title := strings.TrimSuffix(r.Name, ext)
	name := strings.NewReplacer("{title}", title, "{author}", author, "{platform}", platform, "{date}", time.Now().Format("20060102")).Replace(template)
	name = safeFilename(name)
	if ext != "" && !strings.EqualFold(filepath.Ext(name), ext) {
		name += ext
	}
	return dir, name, nil
}
func promoteFile(from, dir, name string) (string, error) {
	name = safeFilename(name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 0; n < 10000; n++ {
		candidate := name
		if n > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		final := filepath.Join(dir, candidate)
		if _, err := os.Stat(final); err == nil {
			continue
		}
		if err := commitDownload(from, final); err != nil {
			if _, exists := os.Stat(final); exists == nil {
				continue
			}
			return "", err
		}
		return final, nil
	}
	return "", errors.New("无法分配文件名")
}
func (e *Engine) renameItems(ids []string, template, mode string) (int, error) {
	if len(ids) == 0 || len(ids) > 250 || len(template) == 0 || len(template) > 240 {
		return 0, errors.New("选择项目并输入有效命名模板")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if mode != "resources" && mode != "jobs" {
		return 0, errors.New("重命名类型无效")
	}
	type renamePlan struct {
		r          *Resource
		j          *Job
		name, path string
	}
	plans := []renamePlan{}
	reserved := map[string]bool{}
	seen := map[string]bool{}
	for i, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var name string
		var r *Resource
		var j *Job
		if mode == "jobs" {
			j = e.jobs[id]
			if j == nil || j.Status != "completed" {
				return 0, errors.New("只能重命名已完成的文件")
			}
			if info, err := os.Stat(j.File); err != nil || !info.Mode().IsRegular() {
				return 0, errors.New("待重命名文件不存在或不是普通文件")
			}
			r = e.resources[j.ResourceID]
			name = j.Name
		} else {
			r = e.resources[id]
			if r == nil {
				return 0, errors.New("资源不存在")
			}
			name = r.Name
		}
		ext := filepath.Ext(name)
		author, platform := "未标注作者", "未标注平台"
		if r != nil {
			if r.Author != "" {
				author = r.Author
			}
			platform = platformName(r.Site, r.URL)
		}
		next := safeFilename(strings.NewReplacer("{title}", strings.TrimSuffix(name, ext), "{author}", author, "{platform}", platform, "{index}", fmt.Sprintf("%03d", i+1), "{date}", time.Now().Format("20060102")).Replace(template))
		if !strings.EqualFold(filepath.Ext(next), ext) {
			next += ext
		}
		if mode == "jobs" {
			dest := filepath.Join(filepath.Dir(j.File), next)
			stem := strings.TrimSuffix(next, ext)
			for n := 1; !strings.EqualFold(dest, j.File); n++ {
				_, err := os.Stat(dest)
				if os.IsNotExist(err) && !reserved[strings.ToLower(dest)] {
					break
				}
				if err != nil && !os.IsNotExist(err) {
					return 0, err
				}
				if n > 9999 {
					return 0, errors.New("无法分配新文件名")
				}
				next = fmt.Sprintf("%s (%d)%s", stem, n, ext)
				dest = filepath.Join(filepath.Dir(j.File), next)
			}
			reserved[strings.ToLower(dest)] = true
			plans = append(plans, renamePlan{r: r, j: j, name: next, path: dest})
		} else {
			plans = append(plans, renamePlan{r: r, name: next})
		}
	}
	count := 0
	for _, plan := range plans {
		if plan.j != nil {
			if plan.path != plan.j.File {
				if err := os.Rename(plan.j.File, plan.path); err != nil {
					return count, fmt.Errorf("已处理 %d 项，其余未改动：%w", count, err)
				}
			}
			plan.j.File, plan.j.Name = plan.path, plan.name
		} else {
			plan.r.Name, plan.r.NameOverride = plan.name, plan.name
		}
		count++
	}
	return count, nil
}
func (e *Engine) clearJobHistory(ids []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range ids {
		j := e.jobs[id]
		if j != nil {
			if activeStatus(j.Status) || j.Status == "paused" {
				return errors.New("运行中或暂停的任务需先取消，才能移除记录")
			}
		}
	}
	for _, id := range ids {
		delete(e.jobs, id)
	}
	return nil
}
func (e *Engine) cleanCache(remove bool) (map[string]any, error) {
	e.previewMu.Lock()
	defer e.previewMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	protected := map[string]bool{}
	roots := map[string]bool{e.config.DownloadDir: true}
	for _, j := range e.jobs {
		cfg := e.config
		if j.OutputDir != "" {
			cfg.DownloadDir = j.OutputDir
			roots[j.OutputDir] = true
		}
		if activeStatus(j.Status) || j.Status == "paused" || j.ResumeAvailable {
			protected[downloadCacheDir(cfg, j.ResourceID, j.RequestedQuality)] = true
		}
	}
	var bytes int64
	count := 0
	for dir := range roots {
		root := filepath.Join(dir, ".resourcelens-temp")
		if !ownedDirectory(dir, root) {
			continue
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			target := filepath.Join(root, entry.Name())
			if protected[target] || !entry.IsDir() || !regexp.MustCompile(`^[a-f0-9]{24}(-[a-f0-9]{12})?$`).MatchString(entry.Name()) {
				continue
			}
			info, err := os.Lstat(target)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if !ownedDirectory(root, target) {
				continue
			}
			filepath.WalkDir(target, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if !d.IsDir() {
					if info, err := d.Info(); err == nil {
						bytes += info.Size()
					}
				}
				return nil
			})
			count++
			if remove {
				if !pathInside(root, target) {
					return nil, errors.New("缓存目录校验失败")
				}
				if err := os.RemoveAll(target); err != nil {
					return nil, err
				}
			}
		}
		if remove {
			os.Remove(root)
		}
	}
	previewDir := filepath.Join(e.root, "data", "preview-cache")
	for _, ticket := range e.mediaTickets {
		if time.Now().Before(ticket.Expires) {
			protected[ticket.File] = true
		}
	}
	for _, file := range e.previewCacheEntries() {
		target := filepath.Join(previewDir, file.Name())
		if protected[target] || time.Since(file.ModTime()) < 15*time.Minute {
			continue
		}
		bytes += file.Size()
		count++
		if remove {
			if err := os.Remove(target); err != nil {
				return nil, err
			}
		}
	}
	removedBytes := int64(0)
	remaining := bytes
	if remove {
		removedBytes = bytes
		remaining = 0
	}
	return map[string]any{"bytes": remaining, "folders": count, "removed": remove, "removedBytes": removedBytes}, nil
}
func mediaGroup(raw, referer string) string {
	identity := referer
	if identity == "" {
		identity = raw
	}
	u, err := url.Parse(identity)
	if err == nil {
		q := u.Query()
		for key := range q {
			if strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "sign") {
				q.Del(key)
			}
		}
		u.RawQuery = q.Encode()
		identity = u.String()
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:8])
}
