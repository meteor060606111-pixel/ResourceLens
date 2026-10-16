package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const videoConnectionAttempts = 3

func videoCacheHasData(dir string) bool {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, f := range files {
		if f.IsDir() || !strings.HasPrefix(f.Name(), "media.") || strings.HasSuffix(f.Name(), ".ytdl") {
			continue
		}
		if info, err := f.Info(); err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}
func videoNetworkError(details string) bool {
	text := strings.ToLower(details)
	for _, marker := range []string{"more expected", "incomplete", "unexpected eof", "timed out", "timeout", "connection reset", "connection aborted", "connection closed", "remote end closed", "unable to download video data", "temporary failure", "temporarily unavailable", "http error 429", "http error 500", "http error 502", "http error 503", "http error 504", "fragment not found"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(text, "[download]") && (strings.Contains(text, "http error 403") || strings.Contains(text, "http error 404"))
}
func (e *Engine) videoNotice(j *Job, status, notice string) {
	e.mu.Lock()
	j.Status, j.Notice = status, notice
	e.mu.Unlock()
}

// Monitor byte movement, not the frequency of progress messages. Only the first two
// attempts refresh a persistently slow connection; the last attempt keeps a slow but
// working transfer alive instead of making low-bandwidth connections fail repeatedly.
func (e *Engine) watchVideoConnection(ctx context.Context, cancel context.CancelFunc, j *Job, attempt int, stop <-chan struct{}, reason chan<- string, exited chan<- struct{}) {
	defer close(exited)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	begin, changed, sampleAt := time.Now(), time.Now(), time.Now()
	previous, sampleBytes, previousStatus := j.done.Load(), j.done.Load(), ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case now := <-tick.C:
			e.mu.RLock()
			status := j.Status
			e.mu.RUnlock()
			bytes := j.done.Load()
			if bytes != previous || status != previousStatus {
				changed = now
			}
			if status != previousStatus {
				sampleAt, sampleBytes = now, bytes
			}
			previous, previousStatus = bytes, status
			message := ""
			if status == "resolving" && now.Sub(begin) > 120*time.Second {
				message = "视频地址请求长时间未响应"
			} else if status == "downloading" && now.Sub(changed) > 45*time.Second {
				message = "连接长时间没有收到新数据"
			} else if status == "downloading" && now.Sub(sampleAt) >= 30*time.Second {
				rate := float64(max(0, bytes-sampleBytes)) / now.Sub(sampleAt).Seconds()
				sampleAt, sampleBytes = now, bytes
				threshold := float64(32 * 1024)
				if j.limiter != nil && j.limiter.bytesPerSecond > 0 {
					threshold = min(threshold, float64(j.limiter.bytesPerSecond)/4)
				}
				if rate < threshold {
					if attempt < videoConnectionAttempts {
						message = "当前连接持续低速"
					} else {
						e.mu.Lock()
						j.Notice = "来源服务器速度偏低，正在继续下载并保留进度"
						e.mu.Unlock()
					}
				}
			}
			if message != "" {
				select {
				case reason <- message:
				default:
				}
				cancel()
				return
			}
		}
	}
}

type streamTransfer struct{ done, total int64 }

func (e *Engine) downloadVideo(ctx context.Context, j *Job, r *Resource, cfg Config) error {
	dir, err := e.videoTools()
	if err != nil {
		return err
	}
	if !validResourceID(r.ID) {
		return errors.New("资源标识无效，请重新添加链接")
	}
	staging := downloadCacheDir(cfg, r.ID, j.RequestedQuality)
	if err = os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	succeeded, preserve := false, videoCacheHasData(staging)
	defer func() {
		e.mu.RLock()
		paused := j.pauseRequested
		e.mu.RUnlock()
		if !succeeded && (e.ctx.Err() != nil || paused || preserve && ctx.Err() == nil) && videoCacheHasData(staging) {
			e.mu.Lock()
			j.ResumeAvailable = true
			e.mu.Unlock()
			return
		}
		cleanupVideoStaging(cfg.DownloadDir, staging)
	}()
	if videoCacheHasData(staging) {
		e.videoNotice(j, "resolving", "检测到未完成的文件，将继续已有下载进度")
	} else {
		e.videoNotice(j, "resolving", "")
	}
	streams := map[string]streamTransfer{}
	var outputFile string
	for attempt := 1; attempt <= videoConnectionAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 1 {
			e.videoNotice(j, "retrying", fmt.Sprintf("正在重新连接（%d/%d），保留已有下载进度", attempt, videoConnectionAttempts))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
			e.mu.Lock()
			j.Status = "resolving"
			e.mu.Unlock()
		}
		outputFile = ""
		inputArgs, inputErr := e.videoDownloadInput(ctx, r, staging)
		if inputErr != nil {
			return inputErr
		}
		args := append(extractorArgs(dir, r.privateHeaders),
			"--retries", "5", "--fragment-retries", "5", "--extractor-retries", "2",
			"--retry-sleep", "http:exp=1:8", "--retry-sleep", "fragment:exp=1:8",
			"--http-chunk-size", "4M", "--continue", "--part", "--abort-on-unavailable-fragments",
			"--no-simulate", "--newline", "--progress", "--progress-delta", "0.5",
			"--concurrent-fragments", strconv.Itoa(min(8, cfg.Segments)), "--no-overwrites",
			"--merge-output-format", "mp4", "--remux-video", "mp4",
			"-o", "media.%(format_id)s.%(ext)s",
			"--print", "before_dl:RL_STREAMS:%(requested_formats)j",
			"--print", "before_dl:RL_QUALITY:%(width)s:%(height)s:%(fps)s",
			"--print", "after_move:RL_FILE:%(filepath)j",
			"--progress-template", "download:RL_PROGRESS:%(info.format_id)s:%(progress)j",
			"--progress-template", "postprocess:RL_POST:%(progress.status)s")
		if cfg.SpeedLimitKB > 0 {
			args = append(args, "--limit-rate", strconv.Itoa(cfg.SpeedLimitKB)+"K", "--concurrent-fragments", "1")
		}
		if selector := selectedVideoSelector(r); selector != "" {
			args = append(args, "-f", selector)
		}
		args = append(args, inputArgs...)
		stderr := &tailBuffer{limit: 64 << 10}
		lines := &lineWriter{onLine: func(line string) {
			if strings.HasPrefix(line, "RL_FILE:") {
				json.Unmarshal([]byte(strings.TrimPrefix(line, "RL_FILE:")), &outputFile)
			} else if strings.HasPrefix(line, "RL_QUALITY:") {
				parts := strings.Split(strings.TrimPrefix(line, "RL_QUALITY:"), ":")
				if len(parts) == 3 {
					width, _ := strconv.Atoi(parts[0])
					height, _ := strconv.Atoi(parts[1])
					fps, _ := strconv.ParseFloat(parts[2], 64)
					if width > 0 && height > 0 {
						label := fmt.Sprintf("%d×%d", width, height)
						if fps > 0 {
							label += fmt.Sprintf(" · %.0f fps", fps)
						}
						e.mu.Lock()
						j.Quality = label
						e.mu.Unlock()
					}
				}
			} else if strings.HasPrefix(line, "RL_POST:") {
				e.videoStage(j, "merging", 99)
			} else if strings.HasPrefix(line, "RL_STREAMS:") {
				var wanted []struct {
					ID     string  `json:"format_id"`
					Ext    string  `json:"ext"`
					Size   float64 `json:"filesize"`
					Approx float64 `json:"filesize_approx"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "RL_STREAMS:")), &wanted) == nil && len(wanted) > 0 {
					next := map[string]streamTransfer{}
					for _, stream := range wanted {
						name := safeFilename("media." + stream.ID + "." + stream.Ext)
						bytes := int64(0)
						for _, suffix := range []string{"", ".part"} {
							file := filepath.Join(staging, name+suffix)
							if pathInside(staging, file) {
								if info, err := os.Stat(file); err == nil {
									bytes = max(bytes, info.Size())
								}
							}
						}
						next[stream.ID] = streamTransfer{bytes, int64(max(stream.Size, stream.Approx))}
					}
					streams = next
				}
				e.mu.Lock()
				j.Status = "downloading"
				var cached, expected int64
				for _, stream := range streams {
					cached += stream.done
					expected += max(stream.total, stream.done)
				}
				j.done.Store(cached)
				j.Total = max(expected, r.Size)
				j.speedSampleAt, j.speedSampleBytes, j.speed = time.Now(), cached, 0
				if j.Total > 0 {
					j.Percent = min(99, float64(cached)/float64(j.Total)*100)
				}
				e.mu.Unlock()
			} else if strings.HasPrefix(line, "RL_PROGRESS:") {
				parts := strings.SplitN(strings.TrimPrefix(line, "RL_PROGRESS:"), ":", 2)
				if len(parts) != 2 {
					return
				}
				var p struct {
					Downloaded float64 `json:"downloaded_bytes"`
					Total      float64 `json:"total_bytes"`
					Estimate   float64 `json:"total_bytes_estimate"`
				}
				if json.Unmarshal([]byte(parts[1]), &p) != nil {
					return
				}
				streams[parts[0]] = streamTransfer{int64(p.Downloaded), int64(max(p.Total, p.Estimate))}
				var done, total int64
				for _, stream := range streams {
					done += stream.done
					total += max(stream.total, stream.done)
				}
				total = max(total, r.Size)
				e.mu.Lock()
				if done > j.done.Load() && !strings.HasPrefix(j.Notice, "来源服务器") {
					j.Notice = ""
				}
				j.Status, j.Total = "downloading", total
				j.done.Store(done)
				if total > 0 {
					j.Percent = min(99, float64(done)/float64(total)*100)
				}
				e.mu.Unlock()
			}
		}}
		errLines := &lineWriter{onLine: func(line string) {
			stderr.Write([]byte(line + "\n"))
			if strings.Contains(line, "[download]") && strings.Contains(line, "Got error") {
				e.mu.Lock()
				j.Notice = "连接中断，正在自动重试，已有数据会保留"
				e.mu.Unlock()
			}
		}}
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		stop, reason, exited := make(chan struct{}), make(chan string, 1), make(chan struct{})
		go e.watchVideoConnection(attemptCtx, cancelAttempt, j, attempt, stop, reason, exited)
		cmd := exec.Command(toolPath(dir, "yt-dlp"), args...)
		cmd.Dir = staging
		cmd.Stdout, cmd.Stderr = lines, errLines
		err = runManagedCommand(attemptCtx, cmd)
		close(stop)
		<-exited
		cancelAttempt()
		lines.Flush()
		errLines.Flush()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stalled := ""
		select {
		case stalled = <-reason:
		default:
		}
		if err == nil {
			break
		}
		details := stderr.String()
		if stalled == "" && !videoNetworkError(details) {
			return platformVideoError(r, ctx, details, err)
		}
		preserve = true
		if attempt == videoConnectionAttempts {
			preserve = true
			if videoCacheHasData(staging) {
				return errors.New("下载连接多次中断，已保留可续传数据。点击“续传”重试")
			}
			return errors.New("下载连接反复中断，多次重连后仍无法接收数据，请稍后重试")
		}
	}
	if outputFile != "" && !filepath.IsAbs(outputFile) {
		outputFile = filepath.Join(staging, outputFile)
	}
	if !pathInside(staging, outputFile) {
		return errors.New("视频引擎没有生成有效输出文件")
	}
	e.videoStage(j, "validating", 99)
	// Invalid media must not be reused on the next retry.
	preserve = false
	if err = validateMP4(ctx, dir, outputFile); err != nil {
		return err
	}
	preserve = true
	info, err := os.Stat(outputFile)
	if err != nil || info.Size() == 0 {
		return errors.New("生成的视频文件为空")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	final, err := promoteVideo(outputFile, cfg.DownloadDir, safeFilename(strings.TrimSuffix(r.Name, filepath.Ext(r.Name))+".mp4"))
	if err != nil {
		return err
	}
	e.mu.Lock()
	j.File, j.Name, j.Total = final, filepath.Base(final), info.Size()
	j.done.Store(info.Size())
	e.mu.Unlock()
	succeeded = true
	return nil
}
