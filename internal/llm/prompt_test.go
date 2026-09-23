package llm

import (
	"strings"
	"testing"
)

func TestFenceStripperLeadingFence(t *testing.T) {
	f := newFenceStripper()
	var out string
	for _, d := range []string{"```ht", "ml\n", "<!DOC", "TYPE html>"} {
		out += f.Feed(d)
	}
	out += f.Finish()
	if out != "<!DOCTYPE html>" {
		t.Fatalf("got %q", out)
	}
}

func TestFenceStripperNoFence(t *testing.T) {
	f := newFenceStripper()
	var out string
	for _, d := range []string{"<htm", "l><b", "ody>"} {
		out += f.Feed(d)
	}
	out += f.Finish()
	if out != "<html><body>" {
		t.Fatalf("got %q", out)
	}
}

func TestFenceStripperShortOutput(t *testing.T) {
	// 极短输出：全部结束后才由 Finish 冲出。
	f := newFenceStripper()
	var out string
	for _, d := range []string{"<p>", "hi"} {
		out += f.Feed(d)
	}
	out += f.Finish()
	if out != "<p>hi" {
		t.Fatalf("got %q", out)
	}
}

func TestFenceStripperBareFenceOnly(t *testing.T) {
	// 只有围栏还没等到内容就结束：不应输出任何东西。
	f := newFenceStripper()
	out := f.Feed("```html") + f.Finish()
	if out != "" {
		t.Fatalf("got %q", out)
	}
}

func TestStripTrailingFence(t *testing.T) {
	cases := map[string]string{
		"<html></html>```":   "<html></html>",
		"<html></html>\n```": "<html></html>",
		"<html></html>":      "<html></html>",
	}
	for in, want := range cases {
		if got := StripTrailingFence(in); got != want {
			t.Errorf("StripTrailingFence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	out := BuildSystemPrompt("生成页面", "https://cdn.example.com")
	if !strings.Contains(out, "https://cdn.example.com") || !strings.Contains(out, "生成页面") {
		t.Fatalf("unexpected: %s", out)
	}
	// 自定义提示词带 {{CDN_BASE}} 占位符时也应被替换。
	out = BuildSystemPrompt("用 {{CDN_BASE}} 引入样式", "https://x.com")
	if strings.Contains(out, "{{CDN_BASE}}") {
		t.Fatal("placeholder not replaced")
	}
}
