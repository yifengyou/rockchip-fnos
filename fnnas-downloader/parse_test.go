package main

import (
	"encoding/json"
	"testing"
)

// encodeNextFChunk 把一段文本编码成 Next.js RSC 数据流里
// self.__next_f.push([1,"<chunk>"]) 调用的 HTML 片段。
// 便于在测试里构造与真实页面同结构的合成 HTML。
func encodeNextFChunk(chunk string) string {
	b, _ := json.Marshal(chunk) // 带 JS 字符串转义
	// b 形如 "...\"..."，直接拼成 push 调用
	return "self.__next_f.push([1," + string(b) + "])"
}

// TestParseDevicesARM 用合成 RSC 流验证 ARM 解析：去重 + arch 标记 + 排序。
func TestParseDevicesARM(t *testing.T) {
	// 两条记录，其中 "友善 NanoPi R5S" 重复一次（页面会把列表渲染两次）。
	stream := encodeNextFChunk(
		`{"name":"瑞莎 Radxa ROCK 5B","url":"https://iso.liveupdate.fnnas.com/arm/image/1.2.0701/rock-5b/fnos_Mainland-PE_arm_1.2.0701_rock-5b_2882.img.gz","hash":"1c94fab745066724e6cd5c500aba5c18","thunder":"thunder://QUFod","originalName":"瑞莎 Radxa ROCK 5B"}` +
			`{"name":"友善 NanoPi R5S","url":"https://iso.liveupdate.fnnas.com/arm/image/1.2.0701/nanopi-r5s/fnos_Mainland-PE_arm_1.2.0701_nanopi-r5s_2882.img.gz","hash":"aaabbbcccdddee","thunder":"thunder://QUFod","originalName":"友善 NanoPi R5S"}` +
			`{"name":"友善 NanoPi R5S","url":"https://iso.liveupdate.fnnas.com/arm/image/1.2.0701/nanopi-r5s/fnos_Mainland-PE_arm_1.2.0701_nanopi-r5s_2882.img.gz","hash":"aaabbbcccdddee","thunder":"thunder://QUFod","originalName":"友善 NanoPi R5S"}`,
	)
	devices := ParseDevices(stream)
	if len(devices) != 2 {
		t.Fatalf("去重后应解析到 2 个设备，实际 %d", len(devices))
	}
	// 排序后 "友善..." 在 "瑞莎..." 之前
	if devices[0].Name != "友善 NanoPi R5S" || devices[1].Name != "瑞莎 Radxa ROCK 5B" {
		t.Errorf("排序错误: %q, %q", devices[0].Name, devices[1].Name)
	}
	for _, d := range devices {
		if d.Arch != "arm" {
			t.Errorf("ARM 条目 %q arch=%q，期望 arm", d.Name, d.Arch)
		}
		if d.URL == "" || d.MD5 == "" {
			t.Errorf("ARM 条目 %q 缺少 url 或 md5", d.Name)
		}
	}
}

// TestParseX86 用合成 RSC 流验证 x86 解析：
//   - 只提取 download.url 为 .iso 的 fnOS ISO 条目
//   - 排除飞牛同步的 windowsUrl/macIntelUrl/macAppleUrl 块
//   - 排除飞牛 App 的 android.url（.apk）块
//   - arch 标记为 x86，version 提取正确
func TestParseX86(t *testing.T) {
	stream := encodeNextFChunk(
		// fnOS ISO（应被提取）
		`{"download":{"version":"1.2.0800","url":"https://iso.liveupdate.fnnas.com/x86_64/trim/fnos_Mainland-PE_x86_1.2.0800_2778.iso","hash":"851da934d172c71e48eec8335874aa3a","thunder":"thunder://QUFod"}}` +
			// 重复一次（页面渲染两次）
			`{"download":{"version":"1.2.0800","url":"https://iso.liveupdate.fnnas.com/x86_64/trim/fnos_Mainland-PE_x86_1.2.0800_2778.iso","hash":"851da934d172c71e48eec8335874aa3a","thunder":"thunder://QUFod"}}` +
			// 飞牛同步（download 块但 url 字段是 windowsUrl/macIntelUrl，不是 url -> 不应匹配）
			`{"download":{"version":"0.2.3","windowsUrl":"https://iso.liveupdate.fnnas.com/pc/fn-sync_0.2.3_x64-setup.exe","macIntelUrl":"https://iso.liveupdate.fnnas.com/pc/fn-sync_0.2.3_x86_64.dmg","macAppleUrl":"https://iso.liveupdate.fnnas.com/pc/fn-sync_0.2.3_aarch64.dmg"}}` +
			// 飞牛 App（android.url 是 .apk，结构不同 -> 不应匹配）
			`{"android":{"version":"1.37.1","url":"https://iso.liveupdate.fnnas.com/x86_64/app/fnos_1.37.1_5510131717.apk"},"ios":{"version":"1.37.0"}}` +
			// 飞牛 TV（mediaAndroid.url 是 .apk -> 不应匹配）
			`{"mediaAndroid":{"url":"https://iso.liveupdate.fnnas.com/x86_64/app/FN_Meida_1.12.2_01110132000.apk","version":"1.12.2"},"mediaIOS":{"version":"1.12.2"}}`,
	)
	entries := ParseX86(stream)
	if len(entries) != 1 {
		t.Fatalf("应只解析到 1 个 ISO 条目，实际 %d", len(entries))
	}
	d := entries[0]
	if d.Arch != "x86" {
		t.Errorf("arch=%q，期望 x86", d.Arch)
	}
	if d.Version != "1.2.0800" {
		t.Errorf("version=%q，期望 1.2.0800", d.Version)
	}
	wantURL := "https://iso.liveupdate.fnnas.com/x86_64/trim/fnos_Mainland-PE_x86_1.2.0800_2778.iso"
	if d.URL != wantURL {
		t.Errorf("url=%q，期望 %q", d.URL, wantURL)
	}
	if d.MD5 != "851da934d172c71e48eec8335874aa3a" {
		t.Errorf("md5=%q，期望 851da934...", d.MD5)
	}
	if d.URL[len(d.URL)-4:] != ".iso" {
		t.Errorf("url 不是 .iso: %s", d.URL)
	}
	if d.Name != "飞牛 fnOS x86_64 1.2.0800" {
		t.Errorf("name=%q，期望 飞牛 fnOS x86_64 1.2.0800", d.Name)
	}
}

// TestParseX86NoVersion 验证 version 为空时条目名仍可生成。
func TestParseX86NoVersion(t *testing.T) {
	stream := encodeNextFChunk(
		`{"download":{"version":"","url":"https://iso.liveupdate.fnnas.com/x86_64/trim/fnos_Mainland-PE_x86_9.9.9999_9999.iso","hash":"deadbeef","thunder":"thunder://QUFod"}}`,
	)
	entries := ParseX86(stream)
	if len(entries) != 1 {
		t.Fatalf("应解析到 1 个条目，实际 %d", len(entries))
	}
	if entries[0].Name != "飞牛 fnOS x86_64" {
		t.Errorf("version 为空时 name=%q，期望 飞牛 fnOS x86_64", entries[0].Name)
	}
}

// TestMatchWhitelistX86 验证白名单能命中 x86 条目。
func TestMatchWhitelistX86(t *testing.T) {
	d := DeviceEntry{
		Name:         "飞牛 fnOS x86_64 1.2.0800",
		URL:          "https://iso.liveupdate.fnnas.com/x86_64/trim/fnos_Mainland-PE_x86_1.2.0800_2778.iso",
		OriginalName: "飞牛 fnOS x86_64 1.2.0800",
		Arch:         "x86",
	}
	cases := []struct {
		patterns []string
		want     bool
	}{
		{[]string{"*"}, true},
		{[]string{"x86"}, true},
		{[]string{"x86_64"}, true},
		{[]string{"fnOS x86_64"}, true},
		{[]string{"trim"}, true},
		{[]string{"nanopi-r5s"}, false},
		{[]string{}, false},
	}
	for _, c := range cases {
		got := MatchWhitelist(d, c.patterns)
		if got != c.want {
			t.Errorf("patterns=%v => %v, want %v", c.patterns, got, c.want)
		}
	}
}
