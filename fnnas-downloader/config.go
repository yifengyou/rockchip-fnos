package main

// Config 描述配置文件（config.toml）的结构。
//
// 示例 config.toml:
//
//   page_url       = "https://fnnas.com/download-arm"
//   x86_page_url   = "https://fnnas.com/download"
//   enable_x86     = true
//   output_dir     = "downloads"
//   concurrency    = 4
//   user_agent     = "Mozilla/5.0 ..."
//   download_host  = "http://thunder.liveupdate.fnnas.com:8080"
//   skip_verified  = true
//   retry          = 5
//
//   # 开启下载的设备型号白名单。
//   # 匹配规则：不区分大小写，对 name / original_name / url 三个字段做子串匹配。
//   # 留空数组 = 不下载任何设备；使用 ["*"] = 下载全部设备。
//   [[devices]]
//   pattern = "nanopi-r5s"
//
//   [[devices]]
//   pattern = "rock-5b"
//
//   # 也可以按中文设备名匹配
//   [[devices]]
//   pattern = "友善"
//
//   # x86 的 fnOS ISO 条目名形如 "飞牛 fnOS x86_64 1.2.0800"，
//   # 可用 pattern = "x86" 或 pattern = "fnOS x86_64" 单独匹配它。

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config 顶层配置。
type Config struct {
	PageURL      string   `toml:"page_url"`
	X86PageURL   string   `toml:"x86_page_url"`
	EnableX86    bool     `toml:"enable_x86"`
	OutputDir    string   `toml:"output_dir"`
	Concurrency  int      `toml:"concurrency"`
	UserAgent    string   `toml:"user_agent"`
	DownloadHost string   `toml:"download_host"`
	SkipVerified bool     `toml:"skip_verified"`
	Retry        int      `toml:"retry"`
	Devices      []Device `toml:"devices"`
}

// Device 白名单中的一条匹配规则。
type Device struct {
	Pattern string `toml:"pattern"`
}

// DefaultConfig 返回带合理默认值的配置。
func DefaultConfig() Config {
	return Config{
		PageURL:      "https://fnnas.com/download-arm",
		X86PageURL:   "https://fnnas.com/download",
		EnableX86:    true,
		OutputDir:    "downloads",
		Concurrency:  4,
		UserAgent:    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		DownloadHost: "http://thunder.liveupdate.fnnas.com:8080",
		SkipVerified: true,
		Retry:        5,
		Devices:      nil,
	}
}

// LoadConfig 从 path 读取 TOML 配置；缺失时写出默认配置到该路径。
// 返回值: (配置, 是否新生成了默认配置, 错误)
func LoadConfig(path string) (cfg Config, generated bool, err error) {
	cfg = DefaultConfig()
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		// 写出一份默认配置方便用户编辑
		if werr := writeDefaultConfig(path); werr != nil {
			return cfg, false, werr
		}
		return cfg, true, nil
	}
	if _, derr := toml.DecodeFile(path, &cfg); derr != nil {
		return cfg, false, derr
	}
	// 校正非法值
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Retry < 0 {
		cfg.Retry = 0
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "downloads"
	}
	if cfg.PageURL == "" {
		cfg.PageURL = "https://fnnas.com/download-arm"
	}
	if cfg.X86PageURL == "" {
		cfg.X86PageURL = "https://fnnas.com/download"
	}
	return cfg, false, nil
}

// writeDefaultConfig 写出一份带注释的示例配置。TOML 库无法保留注释，
// 所以这里用模板手写。
func writeDefaultConfig(path string) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	const sample = `# fnnas 下载器配置文件（ARM + x86）
# 生成后可自行修改再次运行。

# ARM 下载页面地址（用于解析 ARM 设备清单）
page_url = "https://fnnas.com/download-arm"

# x86 下载页面地址（用于解析 fnOS x86_64 安装 ISO）
# 设为空字符串或把 enable_x86 改为 false 可关闭 x86 抓取。
x86_page_url = "https://fnnas.com/download"

# 是否同时抓取并下载 x86 的 fnOS 安装 ISO
enable_x86 = true

# 所有镜像文件平铺存放到该目录
output_dir = "downloads"

# 同时下载的并发数
concurrency = 4

# 抓取页面 / 下载文件时使用的 User-Agent
user_agent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

# 实际下载源。直链 https://iso.liveupdate.fnnas.com 在部分网络环境会被
# CDN 拦截(403)；迅雷镜像 http://thunder.liveupdate.fnnas.com:8080 路径
# 相同且支持 Range 断点续传。程序会把页面直链的 host 替换为此值。
# 留空则直接使用页面上的直链。
download_host = "http://thunder.liveupdate.fnnas.com:8080"

# 已存在且 MD5 校验通过的文件是否跳过下载
skip_verified = true

# 单个文件下载失败后的重试次数
retry = 5

# 开启下载的设备型号白名单。每条 pattern 做不区分大小写的子串匹配，
# 会对设备的 name / original_name / url 三个字段都尝试匹配。
#   - 留空（不写 [[devices]] 段）= 不下载任何设备
#   - 写一条 pattern = "*"        = 下载全部设备
#
# 示例：
# [[devices]]
# pattern = "nanopi-r5s"
#
# [[devices]]
# pattern = "rock-5b"
#
# [[devices]]
# pattern = "友善"
#
# x86 的 fnOS ISO 条目名形如 "飞牛 fnOS x86_64 1.2.0800"，
# url 含 /x86_64/trim/，可用以下 pattern 单独匹配：
# [[devices]]
# pattern = "x86"
`
	return os.WriteFile(path, []byte(sample), 0o644)
}
