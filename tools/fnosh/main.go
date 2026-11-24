package main

// 在路由器上跑的一次性 SSH 客户端，用面板那把只读密钥连 fnOS。
// 为什么要它：iStoreOS 自带的 dropbear 客户端连 fnOS 会 "Exited: String too long"，
// 而 Go 的 x/crypto/ssh 正常——采集器走的就是这条路，排查时也得走同一条路。
//
// 用法：
//
//	ssh root@router '/tmp/fnosh -key /etc/routerpanel/id_panel' < probe.sh

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	key := flag.String("key", "/etc/routerpanel/id_panel", "私钥路径")
	user := flag.String("user", "", "登录用户（必填，不预置任何人的用户名）")
	host := flag.String("host", "", "地址（必填）")
	port := flag.Int("port", 22, "端口")
	tmo := flag.Int("timeout", 30, "整体超时秒")
	flag.Parse()

	kb, err := os.ReadFile(*key)
	if err != nil {
		fmt.Println("读密钥失败:", err)
		os.Exit(1)
	}
	signer, err := ssh.ParsePrivateKey(kb)
	if err != nil {
		fmt.Println("密钥不可用:", err)
		os.Exit(1)
	}
	script, _ := io.ReadAll(os.Stdin)
	// 脚本走 base64 内联，不用 stdin：stdin 那套要么被 CombinedOutput 提前关掉，
	// 要么得自己管两条管道，排查工具不值得
	cmd := "echo " + base64.StdEncoding.EncodeToString(script) + " | base64 -d | bash 2>&1"
	cfg := &ssh.ClientConfig{
		User:            *user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
		Config: ssh.Config{
			Ciphers: []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com",
				"aes256-gcm@openssh.com", "chacha20-poly1305@openssh.com"},
		},
	}
	cli, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", *host, *port), cfg)
	if err != nil {
		fmt.Println("连接失败:", err)
		os.Exit(1)
	}
	defer cli.Close()

	sess, err := cli.NewSession()
	if err != nil {
		fmt.Println("开会话失败:", err)
		os.Exit(1)
	}
	defer sess.Close()
	type res struct {
		out string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		out, err := sess.CombinedOutput(cmd)
		ch <- res{string(out), err}
	}()
	select {
	case r := <-ch:
		fmt.Print(r.out)
		if r.err != nil {
			fmt.Println("(远端退出:", r.err, ")")
		}
	case <-time.After(time.Duration(*tmo) * time.Second):
		fmt.Println("超时")
		os.Exit(1)
	}
}
