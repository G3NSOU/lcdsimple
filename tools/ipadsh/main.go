// iPad 面板机上的远程小工具：跑一条命令，或者把本地文件塞到设备上。
//
// 为什么要单独一个工具：路由器上的 dropbear 客户端和 iOS 9 的 OpenSSH 6.7 协商不下来
// （"String too long"），能连上的只有 x/crypto 那套写死的算法表，和采集器里一模一样。
//
// 用法：
//
//	go run ./tools/ipadsh -host <iPad地址> -key /path/id_ipad -c 'ls /var'
//	go run ./tools/ipadsh ... -put 本地文件:/设备上的路径
//
// -put 走 base64 而不是 SFTP：设备上没装 sftp-server 也能用，且只有一个连接。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func main() {
	host := flag.String("host", "", "iPad 地址")
	user := flag.String("user", "root", "SSH 用户")
	key := flag.String("key", "", "私钥路径")
	cmd := flag.String("c", "", "要跑的命令")
	put := flag.String("put", "", "本地文件:远端路径")
	flag.Parse()

	if *host == "" || *key == "" || (*cmd == "" && *put == "") {
		fmt.Println("要 -host -key，再加 -c 或 -put")
		os.Exit(2)
	}

	kb, err := os.ReadFile(*key)
	if err != nil {
		fmt.Println("读私钥:", err)
		os.Exit(1)
	}
	signer, err := ssh.ParsePrivateKey(kb)
	if err != nil {
		fmt.Println("私钥不可用:", err)
		os.Exit(1)
	}
	cfg := &ssh.ClientConfig{
		User:            *user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
		// iOS 9 的 OpenSSH 6.7：没有 dh-group14-sha256，主机密钥只有 ssh-rsa / ecdsa
		HostKeyAlgorithms: []string{"ssh-rsa", "ecdsa-sha2-nistp256"},
		Config: ssh.Config{
			KeyExchanges: []string{"ecdh-sha2-nistp256", "diffie-hellman-group14-sha1"},
			Ciphers:      []string{"aes128-ctr", "aes256-ctr", "aes128-cbc"},
			MACs:         []string{"hmac-sha2-256", "hmac-sha1"},
		},
	}
	cli, err := ssh.Dial("tcp", *host+":22", cfg)
	if err != nil {
		fmt.Println("连不上:", err)
		os.Exit(1)
	}
	defer cli.Close()

	run := func(s string) string {
		se, err := cli.NewSession()
		if err != nil {
			return "会话失败: " + err.Error()
		}
		defer se.Close()
		var out bytes.Buffer
		se.Stdout, se.Stderr = &out, &out
		if err := se.Run(s); err != nil {
			return out.String() + "\n[退出码非零] " + err.Error()
		}
		return out.String()
	}

	if *put != "" {
		// 冒号只在"本地:远端"这一个分隔符上有意义：路径里带冒号的设备目录不存在
		i := bytes.LastIndexByte([]byte(*put), ':')
		if i < 0 {
			fmt.Println("-put 要写成 本地文件:远端路径")
			os.Exit(2)
		}
		local, remote := (*put)[:i], (*put)[i+1:]
		data, err := os.ReadFile(local)
		if err != nil {
			fmt.Println("读本地文件:", err)
			os.Exit(1)
		}
		// 设备上既没有 base64 也没有 perl/python，openssl 的 base64 子命令是空壳
		// （能跑，输出是空的，这种"成功但什么也没写"最坑）。唯一可靠的写法是把字节
		// 转成 printf 认识的八进制转义，一段一段追加。
		const chunk = 2048
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf(`mkdir -p "$(dirname %q)"; `, remote))
		for i := 0; i < len(data); i += chunk {
			end := i + chunk
			if end > len(data) {
				end = len(data)
			}
			var esc strings.Builder
			for _, b := range data[i:end] {
				fmt.Fprintf(&esc, `\%03o`, b)
			}
			op := ">"
			if i > 0 {
				op = ">>"
			}
			sb.WriteString(fmt.Sprintf("printf '%s' %s %q.tmp", esc.String(), op, remote))
			if end < len(data) {
				sb.WriteString(" && ")
			}
		}
		sh := sb.String() + fmt.Sprintf(` && mv -f %q.tmp %q && openssl md5 %q`, remote, remote, remote)
		fmt.Println(run(sh))
		return
	}
	fmt.Println(run(*cmd))
}
