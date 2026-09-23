package session

import (
	"fmt"
	"strings"
)

// Msg 是建构出的 LLM 消息（与 llm.Message 同构，避免包循环依赖）。
type Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// BuildInput 描述一次生成请求。
type BuildInput struct {
	SystemPrompt string // 已由 llm.BuildSystemPrompt 拼装完成
	URL          string // 本次目标 URL（已 CleanURL）
	Action       *Action
}

// BuildMessages 根据会话历史与本次请求建构发给 LLM 的消息序列：
//
//	system: SystemPrompt
//	system(可选): STATE SUMMARY（压缩过的历史）
//	user: 历史 URL 清单 + 上一页 HTML 尾部 + 上一操作 + 本次请求
func (s *Session) BuildMessages(in BuildInput) []Msg {
	snap := s.Snapshot()
	var msgs []Msg
	msgs = append(msgs, Msg{Role: "system", Content: in.SystemPrompt})
	if snap.Compressed && snap.Summary != "" {
		msgs = append(msgs, Msg{Role: "system", Content: "STATE SUMMARY（此前浏览历史的压缩摘要，请保持连贯性）:\n" + snap.Summary})
	}

	var b strings.Builder
	// 历史浏览路径（帮助 LLM 理解状态机位置）。
	if len(snap.Pages) > 0 {
		b.WriteString("历史浏览路径:\n")
		for i, p := range snap.Pages {
			fmt.Fprintf(&b, "  %d. %s", i+1, p.URL)
			if i == len(snap.Pages)-1 {
				b.WriteString("  (当前页)")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	// 上一页 HTML（有截断保尾，注意提示 LLM 这是尾部片段）。
	if n := len(snap.Pages); n > 0 {
		last := snap.Pages[n-1]
		html := last.HTML
		const tail = 6000 // 上一页 HTML 提示词预算（字符）
		truncated := false
		if len(html) > tail {
			html = html[len(html)-tail:]
			truncated = true
		}
		if truncated {
			b.WriteString("上一页 HTML（开头被截断，这是尾部片段）:\n")
		} else {
			b.WriteString("上一页 HTML:\n")
		}
		b.WriteString(html)
		b.WriteString("\n\n")
	}
	// 最近的操作记录。
	if m := len(snap.Actions); m > 0 {
		lo := m - 4
		if lo < 0 {
			lo = 0
		}
		b.WriteString("最近的用户操作:\n")
		for _, a := range snap.Actions[lo:] {
			fmt.Fprintf(&b, "  %s %s (element=%s)\n", a.Type, a.URL, a.Element)
		}
		b.WriteString("\n")
	}
	// 本次请求。
	fmt.Fprintf(&b, "请求: 生成网址 [%s] 对应的完整页面。", in.URL)
	if in.Action != nil {
		fmt.Fprintf(&b, " 用户刚在上一页执行了操作 %s（目标 %s）。",
			in.Action.Type, in.Action.URL)
		if len(in.Action.Form) > 0 {
			b.WriteString("表单数据: ")
			first := true
			for k, v := range in.Action.Form {
				if !first {
					b.WriteString("; ")
				}
				first = false
				fmt.Fprintf(&b, "%s=%q", k, v)
			}
			b.WriteString("。")
		}
	}
	b.WriteString("\n直接输出 HTML 文档本身。")
	msgs = append(msgs, Msg{Role: "user", Content: b.String()})
	return msgs
}
