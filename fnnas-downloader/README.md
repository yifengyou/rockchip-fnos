# fnnas ARM 下载器

从 [https://fnnas.com/download-arm](https://fnnas.com/download-arm) 解析所有 ARM 开发板/设备的下载地址，
并按配置文件里的型号白名单，把指定镜像下载到本地目录。

支持 **配置驱动的型号选择**、**断点续传**、**并发下载**、**MD5 校验** 与 **跳过已校验文件**。

## 特性

| 特性 | 说明 |
|------|------|
| 型号白名单 | 在 `config.toml` 里用 `[[devices]]` 段开启要下载的型号，支持子串/通配匹配 |
| 断点续传 | 下载中断后再次运行会从 `.part` 文件已有大小处继续（HTTP Range） |
| 并发下载 | 多个设备镜像同时下载，并发数可配置 |
| MD5 校验 | 下载完成后用页面提供的 MD5 校验，失败自动重试 |
| 跳过已校验文件 | 本地已存在且 MD5 匹配的文件直接跳过，避免重复下载 |
| 下载源重写 | 直链 `https://iso.liveupdate.fnnas.com` 在部分网络会被 CDN 拦截（403），程序默认改用可访问且支持 Range 的迅雷镜像 `http://thunder.liveupdate.fnnas.com:8080`（路径相同） |

## 构建

```bash
cd scripts/fnnas-downloader
go build -o fnnas-downloader .
```

依赖：`github.com/BurntSushi/toml`（已在 `go.mod` 中）。

## 使用

### 1. 生成默认配置

首次运行会自动生成 `config.toml`：

```bash
./fnnas-downloader
# 未找到配置文件，已生成默认配置: config.toml
```

### 2. 编辑配置选择型号

打开 `config.toml`，在 `[[devices]]` 段里添加要下载的设备：

```toml
page_url       = "https://fnnas.com/download-arm"
output_dir     = "downloads"
concurrency    = 4
download_host  = "http://thunder.liveupdate.fnnas.com:8080"
skip_verified  = true
retry          = 5

# 下载全部设备
[[devices]]
pattern = "*"

# 或按型号匹配（不区分大小写，对 name / original_name / url 做子串匹配）
[[devices]]
pattern = "nanopi-r5s"

[[devices]]
pattern = "rock-5b"

[[devices]]
pattern = "orangepi-5"
```

### 3. 运行

```bash
# 正式下载
./fnnas-downloader

# 指定配置文件
./fnnas-downloader -config my-config.toml

# 仅列出页面解析到的所有设备（不下载）
./fnnas-downloader -list

# 按白名单过滤后展示将下载哪些，不真正下载
./fnnas-downloader -dry-run
```

## 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-config` | `config.toml` | 配置文件路径 |
| `-list` | `false` | 仅列出页面所有设备，不下载 |
| `-dry-run` | `false` | 按白名单过滤后展示将下载哪些，不真正下载 |

## 配置项说明

| 字段 | 说明 |
|------|------|
| `page_url` | 下载页面地址（用于解析设备清单） |
| `output_dir` | 所有镜像文件平铺存放到该目录 |
| `concurrency` | 同时下载的并发数 |
| `user_agent` | 抓取页面 / 下载文件时使用的 User-Agent |
| `download_host` | 实际下载源；非空时把页面直链的 host 替换为此值。留空则直接用页面直链 |
| `skip_verified` | 已存在且 MD5 校验通过的文件是否跳过 |
| `retry` | 单个文件下载失败后的重试次数 |
| `[[devices]]` | 白名单规则，`pattern` 做不区分大小写的子串匹配；`"*"` 表示下载全部 |

## 输出目录结构

所有文件平铺存放到 `output_dir`，下载中使用 `.part` 后缀的临时文件，校验通过后重命名为最终文件名：

```
downloads/
├── fnos_Mainland-PE_arm_1.2.0701_nanopi-r5s_2882.img.gz          # 完成并校验通过
├── fnos_Mainland-PE_arm_1.2.0701_rock-5b_2882.img.gz.part        # 下载中 / 可续传
└── fnos_Mainland-PE_arm_1.2.0701_orangepi-5_2882.img.gz
```

文件名直接取自下载 URL 的 basename，自带设备标识，无需额外子目录。

## 中断与续传

- 下载过程中按 `Ctrl+C` 会优雅退出，当前已写入的数据保留在 `.part` 文件中。
- 再次运行会从断点继续，不会重头下载。
- 如果某个 `.part` 校验失败（MD5 不匹配），会删除后重试。

## 示例输出

```
抓取页面: https://fnnas.com/download-arm
解析到 68 个 ARM 设备
白名单命中 3 个设备

开始下载到 downloads (并发 4)
──────────────────────────────────────────────
[友善 NanoPi R5S]            开始下载 (第 1 次) http://thunder.liveupdate.fnnas.com:8080/...
[友善 NanoPi R5S]            进度 29% (534.5MB / 1.8GB, 9.1MB/s)
[瑞莎 Radxa ROCK 5B]         断点续传: 从 257.6MB 继续
[友善 NanoPi R5S]            下载完成 1.8GB，用时 3m12s
[友善 NanoPi R5S]            校验 MD5 ...
[友善 NanoPi R5S]            MD5 校验通过
[友善 NanoPi R5S]            ✓ 下载完成
──────────────────────────────────────────────
完成: 成功 3, 跳过 0, 失败 0 (共 3)
```
