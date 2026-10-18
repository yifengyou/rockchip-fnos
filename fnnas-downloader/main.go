package main

// fnnas ARM 下载器入口。
//
// 用法:
//   go run .                       # 用 ./config.toml（缺失则自动生成默认配置）
//   go run . -config my.toml       # 指定配置文件
//   go run . -list                 # 仅列出页面解析到的所有设备，不下载
//   go run . -dry-run              # 按白名单过滤后展示将下载哪些，不真正下载
//   go run . -devices "nanopi-r5s,rock-5b"  # 命令行指定白名单（优先于配置文件）
//
// 过滤优先级: 命令行 -devices > 配置文件 [[devices]] 段。便于在 CI matrix 中
// 按设备分组分流下载，避免单次任务下载全部镜像超出磁盘限额。
//
// 构建:
//   go build -o fnnas-downloader .

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.toml", "配置文件路径")
	listOnly := flag.Bool("list", false, "仅列出页面解析到的所有设备，不下载")
	dryRun := flag.Bool("dry-run", false, "按白名单过滤后展示将下载哪些设备，不真正下载")
	dumpDir := flag.String("dump", "", "把全部设备的链接清单以带时间戳的 CSV 写到该目录后退出（不下载镜像）")
	devicesFlag := flag.String("devices", "", "命令行设备白名单，逗号分隔（优先于配置文件的 [[devices]] 段）。例: -devices \"nanopi-r5s,rock-5b\"；用 \"*\" 下载全部")
	flag.Parse()

	// 1. 读取配置（缺失会自动生成默认配置）
	//    -list 模式只展示页面解析结果，不需要白名单，配置缺失也允许继续
	cfg, generated, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取配置失败: %v\n", err)
		os.Exit(1)
	}
	if generated {
		fmt.Printf("未找到配置文件，已生成默认配置: %s\n", *configPath)
		if !*listOnly && *devicesFlag == "" {
			fmt.Println("请编辑其中的 [[devices]] 段选择要下载的设备型号后重新运行，")
			fmt.Println("或使用 -devices \"nanopi-r5s,rock-5b\" 在命令行指定，使用 \"*\" 可下载全部设备。")
			return
		}
		fmt.Println("（-list 模式或 -devices 已指定，继续执行）")
		fmt.Println()
	}

	// 1.5 命令行 -devices 优先于配置文件的 [[devices]] 段。
	//     这是为了支持 CI matrix 按设备分流下载。
	if *devicesFlag != "" {
		cfg.Devices = parseDevicesFlag(*devicesFlag)
		fmt.Printf("使用命令行白名单 (%d 条): %v\n", len(cfg.Devices), patterns(cfg))
	}

	// 2. 抓取并解析页面（ARM + x86）
	fmt.Printf("抓取 ARM 页面: %s\n", cfg.PageURL)
	html, err := FetchPage(cfg.PageURL, cfg.UserAgent, 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "抓取 ARM 页面失败: %v\n", err)
		os.Exit(1)
	}
	devices := ParseDevices(html)
	fmt.Printf("解析到 %d 个 ARM 设备\n", len(devices))

	// 同时抓取 x86 页面（/download），解析其中的 fnOS ISO 条目
	var x86Entries []DeviceEntry
	if cfg.EnableX86 && cfg.X86PageURL != "" {
		fmt.Printf("抓取 x86 页面: %s\n", cfg.X86PageURL)
		x86HTML, xerr := FetchPage(cfg.X86PageURL, cfg.UserAgent, 30*time.Second)
		if xerr != nil {
			fmt.Fprintf(os.Stderr, "抓取 x86 页面失败: %v\n", xerr)
			os.Exit(1)
		}
		x86Entries = ParseX86(x86HTML)
		fmt.Printf("解析到 %d 个 x86 ISO 条目\n", len(x86Entries))
	}

	// 合并：ARM 设备在前，x86 ISO 在后
	devices = append(devices, x86Entries...)

	if *listOnly {
		fmt.Println()
		printDeviceTable(devices)
		return
	}

	// -dump 模式：把全部设备的链接清单以单个带时间戳的 CSV 写到目录后退出，不下载镜像
	if *dumpDir != "" {
		path, err := DumpDevices(*dumpDir, devices, cfg.PageURL, cfg.X86PageURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "写出清单失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("已把 %d 个设备的链接清单写到 %s\n", len(devices), path)
		return
	}

	// 3. 按白名单过滤
	var selected []DeviceEntry
	selected = filterByWhitelist(devices, patterns(cfg))
	fmt.Printf("白名单命中 %d 个设备\n", len(selected))

	if len(selected) == 0 {
		fmt.Println("没有命中的设备。请在配置文件中添加 [[devices]] 段，")
		fmt.Println("或使用 -devices 在命令行指定，例如:")
		fmt.Println("  -devices \"nanopi-r5s\"")
		fmt.Println("  -devices \"nanopi-r5s,rock-5b\"")
		fmt.Println("或下载全部:")
		fmt.Println("  -devices \"*\"")
		return
	}

	if *dryRun {
		fmt.Println("\n将下载以下设备 (-dry-run 模式，不会真正下载):")
		printDeviceTable(selected)
		return
	}

	// 4. 并发下载
	fmt.Printf("\n开始下载到 %s (并发 %d)\n", cfg.OutputDir, cfg.Concurrency)
	fmt.Println("──────────────────────────────────────────────")

	dl := NewDownloader(cfg)
	dl.OnProgress = func(device, msg string) {
		// 简单的进度打印：[设备名] 消息
		fmt.Printf("[%s] %s\n", truncate(device, 28), msg)
	}

	// 捕获 Ctrl+C，让正在写的 .part 保留以便断点续传
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n收到中断信号，等待当前下载写入完成后退出（.part 文件保留以便续传）...")
		cancel()
	}()

	var (
		wg      sync.WaitGroup
		sem     = make(chan struct{}, cfg.Concurrency)
		success atomic.Int64
		skipped atomic.Int64
		failed  atomic.Int64
	)
	mu := &sync.Mutex{} // 保护标准输出顺序的可读性

	for _, d := range selected {
		d := d
		select {
		case <-ctx.Done():
			goto wait
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}
			skip, err := dl.Download(ctx, d)
			mu.Lock()
			if err != nil {
				if ctx.Err() != nil {
					// 被中断，不算失败
					fmt.Printf("[%-28s] ⊘ 已中断（.part 保留以便续传）\n", truncate(d.Name, 28))
				} else {
					failed.Add(1)
					fmt.Printf("[%-28s] ✗ 失败: %v\n", truncate(d.Name, 28), err)
				}
			} else if skip {
				skipped.Add(1)
				fmt.Printf("[%-28s] ⊘ 跳过（已存在且校验通过）\n", truncate(d.Name, 28))
			} else {
				success.Add(1)
				fmt.Printf("[%-28s] ✓ 下载完成\n", truncate(d.Name, 28))
			}
			mu.Unlock()
		}()
	}

wait:
	wg.Wait()

	fmt.Println("──────────────────────────────────────────────")
	fmt.Printf("完成: 成功 %d, 跳过 %d, 失败 %d (共 %d)\n",
		success.Load(), skipped.Load(), failed.Load(), len(selected))
	if failed.Load() > 0 {
		os.Exit(1)
	}
}

// patterns 把 cfg.Devices 转成字符串切片。
func patterns(cfg Config) []string {
	out := make([]string, 0, len(cfg.Devices))
	for _, d := range cfg.Devices {
		out = append(out, d.Pattern)
	}
	return out
}

// parseDevicesFlag 解析命令行 -devices 参数（逗号分隔），返回 Device 切片。
// 空白条目会被忽略。"*" 表示下载全部。
// 例: "nanopi-r5s,rock-5b,*" -> [{nanopi-r5s}, {rock-5b}, {*}]
func parseDevicesFlag(s string) []Device {
	var out []Device
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, Device{Pattern: p})
	}
	return out
}

// filterByWhitelist 用白名单 patterns 过滤设备列表。
func filterByWhitelist(devices []DeviceEntry, patterns []string) []DeviceEntry {
	var out []DeviceEntry
	for _, d := range devices {
		if MatchWhitelist(d, patterns) {
			out = append(out, d)
		}
	}
	return out
}

// printDeviceTable 打印设备清单表格。
func printDeviceTable(devices []DeviceEntry) {
	fmt.Printf("%-3s  %-4s  %-32s  %-32s  %s\n", "#", "架构", "设备名", "MD5", "下载链接")
	fmt.Println("----------------------------------------------------------------------------------------------------------------------")
	for i, d := range devices {
		arch := d.Arch
		if arch == "" {
			arch = "arm"
		}
		fmt.Printf("%-3d  %-4s  %-32s  %-32s  %s\n", i+1, arch, truncate(d.Name, 32), d.MD5, d.URL)
	}
}

// truncate 截断字符串到 maxRunes 个 rune，超出加 ".."。
func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-2]) + ".."
}
