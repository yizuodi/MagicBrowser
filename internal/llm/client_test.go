package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseHandler 把字符串按给定块数拆成 SSE data 行返回。
func sseHandler(chunks []string, done bool, delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			flusher.Flush()
			time.Sleep(delay)
		}
		if done {
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		}
	}
}

func TestStreamChatAssemblesDeltas(t *testing.T) {
	srv := httptest.NewServer(sseHandler([]string{"<ht", "ml>", " hel", "lo"}, true, 0))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	var sb strings.Builder
	err := c.StreamChat(context.Background(), nil, func(d string) error {
		sb.WriteString(d)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if sb.String() != "<html> hello" {
		t.Fatalf("assembled %q", sb.String())
	}
}

func TestStreamChatHandlesJunkLinesAndMultibyte(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": heartbeat comment\n\n")
		fmt.Fprint(w, "event: ping\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"页面\"}}]}\n\n")
		// SSE 事件行之间穿插注释行/事件名行，验证解析器只认 data: 前缀。
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"内容\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	var got strings.Builder
	if err := c.StreamChat(context.Background(), nil, func(d string) error {
		got.WriteString(d)
		return nil
	}); err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if got.String() != "页面内容" {
		t.Fatalf("got %q", got.String())
	}
}

func TestStreamChatUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	err := c.StreamChat(context.Background(), nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestStreamChatContextCancel(t *testing.T) {
	release := make(chan struct{})
	requestSeen := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen <- struct{}{}
		flusher := w.(http.Flusher)
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-release:
				return
			default:
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.StreamChat(ctx, nil, func(string) error { return nil })
	}()
	<-requestSeen
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected context error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamChat did not return after cancel")
	}
	close(release)
}

func TestChatNonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"summary text"}}]}`)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, 1200)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out != "summary text" {
		t.Fatalf("got %q", out)
	}
}

func TestParseSSEVeryLongLine(t *testing.T) {
	// 单条 delta 内容 200KB（超过默认 Scanner 64KB 上限），验证 ReadBytes 路径。
	big := strings.Repeat("a", 200*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", big)
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m"}
	var n int
	err := c.StreamChat(context.Background(), nil, func(d string) error {
		n += len(d)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if n != len(big) {
		t.Fatalf("got %d bytes, want %d", n, len(big))
	}
}
