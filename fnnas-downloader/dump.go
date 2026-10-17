package main

// Dump 模式：只解析页面、把设备下载链接清单写到指定目录，
// 不下载任何镜像文件。适用于定时任务维护一份链接清单。
//
// 在目录下生成:
//   fnos_image/devices.json   完整结构化数据（含直链/MD5/迅雷链接）
//   fnos_image/devices.csv    表格（带 BOM，Excel 友好）
//   fnos_image/devices.md     Markdown 表格
//   fnos_image/README.md      人类可读的下载说明（含每个设备链接）

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DumpDevices 把设备清单以多种格式写到 dir 目录。
// selected 为 true 时表示已按白名单过滤（标题反映"已开启"设备），
// 为 false 时表示全部设备。
func DumpDevices(dir string, devices []DeviceEntry, selected bool, pageURL string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	if err := writeJSON(filepath.Join(dir, "devices.json"), devices, pageURL); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(dir, "devices.csv"), devices); err != nil {
		return err
	}
	if err := writeMarkdown(filepath.Join(dir, "devices.md"), devices, selected, pageURL); err != nil {
		return err
	}
	if err := writeReadme(filepath.Join(dir, "README.md"), devices, selected, pageURL); err != nil {
		return err
	}
	return nil
}

func writeJSON(path string, devices []DeviceEntry, pageURL string) error {
	data := struct {
		Source      string        `json:"source"`
		GeneratedAt string        `json:"generated_at"`
		Count       int           `json:"count"`
		Devices     []DeviceEntry `json:"devices"`
	}{
		Source:      pageURL,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Count:       len(devices),
		Devices:     devices,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func writeCSV(path string, devices []DeviceEntry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// UTF-8 BOM，让 Excel 正确识别编码
	_, _ = f.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"序号", "设备名", "原始名", "MD5", "直链下载地址", "迅雷链接"}); err != nil {
		return err
	}
	for i, d := range devices {
		if err := w.Write([]string{
			fmt.Sprintf("%d", i+1), d.Name, d.OriginalName, d.MD5, d.URL, d.Thunder,
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeMarkdown(path string, devices []DeviceEntry, selected bool, pageURL string) error {
	scope := "全部"
	if selected {
		scope = "白名单已开启"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 飞牛 fnOS ARM 设备下载地址（%s）\n\n", scope)
	fmt.Fprintf(&sb, "来源：<%s>\n\n", pageURL)
	fmt.Fprintf(&sb, "更新时间：%s ｜ 设备数量：%d\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"), len(devices))
	sb.WriteString("| 序号 | 设备名 | MD5 | 下载链接 |\n")
	sb.WriteString("|------|--------|-----|----------|\n")
	for i, d := range devices {
		fmt.Fprintf(&sb, "| %d | %s | `%s` | %s |\n", i+1, d.Name, d.MD5, d.URL)
	}
	sb.WriteString("\n")
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

func writeReadme(path string, devices []DeviceEntry, selected bool, pageURL string) error {
	scope := "全部 ARM 设备"
	if selected {
		scope = "白名单已开启的设备"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# fnOS ARM 镜像下载清单\n\n")
	fmt.Fprintf(&sb, "本目录由自动化任务定期从飞牛官网解析并更新，仅包含下载链接清单，\n")
	fmt.Fprintf(&sb, "**不包含镜像本体**（单个镜像约 1.9GB，请按需自行下载）。\n\n")
	fmt.Fprintf(&sb, "- 数据来源：%s\n", pageURL)
	fmt.Fprintf(&sb, "- 包含范围：%s\n", scope)
	fmt.Fprintf(&sb, "- 设备数量：%d\n", len(devices))
	fmt.Fprintf(&sb, "- 最近更新：%s\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	sb.WriteString("## 文件说明\n\n")
	sb.WriteString("| 文件 | 说明 |\n|------|------|\n")
	sb.WriteString("| `devices.json` | 完整结构化数据（含直链/MD5/迅雷链接），供程序读取 |\n")
	sb.WriteString("| `devices.csv` | 表格（Excel 友好） |\n")
	sb.WriteString("| `devices.md` | Markdown 表格 |\n\n")
	sb.WriteString("## 下载方式\n\n")
	sb.WriteString("镜像直链格式为 `https://iso.liveupdate.fnnas.com/arm/...`，\n")
	sb.WriteString("若该地址在你的网络环境返回 403，可改用迅雷镜像\n")
	sb.WriteString("`http://thunder.liveupdate.fnnas.com:8080` + 相同路径（支持 Range 断点续传）。\n\n")
	sb.WriteString("## 设备列表\n\n")
	sb.WriteString("| 设备名 | MD5 | 下载链接 |\n")
	sb.WriteString("|--------|-----|----------|\n")
	for _, d := range devices {
		fmt.Fprintf(&sb, "| %s | `%s` | %s |\n", d.Name, d.MD5, d.URL)
	}
	sb.WriteString("\n")
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
