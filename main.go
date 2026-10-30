// Command internet-send starts a LAN file-transfer hub. Every device on the
// same Wi-Fi or hotspot opens the printed address (or scans the QR code) in a
// browser to send files and text to each other — no app install needed.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/AlanSmith20091211/internet-send/internal/netutil"
	"github.com/AlanSmith20091211/internet-send/internal/server"
)

var version = "dev"

func main() {
	port := flag.Int("port", 8787, "监听端口")
	dir := flag.String("dir", defaultDir(), "接收文件的保存目录")
	pin := flag.String("pin", "", "访问口令（可选）；设为 auto 则随机生成 6 位数字")
	noQR := flag.Bool("no-qr", false, "不在终端打印二维码")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()

	if *showVersion {
		fmt.Println("internet-send", version)
		return
	}
	if *pin == "auto" {
		*pin = randomPIN()
	}
	portSet := false
	flag.Visit(func(f *flag.Flag) { portSet = portSet || f.Name == "port" })

	ln, err := listen(*port, !portSet)
	if err != nil {
		fatal("无法监听端口：%v", err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	var urls []string
	for _, ip := range netutil.LANIPv4() {
		urls = append(urls, "http://"+net.JoinHostPort(ip.String(), strconv.Itoa(actualPort)))
	}
	hostname, _ := os.Hostname()

	srv, err := server.New(server.Config{
		Dir: *dir, PIN: *pin, Version: version, Hostname: hostname, URLs: urls,
	})
	if err != nil {
		fatal("无法创建保存目录：%v", err)
	}

	printBanner(urls, actualPort, *dir, *pin, !*noQR)

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("服务异常退出：%v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	fmt.Println("\n正在退出…")
	srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}

// listen binds the port; if the default port is busy it tries the next few.
func listen(port int, fallback bool) (net.Listener, error) {
	tries := 1
	if fallback {
		tries = 20
	}
	var err error
	for i := 0; i < tries; i++ {
		var ln net.Listener
		if ln, err = net.Listen("tcp", fmt.Sprintf(":%d", port+i)); err == nil {
			return ln, nil
		}
	}
	return nil, err
}

func printBanner(urls []string, port int, dir, pin string, qr bool) {
	fmt.Printf("\n  Internet Send %s — 局域网文件传输\n\n", version)
	fmt.Printf("  本机打开:   http://localhost:%d\n", port)
	if len(urls) == 0 {
		fmt.Println("  ⚠ 没有检测到局域网地址，请确认已连接 Wi-Fi 或手机热点")
	}
	for _, u := range urls {
		fmt.Printf("  其他设备:   %s\n", u)
	}
	if pin != "" {
		fmt.Printf("  访问口令:   %s\n", pin)
	}
	fmt.Printf("  文件保存到: %s\n", dir)

	if qr && len(urls) > 0 {
		link := urls[0]
		if pin != "" {
			link += "/?pin=" + url.QueryEscape(pin)
		}
		if q, err := qrcode.New(link, qrcode.Low); err == nil {
			fmt.Println("\n  用手机扫码打开（需连接同一网络）：")
			fmt.Println(q.ToSmallString(false))
		}
	}
	fmt.Println("  按 Ctrl+C 退出")
}

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "InternetSend"
	}
	if st, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && st.IsDir() {
		return filepath.Join(home, "Downloads", "InternetSend")
	}
	return filepath.Join(home, "InternetSend")
}

func randomPIN() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
	return fmt.Sprintf("%06d", n)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "错误: "+format+"\n", args...)
	os.Exit(1)
}
