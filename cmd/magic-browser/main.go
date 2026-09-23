// magic-browser 是 Magic Browser 的可执行入口：
// 一个编译期嵌入了全部前端资产的单一二进制。
package main

import (
	"crypto/rand"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"magic/internal/auth"
	"magic/internal/config"
	"magic/internal/history"
	"magic/internal/server"
)

func main() {
	var (
		addr = flag.String("addr", ":8080", "监听地址")
		data = flag.String("data", "./data", "数据目录（配置与密钥）")
	)
	flag.Parse()

	// 软内存上限：GC 会更积极地把堆压回预算内（目标 <50MB 的一部分）。
	debug.SetMemoryLimit(48 << 20)

	// 加载/初始化配置。
	cfgPath := filepath.Join(*data, "config.json")
	cfgStore, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	cfg := cfgStore.Get()

	// 首次启动：打印初始管理员密码并写提示文件。
	if cfg.InitialAdminPassword != "" {
		hintPath := filepath.Join(*data, "initial_admin_password.txt")
		_ = os.WriteFile(hintPath, []byte(cfg.InitialAdminPassword+"\n"), 0o600)
		log.Printf("====================================================")
		log.Printf("首次启动：已生成初始管理员密码: %s", cfg.InitialAdminPassword)
		log.Printf("（也可在 %s 查看，登录后台后请立即修改）", hintPath)
		log.Printf("====================================================")
	}

	// HMAC 签名密钥：持久化在数据目录，重启不失效。
	secretPath := filepath.Join(*data, "secret.key")
	secret, err := loadOrCreateSecret(secretPath)
	if err != nil {
		log.Fatalf("初始化签名密钥失败: %v", err)
	}

	// 页面历史持久化存储（重启不丢；索引重建只解码元数据，内存有界）。
	hist, err := history.Open(filepath.Join(*data, "pages.jsonl"), history.DefaultMax)
	if err != nil {
		log.Fatalf("初始化历史存储失败: %v", err)
	}
	defer hist.Close()

	srv := server.New(cfgStore, auth.NewTokens(secret), hist)

	stop := make(chan struct{})
	srv.StartSweeper(stop)

	// 支持 -addr 逗号分隔多地址（如 ":8080,127.0.0.1:8090"），
	// 同一 Handler 同时挂到多个 listener。
	handler := srv.Handler()
	var listeners []net.Listener
	for _, a := range splitAddrs(*addr) {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			log.Fatalf("监听 %s 失败: %v", a, err)
		}
		listeners = append(listeners, ln)
		log.Printf("Magic Browser 已启动: http://%s （数据目录 %s）", displayHost(a), *data)
	}

	httpSrv := &http.Server{Handler: handler}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		close(stop)
		httpSrv.Close()
	}()

	// 首个 listener 由主协程服务，其余各起一个 goroutine；任一退出错误都记录。
	errCh := make(chan error, len(listeners))
	for i, ln := range listeners {
		if i == 0 {
			continue
		}
		go func(l net.Listener) { errCh <- httpSrv.Serve(l) }(ln)
	}
	if err := httpSrv.Serve(listeners[0]); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务退出: %v", err)
	}
}

// splitAddrs 拆分逗号分隔的监听地址列表，空白项跳过。
func splitAddrs(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		out = []string{":8080"}
	}
	return out
}

// displayHost 把监听地址渲染成可点击的展示形式（通配地址换成 localhost）。
func displayHost(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil && (host == "0.0.0.0" || host == "") {
		return "localhost:" + port
	}
	return addr
}

// loadOrCreateSecret 加载或生成 32 字节 HMAC 密钥。
func loadOrCreateSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}
