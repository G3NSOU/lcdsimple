package main

// isMainland 决定"出口 IP 已暴露"这条橙色告警开不开，值得钉死：
// 他说的"回到中国大陆境内"是 CN，港澳台不算，误报和漏报都会让人不再信这条提示。

import (
	"net"
	"testing"
)

// ip 只是测试里写短一点的写法
func ip(s string) net.IP { return net.ParseIP(s) }

func TestIsMainland(t *testing.T) {
	cases := []struct {
		cc, country string
		want        bool
	}{
		{"CN", "China", true},
		{"cn", "China", true},
		{" CN ", "中国", true},
		{"", "中国", true},
		{"HK", "Hong Kong", false},
		{"MO", "澳门", false},
		{"TW", "中国台湾", false},
		{"JP", "Japan", false},
		{"US", "United States", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := isMainland(c.cc, c.country); got != c.want {
			t.Errorf("isMainland(%q,%q) = %v，想要 %v", c.cc, c.country, got, c.want)
		}
	}
}

// 内核日志那两行是实测抓回来的原样文本，解析不出来就说明格式判断错了
func TestMihomoLogParse(t *testing.T) {
	line := `time="2026-11-24T19:51:00.554599507+08:00" level=info msg="Sync system time success: 2026-11-24 19:51:00 +0800"`
	lvl, msg, ok := parseMihomo(line)
	if !ok || lvl != "info" || msg != "Sync system time success: 2026-11-24 19:51:00 +0800" {
		t.Fatalf("parseMihomo 解析失败: lvl=%q msg=%q ok=%v", lvl, msg, ok)
	}
	ts, ok := mihomoTime(line)
	if !ok {
		t.Fatalf("mihomoTime 没解析出时刻")
	}
	if got := ts.Format("15:04:05"); got != "19:51:00" {
		t.Errorf("时刻 = %s，想要 19:51:00", got)
	}
}

// wanMode 决定顶栏那枚气泡说什么。最容易错的不是 PPPoE，是"没有拨号口"的两种接法：
// DHCP 上游和旁路由——把它们报成"未连接"，人就以为断网了，其实网络好得很。
func TestWanMode(t *testing.T) {
	cases := []struct {
		proto, dev, rproto, want string
	}{
		{"pppoe", "ppp0", "static", "PPPoE"},
		{"pppoe", "", "", "PPPoE"},
		{"dhcp", "eth0", "dhcp", "DHCP"},
		{"", "eth0", "dhcp", "DHCP"},          // uci 那边读不到协议，路由自己会标 dhcp
		{"static", "eth0", "static", "静态 IP"}, //
		{"", "br-lan", "static", "旁路由"},       // 默认路由走自己的桥 = 二级设备
		{"none", "br-lan", "", "旁路由"},
		{"", "eth0", "", "静态 IP"}, // 有出接口但认不出协议：手工配默认路由是唯一常见解释
		{"", "", "", "WAN"},       // 什么都读不到，别瞎猜模式
	}
	for _, c := range cases {
		if got := wanMode(c.proto, c.dev, c.rproto); got != c.want {
			t.Errorf("wanMode(%q,%q,%q) = %q，想要 %q", c.proto, c.dev, c.rproto, got, c.want)
		}
	}
}

// lanNetOf 是"哪些 IP 算内网设备"的唯一依据。写死网段的话，换一家就一台都列不出来。
func TestLanNetOf(t *testing.T) {
	n := lanNetOf("192.168.1.1/24")
	if n == nil || !n.Contains(ip("192.168.1.77")) || n.Contains(ip("10.8.0.7")) {
		t.Errorf("192.168.1.1/24 网段算错")
	}
	if lanNetOf("10.0.0.1") == nil || !lanNetOf("10.0.0.1").Contains(ip("10.0.0.9")) {
		t.Errorf("掩码丢了要按 /24 处理，不能直接放弃")
	}
	if lanNetOf("") != nil || lanNetOf("不是地址") != nil {
		t.Errorf("空值/坏值要返回 nil，让调用方退回不过滤")
	}
}
