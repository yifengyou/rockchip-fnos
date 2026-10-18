package main

// 解析飞牛下载页面，提取下载条目。
//
//   - ARM: https://fnnas.com/download-arm
//     页面是 Next.js 应用，设备条目内嵌在服务端渲染的 RSC 数据流里，即页面中
//     若干 `self.__next_f.push([1,"..."])` 调用。每条记录形如:
//
//       {"name":"友善 NanoPi R5S",
//        "url":"https://iso.liveupdate.fnnas.com/arm/image/.../...img.gz",
//        "hash":"1c94fab745066724e6cd5c500aba5c18",
//        "thunder":"thunder://QUFod...",
//        "originalName":"友善 NanoPi R5S"}
//
//   - x86: https://fnnas.com/download
//     同样是 Next.js 应用，fnOS 安装 ISO 嵌在 RSC 数据流里，结构为:
//
//       {"download":{"version":"1.2.0800",
//        "url":"https://iso.liveupdate.fnnas.com/x86_64/trim/...iso",
//        "hash":"851da934d172c71e48eec8335874aa3a",
//        "thunder":"thunder://QUFod..."}}
//
//     该页面还包含飞牛 App / TV / 同步 / 音乐 等非镜像条目（用 windowsUrl /
//     android / mediaAndroid 等字段），本文件只提取 download.url 为 ISO 的条目。
//
// 两种页面都用正则把条目抽出来，按 url 去重（页面会把列表渲染两次）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DeviceEntry 单条设备下载信息。
type DeviceEntry struct {
	Name         string `json:"name"`
	URL          string `json:"url"`           // 页面上的直链
	MD5          string `json:"md5"`           // 页面提供的 MD5
	Thunder      string `json:"thunder"`       // 迅雷链接 (thunder://)
	OriginalName string `json:"original_name"` // 通常与 Name 相同
	Version      string `json:"version"`       // x86 条目带 version 字段；ARM 留空
	Arch         string `json:"arch"`          // "arm" / "x86"，仅用于展示与日志
}

// FetchPage 抓取下载页面 HTML（ARM 用 download-arm，x86 用 /download）。
func FetchPage(pageURL, userAgent string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("抓取页面失败: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// decodeNextFChunks 从 HTML 中提取所有 self.__next_f.push([1,"..."]) 调用，
// 按 JSON 字符串转义规则解码后拼接成完整 RSC 流。
func decodeNextFChunks(html string) string {
	re := regexp.MustCompile(`self\.__next_f\.push\(\[\d+,"(.*?)"\]\)`)
	var sb strings.Builder
	for _, m := range re.FindAllStringSubmatchIndex(html, -1) {
		raw := html[m[2]:m[3]]
		// 每个 chunk 是一段 JSON 字符串内容（不含外层引号），用 json 解码还原
		var decoded string
		if err := json.Unmarshal([]byte("\""+raw+"\""), &decoded); err != nil {
			// 退回原始内容
			decoded = raw
		}
		sb.WriteString(decoded)
	}
	return sb.String()
}

// entryRe 匹配单条设备记录。
// 字段顺序按页面实际格式: name, url, hash, thunder, 可选 originalName。
var entryRe = regexp.MustCompile(
	`"name":"(?P<name>[^"]+)"` +
		`,"url":"(?P<url>https://iso\.liveupdate\.fnnas\.com/[^"]+)"` +
		`,"hash":"(?P<hash>[^"]*)"` +
		`,"thunder":"(?P<thunder>[^"]*)"` +
		`(?:,"originalName":"(?P<orig>[^"]*)")?`,
)

// ParseDevices 从 download-arm 页面 HTML 解析所有 ARM 设备下载条目，
// 按 url 去重并按名称排序。
func ParseDevices(html string) []DeviceEntry {
	stream := decodeNextFChunks(html)
	names := entryRe.SubexpNames()
	idx := map[string]int{}
	for i, n := range names {
		if n != "" {
			idx[n] = i
		}
	}

	seen := map[string]struct{}{}
	var out []DeviceEntry
	for _, m := range entryRe.FindAllStringSubmatch(stream, -1) {
		url := m[idx["url"]]
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		orig := m[idx["orig"]]
		if orig == "" {
			orig = m[idx["name"]]
		}
		out = append(out, DeviceEntry{
			Name:         m[idx["name"]],
			URL:          url,
			MD5:          m[idx["hash"]],
			Thunder:      m[idx["thunder"]],
			OriginalName: orig,
			Arch:         "arm",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// x86DownloadRe 匹配 /download 页面里 fnOS ISO 的下载块。
// 结构形如:
//
//	{"download":{"version":"1.2.0800",
//	 "url":"https://iso.liveupdate.fnnas.com/x86_64/trim/...iso",
//	 "hash":"851da934d172c71e48eec8335874aa3a",
//	 "thunder":"thunder://QUFod..."}}
//
// 该页面还有飞牛同步的 {"download":{"version":...,"windowsUrl":...,...}} 块，
// 它们没有 "url":"https://iso.liveupdate..." 字段，因此下面的正则要求 url 指向
// iso.liveupdate.fnnas.com 且文件名以 .iso 结尾，从而只匹配 fnOS 安装镜像。
var x86DownloadRe = regexp.MustCompile(
	`"download":\{` +
		`"version":"(?P<version>[^"]*)"` +
		`,"url":"(?P<url>https://iso\.liveupdate\.fnnas\.com/[^"]*\.iso)"` +
		`,"hash":"(?P<hash>[^"]*)"` +
		`,"thunder":"(?P<thunder>[^"]*)"`,
)

// ParseX86 从 /download 页面 HTML 解析 fnOS x86_64 ISO 下载条目。
// 按 url 去重；页面通常只有一个 ISO 条目（被渲染两次）。
func ParseX86(html string) []DeviceEntry {
	stream := decodeNextFChunks(html)
	names := x86DownloadRe.SubexpNames()
	idx := map[string]int{}
	for i, n := range names {
		if n != "" {
			idx[n] = i
		}
	}

	seen := map[string]struct{}{}
	var out []DeviceEntry
	for _, m := range x86DownloadRe.FindAllStringSubmatch(stream, -1) {
		url := m[idx["url"]]
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		version := m[idx["version"]]
		name := "飞牛 fnOS x86_64"
		if version != "" {
			name = "飞牛 fnOS x86_64 " + version
		}
		out = append(out, DeviceEntry{
			Name:         name,
			URL:          url,
			MD5:          m[idx["hash"]],
			Thunder:      m[idx["thunder"]],
			OriginalName: name,
			Version:      version,
			Arch:         "x86",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MatchWhitelist 判断设备是否命中白名单。
//   - 空白名单：不下载任何设备
//   - 含 "*"：下载全部
//   - 否则对 name / original_name / url 做不区分大小写的子串匹配
func MatchWhitelist(d DeviceEntry, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	hay := strings.ToLower(d.Name + "\x00" + d.OriginalName + "\x00" + d.URL)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.Contains(hay, p) {
			return true
		}
	}
	return false
}
