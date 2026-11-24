package main

// 局域网只读接口：把同一份采集结果用 HTTP/SSE 播出去，给 iPad mini 1 那种
// 老浏览器当数据源用。
//
// 三条硬约束：
//  1. 只读，且绝不往外发 PPPoE 账号、clash 密钥、SSH 私钥路径这些东西；
//  2. 默认绑在 br-lan 的地址上，不监听 WAN（这台机器同时是拨号路由器）；
//  3. 载荷要小——A5 那台设备只有 768MB 内存，284 个节点名的数组不能进 JSON。

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// indexHTML 是 iPad 上那张页面：单文件、内联样式和脚本，ES5。
// 用 embed 打进二进制，路由器上不用再多铺一个文件，也不会有"页面新、接口旧"的错位。

//go:embed web/index.html
var indexHTML []byte

// ---------------- 载荷 ----------------

type wEgress struct {
	IP, CC, City, ISP, Via, Err string
	Age                         float64
	CN                          bool
	Exposure                    float64
}

type wClash struct {
	Installed, Enabled, Running bool
	// Op 非空 = 有一个面板操作正在进行，页面据此锁住按钮
	Op            string
	Version, Mode string
	Node, Group   string
	Groups        []wGroup `json:",omitempty"`
	// Show：用户勾了要显示哪几个分组时的结果（含顺序，第一个就是上面那个 Group）。
	// 没勾过就不出现，页面退回"其他分组=按名字排序截断"的老显示。
	Show                  []wGroup `json:",omitempty"`
	ProxyTotal, ConnCount int
	Latency               float64
	UpTotal, DownTotal    float64
	Egress                wEgress
	Err                   string
}

type wGroup struct{ Name, Now string }

type wWan struct {
	Proto, IfName, IP, GW string
	// Mode 联网模式（PPPoE/DHCP/静态 IP/旁路由），Via 默认路由的下一跳，
	// Connected 是"有没有默认路由"——页面气泡用它，不用 Up，
	// 否则旁路由会被显示成未连接。
	Mode      string
	Via       string
	Connected bool
	Up        bool
	Uptime    float64
	RXr, TXr  float64
	DNS       []string
}

type wIface struct {
	Name   string
	Up     bool
	RXr    float64
	TXr    float64
	RX, TX uint64
}

type wDev struct {
	Name, IP, MAC, State string
	On                   bool
	RXr, TXr             float64
	Conns                int
	Panel                string // 非空 = 这台就是 iPad 面板机，值是画面状态
}

type wVol struct {
	Name  string
	Used  float64
	Total float64
	Pct   float64
	Ok    bool
}

type wFnos struct {
	// Host：设置里填的 fnOS 地址，没填就是空串，页面显示"未配置"
	Host             string
	VMRunning, GPUOk bool
	GPUName          string
	GPUDriver        string
	GPUUtil          float64
	GPUMem           float64
	GPUMemTot        float64
	GPUTemp          float64
	GPUErr           string
	SSHErr           string
	Load1            float64
	CpuPct           float64
	DiskR            float64
	DiskW            float64
	MemTotal         float64
	MemAvail         float64
	Uptime           float64
	Web              int
	QemuCores        float64
	Vols             []wVol
}

type wIPad struct {
	Configured bool
	On         bool
	Err        string
	Ver        string
	Uptime     float64
	Safari     bool
	Clip       bool
	Tabs       int
	MemTotal   float64
	MemUsed    float64
	MemPct     float64
	DiskUsed   float64
	DiskTotal  float64
	DiskPct    float64
	Skew       float64
	PageSeen   float64
	PageOK     bool
	Age        float64
}

// wLink 是"线路质量"那一格。LastDial/LastDrop 直接给 "15:04" 字符串：
// 老 Safari 解析 ISO 时间不可靠，能少一处坑就少一处
type wLink struct {
	GW         string
	GwMs       float64
	GwLoss     float64
	Dns        string
	DnsMs      float64
	ResolveMs  float64
	ResolveErr string
	DialToday  int
	DropToday  int
	NoLog      bool
	LastDial   string
	LastDrop   string
	Age        float64
	Err        string
}

// Payload 是给浏览器看的一帧；字段名保持 Go 风格，前端按原样取用
type Payload struct {
	Time      time.Time
	Hostname  string
	Title     string
	OSName    string
	Kernel    string
	Model     string
	Uptime    float64
	CPUPct    float64
	CorePct   []float64
	Load      [3]float64
	Freq      float64
	TempPkg   float64
	TempBoard float64
	MemTotal  float64
	MemUsed   float64
	MemPct    float64
	Wan       wWan
	Ifaces    []wIface
	DevCount  int
	DevOnline int
	Devs      []wDev
	Clash     wClash
	Fnos      wFnos
	IPad      wIPad
	Link      wLink
	DockerRun int
	DockerAll int
	Logs      []string
	Notes     []string
	Version   string
}

// macTail 只留后三字节：够认出是哪台设备，又不至于把整台网络的拓扑白送出去
func macTail(m string) string {
	p := strings.Split(strings.ToUpper(m), ":")
	if len(p) < 6 {
		return m
	}
	return strings.Join(p[3:], ":")
}

func buildPayload(s *Snap) *Payload {
	p := &Payload{
		Time: s.Time, Hostname: s.Hostname, Title: s.Title, OSName: s.OSName, Kernel: s.Kernel,
		Model: s.Model, Uptime: s.Uptime, CPUPct: s.CPUPct, CorePct: s.CorePct,
		Load: s.Load, Freq: s.Freq, TempPkg: s.TempPkg, TempBoard: s.TempBoard,
		MemTotal: s.MemTotal, MemUsed: s.MemUsed, MemPct: s.MemPct,
		DevCount: s.DevCount, DevOnline: s.DevOnline,
		DockerRun: s.DockerRun, DockerAll: s.DockerAll,
		Logs: s.Logs, Notes: s.Notes, Version: version,
	}
	p.Wan = wWan{Proto: s.Wan.Proto, IfName: s.Wan.IfName, IP: s.Wan.IP, GW: s.Wan.GW,
		Mode: s.Wan.Mode, Via: s.Wan.Via, Connected: s.Wan.Connected,
		Up: s.Wan.Up, Uptime: s.Wan.Uptime, RXr: s.Wan.RXr, TXr: s.Wan.TXr, DNS: s.Wan.DNS}
	for _, i := range s.Ifaces {
		p.Ifaces = append(p.Ifaces, wIface{Name: i.Name, Up: i.Up, RXr: i.RXr, TXr: i.TXr, RX: i.RX, TX: i.TX})
	}
	for _, d := range s.Devs {
		p.Devs = append(p.Devs, wDev{Name: d.Name, IP: d.IP, MAC: macTail(d.MAC), State: d.State,
			On: d.On, RXr: d.RXr, TXr: d.TXr, Conns: d.Conns, Panel: d.Panel})
	}
	cl := s.Clash
	p.Clash = wClash{Installed: cl.Installed, Enabled: cl.Enabled, Running: cl.Running,
		Version: cl.Version, Mode: cl.Mode, Node: cl.Primary.Now, Group: cl.Primary.Name,
		Op:         cl.Op,
		ProxyTotal: cl.ProxyTotal, ConnCount: cl.ConnCount, Latency: cl.Latency,
		UpTotal: cl.UpTotal, DownTotal: cl.DownTotal, Err: cl.Err}
	// 分组只带"当前选了谁"，All 里那几百个节点名是屏幕都不显示的重量
	for _, g := range cl.Groups {
		p.Clash.Groups = append(p.Clash.Groups, wGroup{Name: g.Name, Now: g.Now})
	}
	// 勾过的顺序单独给一份：页面按它排"其他分组"，不用自己再猜
	for _, g := range cl.Show {
		p.Clash.Show = append(p.Clash.Show, wGroup{Name: g.Name, Now: g.Now})
	}
	p.Clash.Egress = wEgress{IP: cl.EgressIP, CC: cl.EgressCC, City: cl.EgressCity,
		ISP: cl.EgressISP, Via: cl.EgressVia, Err: cl.EgressErr, Age: cl.EgressAge,
		CN: cl.EgressCN, Exposure: cl.EgressExposure}
	f := s.Fnos
	p.Fnos = wFnos{Host: f.Host, VMRunning: f.VMRunning, GPUOk: f.GPUOk, SSHErr: f.SSHErr, Load1: f.Load1,
		CpuPct: f.CpuPct, DiskR: f.DiskR, DiskW: f.DiskW,
		GPUName: f.GPUName, GPUDriver: f.GPUDriver, GPUUtil: f.GPUUtil, GPUMem: f.GPUMem,
		GPUMemTot: f.GPUMemTot, GPUTemp: f.GPUTemp, GPUErr: f.GPUErr,
		MemTotal: f.MemTotal, MemAvail: f.MemAvail, Uptime: f.Uptime, Web: f.Web, QemuCores: f.QemuCores}
	ip := s.IPad
	p.IPad = wIPad{Configured: ip.Configured, On: ip.On, Err: ip.Err, Ver: ip.Ver,
		Uptime: ip.Uptime, Safari: ip.Safari, Clip: ip.Clip, Tabs: ip.Tabs, MemTotal: ip.MemTotal,
		MemUsed: ip.MemUsed, MemPct: ip.MemPct, DiskUsed: ip.DiskUsed, DiskTotal: ip.DiskTotal,
		DiskPct: ip.DiskPct, Skew: ip.Skew, PageSeen: ip.PageSeen, PageOK: ip.PageOK}
	if !ip.ProbeT.IsZero() {
		// 探测是在这一帧的中途完成的，ProbeT 会晚于帧起始时刻，不夹住就会显示
		// "-5 秒前探测"这种负数
		if a := s.Time.Sub(ip.ProbeT).Seconds(); a > 0 {
			p.IPad.Age = a
		}
	}
	for _, v := range f.Vols {
		p.Fnos.Vols = append(p.Fnos.Vols, wVol{Name: v.Name, Used: v.Used, Total: v.Total,
			Pct: v.Pct, Ok: v.Available})
	}
	lk := s.Link
	p.Link = wLink{GW: lk.GW, GwMs: lk.GwMs, GwLoss: lk.GwLoss, Dns: lk.Dns, DnsMs: lk.DnsMs,
		ResolveMs: lk.ResolveMs, ResolveErr: lk.ResolveErr, DialToday: lk.DialToday,
		DropToday: lk.DropToday, NoLog: lk.NoLog, Err: lk.Err,
		LastDial: lk.LastDial.Format("15:04"), LastDrop: lk.LastDrop.Format("15:04")}
	if !lk.ProbeT.IsZero() {
		if a := s.Time.Sub(lk.ProbeT).Seconds(); a > 0 {
			p.Link.Age = a
		}
	}
	return p
}

// ---------------- 广播 ----------------

// hub 只保留最后一帧：新订阅者立刻能拿到东西，慢客户端丢帧而不是把主循环堵住
type hub struct {
	mu   sync.Mutex
	last []byte
	subs map[chan []byte]bool
}

func (h *hub) publish(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = b
	for c := range h.subs {
		select {
		case c <- b:
		default: // 客户端跟不上就丢，路由器上的面板不能因为 iPad 卡住
		}
	}
}

func (h *hub) subscribe() chan []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[chan []byte]bool{}
	}
	c := make(chan []byte, 4)
	h.subs[c] = true
	return c
}

func (h *hub) release(c chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, c)
}

func (h *hub) latest() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// ---------------- 服务 ----------------

type webServer struct {
	h        *hub
	addr     string
	srv      *http.Server
	ln       net.Listener
	stopCh   chan struct{}
	col      *Collector
	control  bool
	token    string
	port     int
	wt       *clientWatch
	pageHTML []byte // 按 -status-bar 改写过的那份页面，起服务时算一次
}

// 状态栏样式：iOS 9 只在"全屏网页"（桌面图标那条路）里认这个 meta，
// Safari 里怎么填都没用。black 是不透明黑底；black-translucent 是内容顶到状态栏下面，
// 于是状态栏底下那 20px 由页面自己涂黑。哪个真的把白条变黑，得在他设备上试过一次才知道，
// 所以这做成 uci 开关，改完重新打开图标就行，不用重装描述文件。
var barMetaOld = []byte(`apple-mobile-web-app-status-bar-style" content="black"`)
var barMetaNew = []byte(`apple-mobile-web-app-status-bar-style" content="black-translucent"`)

// 只有 navigator.standalone 为真才加高顶部：Safari 里状态栏不归页面管，
// 多垫 20px 只会把已经铺满的版式挤出屏幕
const barInject = `<style>.sb #app{padding-top:30px}</style>` +
	`<script>if(window.navigator&&navigator.standalone){` +
	`document.documentElement.className+=' sb';}</script>`

func statusBarPage(style string, base []byte) []byte {
	if style != "translucent" {
		return base
	}
	out := bytes.Replace(base, barMetaOld, barMetaNew, 1)
	if i := bytes.LastIndex(out, []byte("</head>")); i >= 0 {
		out = append(append([]byte{}, out[:i]...),
			append([]byte(barInject), out[i:]...)...)
	}
	return out
}

// clientWatch 只回答一个问题：某个局域网客户端最近还在不在取数。
// iPad 那张屏到底有没有在显示面板，靠设备侧进程表是猜的（Safari 活着不等于页面在前台），
// 但这里的数据是真的——SSE 挂着就是有画面，掉了就是没了。
type clientWatch struct {
	mu   sync.Mutex
	live map[string]int
	seen map[string]time.Time
}

// table 只为 /api/debug 服务：这类"页面明明在报活、采集器却说没见过"的矛盾，
// 靠猜没意义，把服务端记到的原始表打出来一眼就能定位是记录端还是消费端的问题。
func (cw *clientWatch) table() map[string]any {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	out := map[string]any{}
	for ip, t := range cw.seen {
		out[ip] = map[string]any{"agoS": int64(time.Since(t).Seconds()), "streams": cw.live[ip]}
	}
	return out
}

func newClientWatch() *clientWatch {
	return &clientWatch{live: map[string]int{}, seen: map[string]time.Time{}}
}

func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func (cw *clientWatch) hit(ip string) {
	if cw == nil {
		return
	}
	cw.mu.Lock()
	cw.seen[ip] = time.Now()
	cw.mu.Unlock()
}

func (cw *clientWatch) enter(ip string) {
	if cw == nil {
		return
	}
	cw.mu.Lock()
	cw.live[ip]++
	cw.seen[ip] = time.Now()
	cw.mu.Unlock()
}

func (cw *clientWatch) leave(ip string) {
	if cw == nil {
		return
	}
	cw.mu.Lock()
	if cw.live[ip] > 0 {
		cw.live[ip]--
	}
	cw.mu.Unlock()
}

func (cw *clientWatch) last(ip string) time.Time {
	if cw == nil {
		return time.Time{}
	}
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.live[ip] > 0 {
		return time.Now()
	}
	return cw.seen[ip]
}

// LAN 地址：读 uci 里现成的配置，不改任何东西。读不到就退回全接口，
// 由防火墙决定要不要放进来。
func lanBind() string {
	out, _ := runTO(1200*time.Millisecond, "uci", "-q", "get", "network.lan.ipaddr")
	ip := strings.Trim(strings.TrimSpace(out), `"'`)
	// iStoreOS 这里写的是 "192.168.1.1/24" 这种带掩码的形式，掩码得剥掉才能当监听地址
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		ip = ip[:i]
	}
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

// startWeb 起局域网服务。control=true 时额外开放"改节点/改模式"的写接口，
// 写接口必须带令牌；令牌存在 uci 里，页面第一次操作时由人输入一次。
func startWeb(port int, bind string, col *Collector, control bool, token string, bar string) *webServer {
	if port <= 0 {
		return nil
	}
	if bind == "" {
		bind = lanBind()
	}
	if bind == "" {
		bind = "0.0.0.0"
	}
	h := &hub{}
	ws := &webServer{h: h, addr: fmt.Sprintf("%s:%d", bind, port), stopCh: make(chan struct{}),
		col: col, control: control, token: token, port: port, wt: newClientWatch(),
		pageHTML: statusBarPage(bar, indexHTML)}
	if ws.control && ws.token == "" {
		ws.token = newToken()
		persistToken(ws.token)
		fmt.Fprintf(os.Stderr, "控制令牌已生成（页面里输入一次即可）：%s\n", ws.token)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		setNoStore(w)
		_, _ = w.Write(ws.pageHTML)
	})
	// 启动图走页面里的 <link rel="apple-touch-startup-image">：Apple 的 WebClip 载荷
	// 没有 StartupImage 这个键，白屏只能从网页侧治。iOS 9 按文件名缓存，
	// 所以给一周的 max-age，切版本时改文件名。
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		var img []byte
		switch r.URL.Path {
		case "/assets/ipad-startup-land.png":
			img = ipadStartupLand
		case "/assets/ipad-startup-port.png":
			img = ipadStartupPort
		case "/assets/ipad-startup-land2x.png":
			img = ipadStartupLand2x
		case "/assets/ipad-startup-port2x.png":
			img = ipadStartupPort2x
		case "/assets/ipad-76.png":
			img = ipadIcon76
		case "/assets/ipad-120.png":
			img = ipadIcon120
		case "/assets/ipad-152.png":
			img = ipadIcon152
		case "/assets/ipad-180.png":
			img = ipadIcon180
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=604800")
		_, _ = w.Write(img)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		b := h.latest()
		if b == nil {
			http.Error(w, `{"error":"还没有采集到数据"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		setNoStore(w)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		setNoStore(w)
		rc := http.NewResponseController(w)
		c := h.subscribe()
		streamIP := clientIP(r)
		ws.wt.enter(streamIP)
		defer ws.wt.leave(streamIP)
		defer h.release(c)
		// iOS 9 的 EventSource 会缓冲、会静默断连，心跳注释让它至少知道链路还活着
		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		fmt.Fprint(w, ": routerpanel\n\n")
		fl.Flush()
		if b := h.latest(); b != nil {
			writeFrame(w, b)
			fl.Flush()
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ws.stopCh:
				return
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			case b := <-c:
				// 20 秒写不出去就是没人读了（熄屏、后台挂起、半死链路），
				// 主动断掉，让这一路的"在线"判定回到诚实
				_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Second))
				if err := writeFrameErr(w, b); err != nil {
					return
				}
				fl.Flush()
			}
		}
	})
	mux.HandleFunc("/ipad", ws.ipadSetup)
	mux.HandleFunc("/ipad.mobileconfig", ws.ipadProfile)
	mux.HandleFunc("/api/meta", func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		s := ws.col.Last()
		groups := []string{}
		mode := ""
		if s != nil {
			mode = s.Clash.Mode
			for _, g := range s.Clash.Groups {
				groups = append(groups, g.Name)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"control": ws.control, "groups": groups, "mode": mode,
			// show：用户勾的那几个（按勾的顺序），页面拿它把勾选状态画回去
			"show": ws.col.ShowGroups(),
		})
	})
	// /api/ping 是页面自己发的活体心跳：JS 还在跑才会发，熄屏挂起后立刻停。
	// 判断"那块屏到底有没有在显示"，这个比 socket 还开着可靠得多。
	mux.HandleFunc("/api/debug", ws.auth(ws.debugAPI))
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/nodes", ws.auth(ws.nodes))
	mux.HandleFunc("/api/control", ws.auth(ws.controlAPI))

	ln, err := net.Listen("tcp", ws.addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HTTP 监听 %s 失败: %v\n", ws.addr, err)
		return nil
	}
	ws.ln = ln
	// 每个请求都记一笔来源 IP：iOS 9 的 EventSource 不稳，页面会退化成 XHR 轮询，
	// 轮询同样是"画面还活着"的证据，两条路都得算
	tracked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws.wt.hit(clientIP(r))
		mux.ServeHTTP(w, r)
	})
	ws.srv = &http.Server{Handler: tracked, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := ws.srv.Serve(ln); err != nil && err.Error() != "http: Server closed" {
			fmt.Fprintf(os.Stderr, "HTTP 服务退出: %v\n", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "HTTP 已监听 %s（局域网只读）\n", ws.addr)
	return ws
}

// lastSeen 给采集器用：这个 IP 最近一次取数时间，零值表示从没见过
func (ws *webServer) lastSeen(ip string) time.Time {
	if ws == nil {
		return time.Time{}
	}
	return ws.wt.last(ip)
}

func setNoStore(w http.ResponseWriter) {
	// iOS Safari 对没写缓存头的响应很激进，不加这段 iPad 会一直给你看旧页面
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func writeFrame(w http.ResponseWriter, b []byte) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func writeFrameErr(w http.ResponseWriter, b []byte) error {
	_, err := fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

func (ws *webServer) push(p *Payload) {
	if ws == nil {
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	ws.h.publish(b)
}

// auth 只挡写接口：没开控制就一律 404，开了就必须带对令牌
func (ws *webServer) auth(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !ws.control {
			http.Error(w, `{"error":"控制接口未开启"}`, http.StatusNotFound)
			return
		}
		tok := r.Header.Get("X-Token")
		if tok == "" {
			_ = r.ParseForm()
			tok = r.FormValue("token")
		}
		if ws.token == "" || tok != ws.token {
			http.Error(w, `{"error":"令牌不对"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// debugAPI：谁在取数、多久之前、挂了几条流。排查"页面到底有没有在显示"只看这一份
func (ws *webServer) debugAPI(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"clients": ws.wt.table(),
		"ipad":    map[string]any{"host": ws.col.cfg.IPadHost, "seesSet": ws.col.sees != nil},
	})
}

func (ws *webServer) nodes(w http.ResponseWriter, r *http.Request) {
	g := r.URL.Query().Get("group")
	if g == "" {
		http.Error(w, `{"error":"缺 group"}`, http.StatusBadRequest)
		return
	}
	ns, err := ws.col.ClashNodes(g)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"group": g, "nodes": ns})
}

// controlAPI 三件事：切代理模式、开/关 OpenClash 插件、存"卡片上显示哪几个分组"。
// 节点切换按他的要求拿掉了——那是最容易被误点、又最需要去内核页面确认的一层。
// 也不碰 uci 的网络配置：这些要么是内核运行时的开关（和屏幕上点一下等价），
// 要么是本目录下一个纯文本文件。
//
// 每个动作都先进操作队列（同一时刻只允许一个），日志栏立刻出现"收到请求"，
// 结束时再写"完成/失败"。这样点一次就能看到它在跑，不会以为面板假死又点一次。
func (ws *webServer) controlAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"要 POST"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string   `json:"action"`
		Mode   string   `json:"mode"`
		State  string   `json:"state"`
		Groups []string `json:"groups"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, `{"error":"请求体不是 JSON"}`, http.StatusBadRequest)
		return
	}
	setNoStore(w)
	ip := clientIP(r)
	switch req.Action {
	case "mode":
		switch req.Mode {
		case "rule", "global", "direct":
		default:
			http.Error(w, `{"error":"mode 只能是 rule/global/direct"}`, http.StatusBadRequest)
			return
		}
		name := "模式 \u2192 " + modeCN(req.Mode)
		mode := req.Mode
		ws.runOp(name, ip, w, func() (string, error) {
			b, _ := json.Marshal(map[string]string{"mode": mode})
			code, out, err := ws.col.ClashDo("PATCH", "/configs", b)
			if err != nil {
				return "", fmt.Errorf("内核不可达")
			}
			if code >= 300 {
				return "", fmt.Errorf("内核返回 %d %s", code, lastLine(string(out)))
			}
			return "mode=" + mode, nil
		})
	case "clash":
		on := req.State == "on"
		name, verb := "OpenClash 启动", "start"
		if !on {
			name, verb = "OpenClash 停止", "stop"
		}
		ws.runOp(name, ip, w, func() (string, error) {
			// 脚本自己会等内核起来，最坏接近一分钟，这里给足 100 秒
			out, err := runTO(100*time.Second, "/etc/init.d/openclash", verb)
			if err != nil {
				return "", fmt.Errorf("%v（%s）", err, lastLine(out))
			}
			return "/etc/init.d/openclash " + verb + " 已返回", nil
		})
	case "groups":
		// 只收内核现在真的有的分组名：勾完正好赶上订阅更新、名字变了，
		// 与其存进去让卡片显示一行指错对象的，不如当场说清楚。
		known := map[string]bool{}
		if s := ws.col.Last(); s != nil {
			for _, g := range s.Clash.Groups {
				known[g.Name] = true
			}
		}
		clean := make([]string, 0, len(req.Groups))
		seen := map[string]bool{}
		for _, n := range req.Groups {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			if !known[n] {
				http.Error(w, fmt.Sprintf(`{"error":"分组 %q 现在不在了（订阅更新或改名了？）"}`, n), http.StatusBadRequest)
				return
			}
			seen[n] = true
			clean = append(clean, n)
		}
		if len(clean) > 12 { // 一屏摆不下，摆不下就是没意义
			http.Error(w, `{"error":"最多勾 12 个分组"}`, http.StatusBadRequest)
			return
		}
		names := clean
		ws.runOp("保存显示分组", ip, w, func() (string, error) {
			if err := ws.col.SetShowGroups(names); err != nil {
				return "", fmt.Errorf("存不下去：%v", err)
			}
			if len(names) == 0 {
				return "清空了勾选，卡片退回按名字排序", nil
			}
			return fmt.Sprintf("显示 %d 个分组：%s", len(names), strings.Join(names, "、")), nil
		})
	default:
		http.Error(w, `{"error":"未知 action（只剩 mode / clash / groups）"}`, http.StatusBadRequest)
	}
}

// runOp 抢到执行权就异步跑并立刻回 202；抢不到说明上一个还没结束，
// 把这次重复请求也记进日志——这正是"看起来没反应"最容易被误解成假死的地方。
func (ws *webServer) runOp(name, ip string, w http.ResponseWriter, fn func() (string, error)) {
	if !ws.col.startOp(name) {
		cur := ws.col.currentOp()
		ws.col.LogOp("重复请求被忽略：%s（上一个「%s」还在跑）", name, cur)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"error":"已有操作在进行中：%s"}`, jsonEsc(cur))
		ws.kick()
		return
	}
	ws.col.LogOp("收到请求：%s（来自 %s）", name, ip)
	go func() {
		defer ws.col.endOp()
		res, err := fn()
		if err != nil {
			ws.col.LogOp("失败：%s — %v", name, err)
		} else {
			ws.col.LogOp("完成：%s — %s", name, res)
		}
		ws.kick()
	}()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"ok":true,"pending":"%s"}`, jsonEsc(name))
	ws.kick()
}

func modeCN(m string) string {
	switch m {
	case "global":
		return "全局"
	case "direct":
		return "直连"
	case "rule":
		return "规则"
	}
	return m
}

func jsonEsc(v string) string {
	b, _ := json.Marshal(v)
	return string(b[1 : len(b)-1])
}

func lastLine(out string) string {
	l := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for k := len(l) - 1; k >= 0; k-- {
		if t := strings.TrimSpace(l[k]); t != "" {
			return shorten(t, 60)
		}
	}
	return ""
}

// kick 把最近一帧重新推一遍：操作跑完后不该等下一个刷新周期才让页面看到结果。
// 这里只读缓存的那一帧——采集本身仍归渲染循环独占，别处不能调 Collect()。
func (ws *webServer) kick() {
	if ws == nil {
		return
	}
	s := ws.col.Last()
	if s == nil {
		return
	}
	ws.push(buildPayload(s))
}

func (ws *webServer) stop() {
	if ws == nil {
		return
	}
	select {
	case <-ws.stopCh:
	default:
		close(ws.stopCh)
	}
	if ws.srv != nil {
		_ = ws.srv.Close()
	}
}

// newToken 给写接口生成一次性令牌；页面输入一次后存 localStorage
func newToken() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// persistToken 把令牌落进 uci，路由器重启后页面还能用同一把
func persistToken(t string) {
	if _, err := os.Stat("/etc/config/routerpanel"); err != nil {
		return
	}
	_, _ = runTO(1500*time.Millisecond, "uci", "set", "routerpanel.main.control_token="+t)
	_, _ = runTO(1500*time.Millisecond, "uci", "commit", "routerpanel")
}
