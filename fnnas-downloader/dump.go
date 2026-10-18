package main

// Dump 模式：只解析页面、把设备下载链接清单以单个 CSV 文件写到指定目录，
// 不下载任何镜像文件。适用于定时任务维护一份链接清单。
//
// 文件名格式: fnos-filelist-YYYYMMDD.csv（TIMESTAMP 精确到天）
// 例: fnos-filelist-20260102.csv

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DumpDevices 把设备清单以单个带时间戳的 CSV 文件写到 dir 目录。
// 返回写出的文件路径。
// armPageURL / x86PageURL 用于在 CSV 的"来源页面"列按条目架构填入对应页面。
func DumpDevices(dir string, devices []DeviceEntry, armPageURL, x86PageURL string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	ts := time.Now().Format("20060102") // 8 位精确到天
	filename := fmt.Sprintf("fnos-filelist-%s.csv", ts)
	path := filepath.Join(dir, filename)

	if err := writeCSV(path, devices, armPageURL, x86PageURL); err != nil {
		return "", err
	}
	return path, nil
}

// writeCSV 把设备清单写成 UTF-8 BOM 的 CSV，Excel 友好。
func writeCSV(path string, devices []DeviceEntry, armPageURL, x86PageURL string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// UTF-8 BOM，让 Excel 正确识别编码
	_, _ = f.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{
		"序号", "架构", "设备名", "原始名", "MD5",
		"直链下载地址", "迅雷链接", "来源页面",
	}); err != nil {
		return err
	}
	for i, d := range devices {
		arch := d.Arch
		if arch == "" {
			arch = "arm"
		}
		src := armPageURL
		if arch == "x86" {
			src = x86PageURL
		}
		if err := w.Write([]string{
			fmt.Sprintf("%d", i+1),
			arch,
			d.Name,
			d.OriginalName,
			d.MD5,
			d.URL,
			d.Thunder,
			src,
		}); err != nil {
			return err
		}
	}
	return nil
}
