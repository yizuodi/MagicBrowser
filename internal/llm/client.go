// Package llm 实现 OpenAI 兼容 Chat Completions 客户端：
// 流式（SSE 逐 delta 回调）与非流式（上下文压缩摘要用）两种调用形态。
// 上游请求绑定调用方传入的 ctx，客户端断开会级联取消上游连接。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message 是一条对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client 指向一个 OpenAI 兼容 endpoint。
type Client struct {
	BaseURL string // 如 https://api.groq.com/openai/v1
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type chatChoice struct {
	Message      Message `json:"message"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// apiError 是上游返回的非 2xx 响应。
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("llm: upstream %d: %s", e.Status, truncate(e.Body, 300))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	return c.httpClient().Do(req)
}

// StreamChat 发起流式补全，每收到一个内容增量调用 onDelta。
// 返回 nil 表示流正常结束（[DONE] 或上游关闭）。onDelta 返回错误会中断并返回该错误。
func (c *Client) StreamChat(ctx context.Context, msgs []Message, onDelta func(string) error) error {
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: msgs, Stream: true})
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return fmt.Errorf("llm: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &apiError{Status: resp.StatusCode, Body: string(raw)}
	}
	return parseSSE(resp.Body, onDelta)
}

// Chat 发起非流式补全并返回完整内容，用于上下文压缩摘要。
// maxTokens <= 0 表示不限制。
func (c *Client) Chat(ctx context.Context, msgs []Message, maxTokens int) (string, error) {
	payload := map[string]any{
		"model":    c.Model,
		"messages": msgs,
	}
	if maxTokens > 0 {
		payload["max_tokens"] = maxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return "", fmt.Errorf("llm: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", &apiError{Status: resp.StatusCode, Body: string(raw)}
	}
	var out chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("llm: decode: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: empty choices")
	}
	return out.Choices[0].Message.Content, nil
}

// parseSSE 增量解析上游 SSE 流：只关心 data: 前缀行的 JSON delta。
// 用 bufio.Reader.ReadBytes 而非 Scanner，容忍超长行（大 delta 一次下发）。
// 返回值 second bool = 是否收到 [DONE]（用于测试与上游提前断开的区分）。
func parseSSE(r io.Reader, onDelta func(string) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		trimmed = bytes.TrimSpace(trimmed)
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if len(payload) == 0 {
				goto next
			}
			if string(payload) == "[DONE]" {
				return nil
			}
			if content, ok := decodeDelta(payload); ok && content != "" {
				if err := onDelta(content); err != nil {
					return err
				}
			}
		}
	next:
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("llm: read stream: %w", err)
		}
	}
}

// decodeDelta 解析一条 data 载荷中的内容增量。
func decodeDelta(payload []byte) (string, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return "", false // 心跳/非 JSON 载荷直接跳过
	}
	if len(chunk.Choices) == 0 {
		return "", false
	}
	return chunk.Choices[0].Delta.Content, true
}

// DefaultTimeout 返回生成调用的兜底超时（秒）。
func DefaultTimeout() time.Duration { return 120 * time.Second }
