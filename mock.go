package main

import (
	"fmt"
	"time"
)

// mockSnap 造一帧"什么都正常、什么都可能出问题"的数据，专门用来在没外网的
// 情况下先验证运行态版式：OpenClash 跑起来后卡片会长什么样、节点名超长会不会
// 撑破边框、80%/90% 两档告警色对不对。假数据只在 -mock 下用，不会上屏。
func mockSnap() *Snap {
	t := time.Now()
	s := &Snap{
		// 假数据里不放任何真实环境的东西：机器名、主板型号、内网网段都是编的。
		// 这些字符串要够长够杂，才能验出版式会不会被撑破——但没必要拿别人的设备当样本。
		Time: t, Hostname: "example-router", OSName: "iStoreOS 25.12.5",
		Model:  "EXAMPLE COMPUTER INC. B360M-XXXX",
		Kernel: "6.12.94", Uptime: 9120,
		CPUPct: 62, CorePct: []float64{12, 8, 95, 44, 30, 88, 6, 3, 91, 20, 15, 7},
		Load: [3]float64{4.21, 3.02, 2.11}, Freq: 4.3, TempPkg: 57,
		MemTotal: 33.4e9, MemUsed: 24.6e9, MemPct: 74,
		LanIP: "192.168.1.1", Leases: 12, DevCount: 11,
		Wan: Wan{Proto: "pppoe", IfName: "eth2", IP: "116.24.71.98", GW: "116.24.0.1",
			Up: true, Uptime: 7200, RXr: 8.4e6, TXr: 1.2e6},
		Ifaces: []Iface{{Name: "eth0", Up: true}, {Name: "eth1", Up: false},
			{Name: "eth2", Up: true}, {Name: "eth3", Up: true}, {Name: "br-lan", Up: true}},
		Clash: Clash{
			Installed: true, Enabled: true, Running: true, Version: "v0.47.1",
			Mode: "Rule", ProxyTotal: 186, ConnCount: 43, Latency: 142,
			DownTotal: 148.3e9, UpTotal: 12.7e9, CtrlAddr: "127.0.0.1:9090",
			ConfigFile: "/etc/openclash/Soft-Cloud.yaml",
			Primary: ProxyGroup{Name: "节点选择", Now: "🇯🇵 Tokyo-02 | 0.1x 倍率 | 双向三网直连优化 IEPL 专线",
				All: []string{"a", "b", "c"}},
			Groups: []ProxyGroup{
				{Name: "节点选择", Now: "Tokyo-02", All: []string{"a"}},
				{Name: "流媒体解锁", Now: "US-Streaming-01 倍率1.0", All: []string{"b"}},
				{Name: "广告拦截", Now: "REJECT", All: []string{"c"}},
				{Name: "回国专线", Now: "CN-Guangzhou-03 | 三网优化 | 高带宽", All: []string{"d"}},
				{Name: "AI服务", Now: "SG-11 | OpenAI 专用", All: []string{"e"}},
				{Name: "Apple服务", Now: "US-11", All: []string{"f"}},
				{Name: "漏网之鱼", Now: "HK-04", All: []string{"g"}},
			},
			// Show：模拟"用户在页面上勾了 6 个、按勾的顺序排"。第一个兼作 Primary，
			// 其余进"其他分组"；帧缓冲那侧一屏摆 5 行，所以要看到"另有 1 个"那行。
			Show: []ProxyGroup{
				{Name: "节点选择", Now: "Tokyo-02"},
				{Name: "AI服务", Now: "SG-11 | OpenAI 专用"},
				{Name: "流媒体解锁", Now: "US-Streaming-01 倍率1.0"},
				{Name: "漏网之鱼", Now: "HK-04"},
				{Name: "Apple服务", Now: "US-11"},
				{Name: "广告拦截", Now: "REJECT"},
				{Name: "回国专线", Now: "CN-Guangzhou-03 | 三网优化 | 高带宽"},
			},
			// 常态出口（境外）。想验"IP 已暴露"的橙色告警版式，把上面四行换成：
			// EgressIP: "117.136.38.21", EgressCC: "CN", EgressCity: "重庆",
			// EgressISP: "China Mobile", EgressVia: "本机直连", EgressAge: 32,
			// EgressCN: true, EgressExposure: 205,
			EgressIP: "104.28.55.11", EgressCC: "JP", EgressCity: "东京",
			EgressISP: "Cloudflare, Inc.", EgressVia: "OpenClash 代理", EgressAge: 32,
		},
		Fnos: Fnos{
			Host: "192.168.1.20", VMRunning: true, SSHO: true, Load1: 3.4, MemTotal: 16.8e9, MemAvail: 4.2e9,
			Uptime: 9000, QemuCores: 2.8, AllocBytes: 17.2e9, Web: 200,
			GPUOk: true, GPUUtil: 37,
			Vols: []Vol{
				{Name: "/vol1", Used: 21.7e9, Total: 1000e9, Available: true},
				{Name: "/vol2", Used: 3.3e12, Total: 3.6e12, Available: true},
				{Name: "/vol3", Used: 1.8e12, Total: 1.9e12, Available: true},
				{Name: "/vol4", Used: 0.4e12, Total: 4.0e12, Available: true},
			},
		},
		Disks: []Disk{
			{Name: "根分区", Mount: "/", Size: 2e9, Used: 0.2e9, Pct: 10, IsRoot: true},
			{Name: "fnOS 镜像盘", Mount: "/mnt/nvme0n1-4", Size: 231.3e9, Used: 214e9, Pct: 92.5, IsVMStore: true},
		},
		DockerRun: 3, DockerAll: 4,
		Devs: []Device{
			{Name: "mac-mini", IP: "192.168.1.189", MAC: "5e:9b:36:88:a0:61", Conns: 43, RXr: 6.1e6, TXr: 1.1e6, On: true, State: "REACHABLE"},
			{Name: "iPhone", IP: "192.168.1.101", MAC: "2a:8b:34:79:fe:4f", Conns: 12, RXr: 0.2e6, TXr: 0.1e6, On: true, State: "STALE"},
			{Name: "未命名设备", IP: "192.168.1.114", MAC: "a6:e2:4a:12:34:56", Conns: 18, On: true, State: "REACHABLE"},
			{Name: "fnos", IP: "192.168.1.20", Conns: 6, RXr: 1.4e6, TXr: 0.9e6, On: true, State: "REACHABLE"},
			{Name: "phone-a", IP: "192.168.1.186", Conns: 2, On: true, State: "STALE"},
			{Name: "Watch", IP: "192.168.1.217", On: false, State: "FAILED"},
		},
		Logs: []string{
			"01:08:27  odhcpd: A default route is present but there is no public prefix on lan thus we announce no default route by setting ra_lifetime to 0!  ×6",
			"01:08:31  dnsmasq: compile time options: IPv6-GNU-libc ipset no-auth no-auth-dnsmasq",
			"01:08:40  netifd: wan (6124): IPv6 RA received, default route present but no public prefix",
			"01:08:52  openclash: mihomo reload config ok, proxies 186",
			"01:09:03  hostapd: wlan0: STA 5e:9b:36:88:a0:61 IEEE 802.11: authenticated",
		},
		Notes: []string{"quickstart 接口无响应"},
	}
	for i := range s.Logs {
		s.Logs[i] = fmt.Sprintf("%s", s.Logs[i])
	}
	return s
}
