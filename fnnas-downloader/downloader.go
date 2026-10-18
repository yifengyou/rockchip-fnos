package main

// 并发断点续传下载器，附带 MD5 校验。
//
// 核心特性:
//   - 断点续传: 用 HTTP Range 请求，已下载部分写到 .part 文件，重启后接着写
//   - 跳过已校验: output_dir 里已存在同名文件且 MD5 匹配则直接跳过
//   - MD5 校验: 下载完成后校验，失败则删除 .part 重试
//   - 并发: 多个设备同时下载，数量由配置控制
//   - 下载源重写: 把页面直链的 host 替换为配置里的 download_host
//     (因为 https://iso.liveupdate.fnnas.com 在部分网络环境会被 CDN 拦截，
//      而 http://thunder.liveupdate.fnnas.com:8080 路径相同且支持 Range)

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Downloader 负责把设备镜像下载到本地目录。
type Downloader struct {
	OutputDir    string
	UserAgent    string
	DownloadHost string // 若非空，把页面直链的 scheme+host 替换为此值
	SkipVerified bool
	Retry        int
	SplitSize    int64 // 每份最大字节数；>0 时下载完成后把超过该大小的文件切成 -part01/-part02 ... 并删除原文件（规避 GitHub 单文件 2GB 限制）
	HTTPClient   *http.Client
	OnProgress   func(device string, msg string) // 可选的进度回调
}

// NewDownloader 构造一个下载器。timeout 控制单次 HTTP 请求的超时。
func NewDownloader(cfg Config) *Downloader {
	return &Downloader{
		OutputDir:    cfg.OutputDir,
		UserAgent:    cfg.UserAgent,
		DownloadHost: cfg.DownloadHost,
		SkipVerified: cfg.SkipVerified,
		Retry:        cfg.Retry,
		SplitSize:    cfg.SplitSize,
		HTTPClient: &http.Client{
			Timeout: 0, // 下载大文件不设整体超时，靠每次连接的临时 client 控制
		},
	}
}

// rewriteURL 把页面直链替换成实际可下载的 host。
// 例如 https://iso.liveupdate.fnnas.com/arm/...  ->  http://thunder.liveupdate.fnnas.com:8080/arm/...
// 若 DownloadHost 为空或解析失败则原样返回。
func (dl *Downloader) rewriteURL(raw string) string {
	if dl.DownloadHost == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.TrimRight(dl.DownloadHost, "/")
	return host + u.RequestURI()
}

// fileName 从 URL 取出文件名，作为本地落盘名。
func (dl *Downloader) fileName(d DeviceEntry) string {
	u, err := url.Parse(d.URL)
	if err != nil {
		// 退而求其次：用 original_name 的安全形式
		return sanitize(d.OriginalName) + ".img.gz"
	}
	return filepath.Base(u.Path)
}

// partPath / finalPath 给出临时与最终路径。全部平铺到 OutputDir。
func (dl *Downloader) partPath(d DeviceEntry) string {
	return filepath.Join(dl.OutputDir, dl.fileName(d)+".part")
}
func (dl *Downloader) finalPath(d DeviceEntry) string {
	return filepath.Join(dl.OutputDir, dl.fileName(d))
}

// log 通过回调输出进度信息。
func (dl *Downloader) log(device, format string, args ...any) {
	if dl.OnProgress != nil {
		dl.OnProgress(device, fmt.Sprintf(format, args...))
	}
}

// ShouldSkip 判断是否可以跳过下载:
//   - 开启 SplitSize 时，校验所有分片存在且 MD5 匹配
//   - 否则，最终文件已存在且 SkipVerified 开启，且 MD5 匹配 -> 跳过
func (dl *Downloader) ShouldSkip(d DeviceEntry) (bool, error) {
	if !dl.SkipVerified {
		return false, nil
	}
	// 分片模式
	if dl.SplitSize > 0 {
		parts := dl.findParts(d)
		if len(parts) == 0 {
			return false, nil
		}
		if d.MD5 == "" {
			dl.log(d.Name, "已存在 %d 个分片，但页面未提供 MD5，跳过校验直接视为已完成", len(parts))
			return true, nil
		}
		dl.log(d.Name, "已存在 %d 个分片，校验 MD5 ...", len(parts))
		ok, err := verifyPartsMD5(parts, d.MD5)
		if err != nil {
			return false, err
		}
		if ok {
			dl.log(d.Name, "MD5 校验通过（分片合并），跳过下载")
			return true, nil
		}
		dl.log(d.Name, "MD5 不匹配，删除旧分片重新下载")
		_ = removeParts(parts)
		return false, nil
	}
	// 单文件模式
	final := dl.finalPath(d)
	info, err := os.Stat(final)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if d.MD5 == "" {
		// 无 MD5 无法校验，不跳过
		return false, nil
	}
	dl.log(d.Name, "已存在 %s (%s)，校验 MD5...", filepath.Base(final), humanSize(info.Size()))
	ok, err := verifyMD5(final, d.MD5)
	if err != nil {
		return false, err
	}
	if ok {
		dl.log(d.Name, "MD5 校验通过，跳过下载")
		return true, nil
	}
	dl.log(d.Name, "MD5 不匹配，重新下载")
	// 校验失败的旧文件删掉，避免干扰
	_ = os.Remove(final)
	return false, nil
}

// Download 下载单个设备镜像（含重试）。返回 (skipped, error)。
// ctx 取消时会尽快中止当前下载并返回 ctx.Err()，已写入的 .part 保留以便续传。
func (dl *Downloader) Download(ctx context.Context, d DeviceEntry) (bool, error) {
	// 先尝试跳过
	if skip, err := dl.ShouldSkip(d); err != nil {
		return false, err
	} else if skip {
		return true, nil
	}

	target := dl.rewriteURL(d.URL)
	final := dl.finalPath(d)
	part := dl.partPath(d)

	var lastErr error
	for attempt := 1; attempt <= dl.Retry+1; attempt++ {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// 每次重试都确保输出目录存在（目录可能被外部删除）
		if err := os.MkdirAll(dl.OutputDir, 0o755); err != nil {
			return false, err
		}
		dl.log(d.Name, "开始下载 (第 %d 次) %s", attempt, target)
		err := dl.downloadOnce(ctx, d, target, part, final)
		if err == nil {
			return false, nil
		}
		if ctx.Err() != nil {
			// 被用户中断，不再重试
			return false, ctx.Err()
		}
		lastErr = err
		dl.log(d.Name, "下载失败: %v", err)
		if attempt <= dl.Retry {
			dl.log(d.Name, "等待 %ds 后重试...", attempt*3)
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(attempt*3) * time.Second):
			}
		}
	}
	return false, fmt.Errorf("重试 %d 次仍失败: %w", dl.Retry, lastErr)
}

// downloadOnce 执行一次带断点续传的下载，完成后做 MD5 校验并重命名 .part -> 最终文件。
func (dl *Downloader) downloadOnce(ctx context.Context, d DeviceEntry, target, part, final string) error {
	// 已下载字节数（用于 Range 续传）
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	// 大文件下载不设整体超时，靠 ctx 控制生命周期
	dialClient := &http.Client{Timeout: 0}

	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", dl.UserAgent)
	req.Header.Set("Accept", "*/*")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		dl.log(d.Name, "断点续传: 从 %s 继续", humanSize(offset))
	}

	resp, err := dialClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 期望 200 (全新下载) 或 206 (断点续传)
	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0 // 服务器不支持 Range 或要求从头开始，覆盖写
	case http.StatusPartialContent:
		// 继续追加
	default:
		return fmt.Errorf("服务器返回 HTTP %d", resp.StatusCode)
	}

	// 打开 .part 文件：200 时覆盖，206 时追加
	var f *os.File
	if offset > 0 && resp.StatusCode == http.StatusPartialContent {
		f, err = os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0o644)
	} else {
		f, err = os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	}
	if err != nil {
		return err
	}
	defer f.Close()

	total := resp.ContentLength
	if total > 0 && resp.StatusCode == http.StatusPartialContent {
		total += offset // 206 的 ContentLength 只是剩余部分
	}

	// 流式拷贝并报告进度
	start := time.Now()
	written, err := dl.copyWithProgress(ctx, f, resp.Body, d.Name, offset, total, start)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // 被中断，保留 .part 以便续传
		}
		return fmt.Errorf("写入失败: %w", err)
	}
	dl.log(d.Name, "下载完成 %s，用时 %s", humanSize(written), time.Since(start).Round(time.Second))

	// MD5 校验（仅当页面提供了 hash）
	if d.MD5 != "" {
		dl.log(d.Name, "校验 MD5 ...")
		ok, err := verifyMD5(part, d.MD5)
		if err != nil {
			return err
		}
		if !ok {
			_ = os.Remove(part)
			return fmt.Errorf("MD5 校验失败 (期望 %s)", d.MD5)
		}
		dl.log(d.Name, "MD5 校验通过")
	} else {
		dl.log(d.Name, "页面未提供 MD5，跳过校验")
	}

	// .part -> 最终文件名
	if err := os.Rename(part, final); err != nil {
		return fmt.Errorf("重命名失败: %w", err)
	}

	// 如果设置了 SplitSize 且文件超过阈值，切成 -part01/-part02 ... 并删除原文件
	// （规避 GitHub Releases 单文件 2GB 上传限制）
	if dl.SplitSize > 0 {
		if err := dl.splitFile(d, final); err != nil {
			return fmt.Errorf("分片失败: %w", err)
		}
	}
	return nil
}

// copyWithProgress 流式拷贝，每 5% 报告一次进度，并响应 ctx 取消。
// ctx 取消时 src.Read 会随请求被中止而返回错误，循环随即退出。
func (dl *Downloader) copyWithProgress(ctx context.Context, dst io.Writer, src io.Reader, device string, offset, total int64, start time.Time) (int64, error) {
	buf := make([]byte, 256*1024)
	var written int64
	lastReport := -1
	for {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written + int64(n), werr
			}
			written += int64(n)
			// 报告进度
			if total > 0 {
				pct := int((offset + written) * 100 / total)
				if pct-lastReport >= 5 {
					lastReport = pct
					elapsed := time.Since(start).Seconds()
					speed := 0.0
					if elapsed > 0 {
						speed = float64(written) / elapsed
					}
					dl.log(device, "进度 %d%% (%s / %s, %s/s)",
						pct, humanSize(offset+written), humanSize(total), humanSize(int64(speed)))
				}
			}
		}
		if err == io.EOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

// verifyMD5 计算文件 MD5 并与期望值比较（不区分大小写）。
func verifyMD5(path, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(got, expected), nil
}

// humanSize 把字节数转成人类可读形式。
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// sanitize 把字符串转成文件名安全的形式（保留中文等，去掉路径分隔符）。
func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "-")
	return r.Replace(s)
}

// partSuffix 给出第 n 份分片（从 1 开始）的后缀，固定两位补零：-part01 / -part02 ...
// 注意：分片后缀用 "-partNN"，与下载过程中的临时文件后缀 ".part" 不同，
// 不会和断点续传的 .part 临时文件混淆。
func partSuffix(n int) string {
	return fmt.Sprintf("-part%02d", n)
}

// partPathName 给出最终文件名对应的第 n 份分片文件名（不含目录）。
// 例: foo.iso + n=1 -> foo.iso-part01
func (dl *Downloader) partPathName(d DeviceEntry, n int) string {
	return dl.fileName(d) + partSuffix(n)
}

// findParts 扫描 OutputDir，返回某设备最终文件对应的全部分片路径，按序号升序。
// 通过 fileName + "-partNN" 前缀匹配。没有分片时返回 nil。
func (dl *Downloader) findParts(d DeviceEntry) []string {
	base := dl.fileName(d) + "-part"
	entries, err := os.ReadDir(dl.OutputDir)
	if err != nil {
		return nil
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// 后面必须恰好是 2 位数字
		tail := name[len(base):]
		if len(tail) != 2 {
			continue
		}
		if tail[0] < '0' || tail[0] > '9' || tail[1] < '0' || tail[1] > '9' {
			continue
		}
		parts = append(parts, filepath.Join(dl.OutputDir, name))
	}
	// 按 tail 字符串排序即可，因为两位补零
	for i := 0; i < len(parts); i++ {
		for j := i + 1; j < len(parts); j++ {
			if parts[j] < parts[i] {
				parts[i], parts[j] = parts[j], parts[i]
			}
		}
	}
	return parts
}

// splitFile 把 final 切成若干 -partNN 分片，每份不超过 SplitSize 字节，
// 切完校验字节数合计与原文件一致后删除原文件。
// 文件本身不超过阈值时不动它（保持单文件）。
func (dl *Downloader) splitFile(d DeviceEntry, final string) error {
	info, err := os.Stat(final)
	if err != nil {
		return err
	}
	totalSize := info.Size()
	if totalSize <= dl.SplitSize {
		// 不需要切
		return nil
	}
	nParts := int(totalSize / dl.SplitSize)
	if totalSize%dl.SplitSize != 0 {
		nParts++
	}
	dl.log(d.Name, "文件 %s (%s) 超过 %s，拆分为 %d 份分片",
		filepath.Base(final), humanSize(totalSize), humanSize(dl.SplitSize), nParts)

	src, err := os.Open(final)
	if err != nil {
		return err
	}
	defer src.Close()

	buf := make([]byte, 256*1024)
	var writtenTotal int64
	for i := 1; i <= nParts; i++ {
		partName := dl.partPathName(d, i)
		partPath := filepath.Join(dl.OutputDir, partName)
		dst, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		var written int64
		for written < dl.SplitSize {
			remain := dl.SplitSize - written
			if int64(len(buf)) > remain {
				buf = buf[:remain]
			}
			n, rerr := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					dst.Close()
					return werr
				}
				written += int64(n)
				writtenTotal += int64(n)
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				dst.Close()
				return rerr
			}
		}
		dst.Close()
		dl.log(d.Name, "  -> %s (%s)", partName, humanSize(written))
		// 恢复 buf 容量（上面可能截短了）
		if cap(buf) < 256*1024 {
			buf = make([]byte, 256*1024)
		} else {
			buf = buf[:cap(buf)]
		}
	}

	if writtenTotal != totalSize {
		return fmt.Errorf("分片字节数 %d 与原文件 %d 不一致，已中止（原文件保留）", writtenTotal, totalSize)
	}

	// 校验通过，删除原文件
	if err := os.Remove(final); err != nil {
		dl.log(d.Name, "警告: 删除原文件失败: %v（分片已生成）", err)
		return err
	}
	dl.log(d.Name, "分片完成，已删除原文件 %s", filepath.Base(final))
	return nil
}

// verifyPartsMD5 把所有分片按序合并计算 MD5，与期望值比较（不区分大小写）。
func verifyPartsMD5(parts []string, expected string) (bool, error) {
	h := md5.New()
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			return false, err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return false, err
		}
		f.Close()
	}
	got := hex.EncodeToString(h.Sum(nil))
	return strings.EqualFold(got, expected), nil
}

// removeParts 删除一组分片文件（忽略不存在的错误）。
func removeParts(parts []string) error {
	var lastErr error
	for _, p := range parts {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			lastErr = err
		}
	}
	return lastErr
}

// parseSplitSize 解析形如 "1.8G" / "1800M" / "2000000000" 的尺寸字符串为字节数。
// 支持 K/M/G/T（不区分大小写），裸数字视为字节。空串返回 0。
func parseSplitSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	last := s[len(s)-1]
	mul := int64(1)
	num := s
	switch last {
	case 'k', 'K':
		mul = 1024
		num = s[:len(s)-1]
	case 'm', 'M':
		mul = 1024 * 1024
		num = s[:len(s)-1]
	case 'g', 'G':
		mul = 1024 * 1024 * 1024
		num = s[:len(s)-1]
	case 't', 'T':
		mul = 1024 * 1024 * 1024 * 1024
		num = s[:len(s)-1]
	}
	// 允许小数，如 1.8G
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("无法解析尺寸 %q: %w", s, err)
	}
	return int64(f * float64(mul)), nil
}
