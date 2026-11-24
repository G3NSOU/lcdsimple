package main

import "testing"

func TestMaskStr(t *testing.T) {
	cases := [][2]string{
		{"198.51.100.10", "x.x.x.x"},
		{"网关 203.0.113.1 丢包 0%", "网关 x.x.x.x 丢包 0%"},
		{"5e:9b:36:88:a0:61", "xx:xx:xx:xx:xx:xx"},
		{"http://192.0.2.1:9090/ui", "http://x.x.x.x:9090/ui"}, // 端口留着：那是服务约定，不是个人信息
		{"http://nas.internal.lan:5666/", "http://x.x.x.x:5666/"},
		{"https://a.b.c", "https://x.x.x.x"},
		{"运行 11小时38分", "运行 11小时38分"},
		{"15%", "15%"},
	}
	for _, c := range cases {
		if got := maskStr(c[0]); got != c[1] {
			t.Errorf("maskStr(%q) = %q, 期望 %q", c[0], got, c[1])
		}
	}
}

func TestMaskSnap(t *testing.T) {
	s := &Snap{
		Hostname: "my-nas",
		LanIP:    "192.0.2.1",
		DevCount: 3,
		Devs:     []Device{{Name: "fnos", IP: "198.51.100.10", MAC: "aa:bb:cc:dd:ee:ff"}},
		Wan:      Wan{IP: "203.0.113.79", Mode: "PPPoE", GW: "203.0.113.1"},
		Fnos:     Fnos{Host: "198.51.100.10", Load1: 0.36},
		Clash:    Clash{EgressIP: "203.0.113.79", EgressVia: "http://ip-api.bard/x"},
	}
	MaskSnap(s)
	if s.LanIP != "x.x.x.x" || s.Devs[0].IP != "x.x.x.x" || s.Wan.IP != "x.x.x.x" || s.Fnos.Host != "x.x.x.x" {
		t.Errorf("地址没打干净：%v %v %v %v", s.LanIP, s.Devs[0].IP, s.Wan.IP, s.Fnos.Host)
	}
	if s.Devs[0].MAC != "xx:xx:xx:xx:xx:xx" {
		t.Errorf("MAC 没打码：%s", s.Devs[0].MAC)
	}
	if s.Wan.GW != "x.x.x.x" || s.Clash.EgressIP != "x.x.x.x" {
		t.Errorf("网关/出口没打码：%s %s", s.Wan.GW, s.Clash.EgressIP)
	}
	// 自定义域名里没有点分四段，得从 URL 里把主机段整块换掉
	if s.Clash.EgressVia != "http://x.x.x.x/x" {
		t.Errorf("探测 URL 里的域名没打码：%s", s.Clash.EgressVia)
	}
	// 设备名经常直接就是本人 ID，反射也抓不到（它不是地址形状）
	if s.Devs[0].Name != "设备 1" {
		t.Errorf("设备名没打码：%s", s.Devs[0].Name)
	}
	// 主机名是各人起的昵称，不是地址，反射抓不到，得单独处理
	if s.Hostname != "hostname" {
		t.Errorf("主机名没打码：%s", s.Hostname)
	}
	// 数字不能被顺手改掉
	if s.DevCount != 3 || s.Fnos.Load1 != 0.36 {
		t.Errorf("数字被改坏了")
	}
}
