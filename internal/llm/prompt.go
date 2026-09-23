package llm

import (
	"fmt"
	"strings"
)

// StyleSchema 附加在系统提示词后的输出格式约定，与 DefaultSystemPrompt 配合。
const StyleSchema = `

【输出格式 Schema】
- 首个字符必须是 "<"（即 <!DOCTYPE html>），最后一个字符是 ">"（</html>）。
- 引入 Tailwind：<script src="{{CDN_BASE}}"></script> 放在 <head> 内。
- 深度交互标记：data-magic-nav（元素/链接，配合 data-magic-url 或 href 指向目标路径）、
  data-magic-form（提交后跳转的表单）、data-magic-local（纯本地表单，不触发跳转）。`

// BuildSystemPrompt 把后台配置的系统提示词与 CDN 地址拼装为最终 system 消息。
func BuildSystemPrompt(userPrompt, cdnBase string) string {
	p := userPrompt
	if p == "" {
		p = "你是 Magic Browser 的页面渲染引擎，按用户给的网址生成完整 HTML 页面。"
	}
	schema := strings.ReplaceAll(StyleSchema, "{{CDN_BASE}}", cdnBase)
	if !strings.Contains(p, "{{CDN_BASE}}") {
		return p + schema
	}
	return strings.ReplaceAll(p, "{{CDN_BASE}}", cdnBase)
}

// stripState 用于 fenceStripper 的状态机。
type stripState struct {
	started bool   // 已越过开头围栏区
	head    string // 扣留的头部（未确定是否含围栏）
}

// newFenceStripper 返回一个剥除 LLM 输出首尾 markdown 围栏的助手。
// 前 32 字节先扣留：若发现 ```html / ``` 围栏则丢弃至行尾；一旦出现 '<' 即放行。
type fenceStripper struct{ s stripState }

func newFenceStripper() *fenceStripper { return &fenceStripper{} }

// NewFenceStripper 是 newFenceStripper 的导出形式（server 流泵使用）。
func NewFenceStripper() *fenceStripper { return newFenceStripper() }

// Feed 输入一个增量，返回应当下发的内容（可能为空）。
func (f *fenceStripper) Feed(delta string) string {
	if !f.s.started {
		f.s.head += delta
		if len(f.s.head) < 32 && !strings.ContainsRune(f.s.head, '<') {
			return "" // 继续扣留
		}
		f.s.started = true
		return stripFence(f.s.head)
	}
	return delta
}

// Finish 返回流结束时仍被扣留的内容（极短输出场景）。
func (f *fenceStripper) Finish() string {
	if !f.s.started {
		f.s.started = true
		return stripFence(f.s.head)
	}
	return ""
}

// stripFence 处理开头片段：去掉 ```html、``` 等围栏前缀。
func stripFence(s string) string {
	t := strings.TrimLeft(s, " \t\n\r")
	if strings.HasPrefix(t, "```") {
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			t = t[nl+1:]
		} else {
			t = "" // 围栏行还没结束，丢弃（内容下一 delta 才来）
		}
	}
	return t
}

// StripTrailingFence 去掉完整输出的尾部围栏（```）与多余空白。
func StripTrailingFence(s string) string {
	t := strings.TrimRight(s, " \t\n\r")
	if strings.HasSuffix(t, "```") {
		t = strings.TrimRight(t[:len(t)-3], " \t\n\r")
	}
	return t
}

// FormatAction 把用户操作渲染为提示词中的文本描述。
func FormatAction(actType, url string, form map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "{type:%q, url:%q", actType, url)
	if len(form) > 0 {
		b.WriteString(", form:{")
		first := true
		for k, v := range form {
			if !first {
				b.WriteString(", ")
			}
			first = false
			fmt.Fprintf(&b, "%q:%q", k, v)
		}
		b.WriteString("}")
	}
	b.WriteString("}")
	return b.String()
}
