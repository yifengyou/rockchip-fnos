package main

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSplitSize(t *testing.T) {
	g := int64(1024 * 1024 * 1024)
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"1024", 1024},
		{"1k", 1024},
		{"1K", 1024},
		{"1m", 1024 * 1024},
		{"2M", 2 * 1024 * 1024},
		{"1g", 1024 * 1024 * 1024},
		{"1.8G", int64(1.8 * float64(g))},
		{"1.5g", int64(1.5 * float64(g))},
		{"1t", 1024 * 1024 * 1024 * 1024},
	}
	for _, c := range cases {
		got, err := parseSplitSize(c.in)
		if err != nil {
			t.Errorf("parseSplitSize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSplitSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSplitSizeInvalid(t *testing.T) {
	bad := []string{"abc", "1.2.3G", "--1G", "G"}
	for _, s := range bad {
		if _, err := parseSplitSize(s); err == nil {
			t.Errorf("parseSplitSize(%q) expected error, got nil", s)
		}
	}
}

// TestSplitAndReassemble 构造一个超过阈值的大文件，调用 splitFile 切分，
// 然后把分片按序合并并校验 MD5 等于原文件，且原文件已被删除。
func TestSplitAndReassemble(t *testing.T) {
	dir := t.TempDir()
	dl := &Downloader{OutputDir: dir, SplitSize: 100} // 每 100 字节一份

	// 原始数据 250 字节
	orig := make([]byte, 250)
	for i := range orig {
		orig[i] = byte(i)
	}
	final := filepath.Join(dir, "foo.img.gz")
	if err := os.WriteFile(final, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	// 期望 3 份 (100+100+50)
	d := DeviceEntry{URL: "https://iso.liveupdate.fnnas.com/arm/foo.img.gz"}
	if err := dl.splitFile(d, final); err != nil {
		t.Fatalf("splitFile: %v", err)
	}

	// 原文件应已删除
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("原文件应已删除，stat err=%v", err)
	}

	parts := dl.findParts(d)
	if len(parts) != 3 {
		t.Fatalf("期望 3 份分片，得到 %d", len(parts))
	}

	// 各分片大小应为 100, 100, 50
	sizes := []int64{100, 100, 50}
	for i, p := range parts {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() != sizes[i] {
			t.Errorf("分片 %d 大小 = %d, 期望 %d", i+1, st.Size(), sizes[i])
		}
	}

	// 合并分片并校验内容等于原始数据
	var got []byte
	for _, p := range parts {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b...)
	}
	if string(got) != string(orig) {
		t.Errorf("合并后内容与原文件不一致 (len got=%d, want=%d)", len(got), len(orig))
	}

	// MD5 校验
	h := md5.New()
	h.Write(orig)
	wantMD5 := hex.EncodeToString(h.Sum(nil))
	ok, err := verifyPartsMD5(parts, wantMD5)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("verifyPartsMD5 返回 false，期望 true")
	}
}

// TestSplitFileBelowThreshold 验证文件不超过阈值时不切分。
func TestSplitFileBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	dl := &Downloader{OutputDir: dir, SplitSize: 1000}

	orig := make([]byte, 50)
	final := filepath.Join(dir, "small.img.gz")
	if err := os.WriteFile(final, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	d := DeviceEntry{URL: "https://iso.liveupdate.fnnas.com/arm/small.img.gz"}
	if err := dl.splitFile(d, final); err != nil {
		t.Fatalf("splitFile: %v", err)
	}

	// 原文件应仍存在
	st, err := os.Stat(final)
	if err != nil {
		t.Fatalf("原文件应保留，stat err=%v", err)
	}
	if st.Size() != 50 {
		t.Errorf("原文件大小 = %d, 期望 50", st.Size())
	}
	parts := dl.findParts(d)
	if len(parts) != 0 {
		t.Errorf("不应有分片，得到 %d", len(parts))
	}
}

// TestFindParts 验证 findParts 只匹配 fileName-partNN 格式。
func TestFindParts(t *testing.T) {
	dir := t.TempDir()
	dl := &Downloader{OutputDir: dir}

	d := DeviceEntry{URL: "https://iso.liveupdate.fnnas.com/arm/foo.iso"}

	// 创建 3 份正确分片
	for _, n := range []int{1, 2, 3} {
		os.WriteFile(filepath.Join(dir, dl.partPathName(d, n)), []byte{byte(n)}, 0o644)
	}
	// 干扰文件：下载临时文件 .part、其它设备分片、无序号
	os.WriteFile(filepath.Join(dir, "foo.iso.part"), []byte{0}, 0o644)
	os.WriteFile(filepath.Join(dir, "bar.iso-part01"), []byte{0}, 0o644)
	os.WriteFile(filepath.Join(dir, "foo.iso-part"), []byte{0}, 0o644)
	os.WriteFile(filepath.Join(dir, "foo.iso-part1"), []byte{0}, 0o644)  // 1 位
	os.WriteFile(filepath.Join(dir, "foo.iso-part001"), []byte{0}, 0o644) // 3 位

	parts := dl.findParts(d)
	if len(parts) != 3 {
		t.Fatalf("期望 3 份分片，得到 %d: %v", len(parts), parts)
	}
	// 应按序号升序
	for i, p := range parts {
		want := filepath.Join(dir, dl.partPathName(d, i+1))
		if p != want {
			t.Errorf("分片 %d = %q, 期望 %q", i+1, p, want)
		}
	}
}

func TestPartSuffix(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "-part01"},
		{2, "-part02"},
		{10, "-part10"},
		{99, "-part99"},
	}
	for _, c := range cases {
		if got := partSuffix(c.n); got != c.want {
			t.Errorf("partSuffix(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
