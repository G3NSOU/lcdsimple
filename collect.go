package main

// 采集层。原则：每个源独立超时、独立失败，任何一路挂了只影响它自己那张卡，
// 面板永远出图 —— 屏幕整块黑掉比数据缺一块更糟。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------------- 数据结构 ----------------

type Vol struct {
	Name      string
	Used      float64 // 字节
	Total     float64
	Pct       float64
	Available bool
}

type Iface struct {
	Name string
	Up   bool
	RX   uint64
	TX   uint64
	RXr  float64 // bps
	TXr  float64
}

type Wan struct {
	Proto  string
	IfName string
	// Mode 是给人看的联网模式：PPPoE / DHCP / 静态 IP / 旁路由。
	// 它不是 Proto 的大写——旁路由这种没有拨号口的接法，Proto 那一路什么都读不到。
	Mode string
	// Connected 看的是"有没有默认路由"，不是 network.interface.wan 的 up。
	// 只信后者会把旁路由报成"未连接"：那台设备本来就不该有 wan 口，这是假故障。
	Connected bool
	Via       string
	IP        string
	Mask      int
	GW        string
	DNS       []string
	Up        bool
	Uptime    float64
	RXr       float64
	TXr       float64
}

// NodeInfo 是页面上"切换节点"列表的一行
type NodeInfo struct {
	Name    string
	Delay   float64
	Current bool
}

type ProxyGroup struct {
	Name string
	Now  string
	All  []string
}

type Clash struct {
	Installed bool
	Enabled   bool
	Running   bool
	// Op 非空 = 面板正在执行一个操作（切模式/开关插件），页面据此锁住按钮并显示"操作中"
	Op      string
	Version string
	Mode    string
	Groups  []ProxyGroup
	// Show 是用户勾选的那几个分组，顺序就是显示顺序；第一个兼作 Primary（卡片标题下的
	// "当前节点/延迟"就是它）。没勾过时为空，卡片退回兜底逻辑。
	Show       []ProxyGroup
	Primary    ProxyGroup
	ProxyTotal int
	Latency    float64
	UpTotal    float64
	DownTotal  float64
	ConnCount  int
	EgressIP   string
	EgressCC   string
	EgressCity string
	EgressISP  string
	EgressVia  string
	EgressAge  float64
	EgressErr  string
	// EgressCN：出口 IP 当前在中国大陆境内（HK/TW/MO 不算，他说的是"大陆"）
	EgressCN       bool
	EgressExposure float64 // 已经暴露多少秒
	Err            string
	CtrlAddr       string
	// ConfigFile 是 uci 里指向的 yaml，内核没起来时也能告诉用户用的是哪份配置
	ConfigFile string
}

type Fnos struct {
	// Host 是用户在设置里填的 fnOS 地址，面板只是把它显示出来。
	// 代码里不预置任何地址：每家网段不一样，写死等于把一个人的环境发给所有人。
	Host      string
	VMRunning bool
	SSHO      bool
	SSHErr    string
	Load1     float64
	// CpuPct 是客户机自己的 CPU 利用率，DiskR/DiskW 是整盘读写每秒。
	// 卡片以前拿"负载 1 分钟"当 CPU：1.29 到底是 8% 还是 90% 全看分了几核，
	// 看的人只能猜，所以还是把这两个直接能读到的数取回来。
	CpuPct    float64
	DiskR     float64
	DiskW     float64
	MemTotal  float64
	MemAvail  float64
	Uptime    float64
	Vols      []Vol
	MDRaw     []string
	GPUUtil   float64
	GPUOk     bool
	GPUName   string
	GPUDriver string
	GPUMem    float64 // 已用显存，MB
	GPUMemTot float64 // 总显存，MB
	GPUTemp   float64
	GPUErr    string
	Web       int
	QemuCores float64
	// AllocBytes 是 qemu -m 给的分配量，来自宿主侧 cmdline
	AllocBytes float64
}

type QuickDisk struct {
	Name        string `json:"name"`
	Used        string `json:"used"`
	Total       string `json:"total"`
	UsedInt     string `json:"usedInt"`
	TotalInt    string `json:"totalInt"`
	IsRoot      bool   `json:"isRoot"`
	UsedPercent int    `json:"usedPercent"`
}

type Disk struct {
	Name      string
	Mount     string
	Size      float64
	Used      float64
	Pct       float64
	IsRoot    bool
	IsVMStore bool
}

type Quick struct {
	OK       bool        `json:"-"`
	CPU      float64     `json:"cpu"`
	Memory   float64     `json:"memory"`
	Temp1    float64     `json:"temp1"`
	Devices  int         `json:"devices"`
	NetErr   string      `json:"netErr"`
	Upload   string      `json:"upload"`
	Download string      `json:"download"`
	DockerOK bool        `json:"dockerOk"`
	Linkease bool        `json:"linkeaseOk"`
	Disks    []QuickDisk `json:"disks"`
}

type Snap struct {
	Time time.Time
	// Title 面板名字（空时各处退回主机名显示）
	Title     string
	Hostname  string
	Model     string
	Kernel    string
	OSName    string
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
	Wan       Wan
	Ifaces    []Iface
	LanIP     string
	Leases    int
	DevCount  int

	Devs      []Device
	DevOnline int
	Quick     Quick
	Disks     []Disk
	Clash     Clash
	DockerRun int
	DockerAll int
	Fnos      Fnos
	IPad      IPad `json:"iPad"`
	Link      Link
	Logs      []string
	Notes     []string
	Primed    bool // 第一轮差分数据还没有
}

// ---------------- 采集器 ----------------

type Config struct {
	FnHost   string
	FnUser   string
	FnKey    string
	FnMon    string
	ProxyURL string
	NoEgress bool
	// iPad 面板机：留空就不探
	IPadHost string
	IPadUser string
	IPadKey  string
	// GroupsFile 存用户勾选的分组名（一行一个）。默认 /etc/routerpanel/groups，
	// 本机调试/测试指到别处，别去碰真配置。
	GroupsFile string
	// Title 是这套面板的名字，出现在屏幕顶栏、网页标题、iPad 桌面图标上。
	// 它是产品名、不是机器事实，所以可以有默认值；想换成自己家的，uci 里 title 改一下就行。
	Title string
}

type egressCache struct {
	IP, CC, City, ISP, Via, Err string
	At                          time.Time
	// CNSince 是第一次探到出口落在中国大陆境内的时刻；不在境内时清零。
	// 起点只能是"探到的那一刻"，所以显示的暴露时长是下界（见 egressEveryCN 为什么要加密探测）
	CNSince time.Time
}

// 出口在国内时把探测间隔从 5 分钟压到 30 秒：一是尽快确认恢复，
// 二是让"已暴露 x 秒"这个数不至于差出一个 5 分钟的洞
const egressEveryCN = 30 * time.Second

type Collector struct {
	cfg      Config
	cpuTotal [2]uint64
	cpuPrev  [][2]uint64
	netPrev  map[string][2]uint64
	netT     time.Time
	// fnOS 的 CPU/磁盘是累计计数器，靠"这一轮减上一轮"换算，所以要存上一轮
	fnCPUPrev [2]float64
	fnDisk    [2]float64
	fnDiskT   time.Time
	lastT     time.Time
	qemuPrev  [2]uint64
	qemuPid   int
	egress    egressCache
	notes     []string
	wanUp     bool
	// conntrack 差分：累计字节按设备存一份上一轮的值
	ctPrev map[string][2]uint64
	ctT    time.Time
	// clashSecret 记住上一次能打通控制接口的密钥，避免每轮都从 401 开始试
	clashSecret string
	// 代理口：上次探测成功的那个排最前，其余按本轮从 uci/yaml 发现的顺序
	proxyOK   string
	proxyPort []string
	// lastSnap/lastMu：HTTP 线程要能拿到"最近一帧"，但采集本身仍由渲染循环独占
	lastSnap *Snap
	lastMu   sync.Mutex
	// clashBase / clashSecret 留给控制接口复用：面板已经知道内核在哪、密钥是什么，
	// 再让调用方自己拼一遍就会两处密钥不同步
	clashBase string
	proxyAuth []string // yaml 里 authentication 的 user:pass，代理口要它才通（只在内存里用，不上屏）
	// iPad 探测单独限速，结果先缓存着给每一帧复用
	ipadCache IPad
	ipadT     time.Time
	// 线路质量：探测在独立协程里跑（一轮 ping 最坏十几秒），这里只放最近一轮的结果
	linkMu      sync.Mutex
	linkCache   Link
	linkStarted bool
	// ipadPrevUp：上一次看到的 iPad 运行秒数，用来认出"它重启过"
	ipadPrevUp float64
	// sees：HTTP 侧回查"某个 IP 最近什么时候取过数"，没起 HTTP 时为 nil
	sees func(ip string) time.Time
	// 操作队列：同一时刻只允许一个面板操作在跑，重复请求直接顶回去并记进日志
	opMu  sync.Mutex
	opCur string
	ops   []opRec
	// OpenClash / fnOS 两路日志单独限速抓取，结果缓存给每一帧复用
	clLogT   time.Time
	clLog    []logRec
	fnosLogT time.Time
	fnosLog  []logRec
	// 卡片"其他分组"要显示哪几个，是用户在页面上勾的，代码不猜名字：
	// 分组名来自各家 yaml，猜错就变成"我的屏上有、你的屏上没有"。
	// showGrp 是这个选择的内存副本，真身在 GroupsFile 那个文件里（重启还在）。
	groupsMu  sync.Mutex
	showGrp   []string
	grpLoaded bool
}

// opRec 是一条进融合日志栏的事件。正文由发起方写全，这里只带来源标签。
type opRec struct {
	At   time.Time
	Src  string
	Text string
}

type logRec struct {
	At   time.Time
	Src  string
	Text string
}

// LogEvent 往融合日志栏里记一条带来源的事件。HTTP 线程和采集线程都会调，必须带锁。
// 面板自己的操作、设备侧刚发生的异常，都走这条路——日志栏现在是"今天出过什么事"的第一站。
func (c *Collector) LogEvent(src, f string, a ...any) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.ops = append(c.ops, opRec{At: time.Now(), Src: src, Text: fmt.Sprintf(f, a...)})
	if len(c.ops) > 24 {
		c.ops = c.ops[len(c.ops)-24:]
	}
}

// LogOp 记一条面板操作。HTTP 线程会调它，所以必须带锁。
func (c *Collector) LogOp(f string, a ...any) { c.LogEvent("面板", f, a...) }

// startOp 抢到执行权返回 true；已经有操作在跑就返回 false
func (c *Collector) startOp(name string) bool {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if c.opCur != "" {
		return false
	}
	c.opCur = name
	return true
}

func (c *Collector) endOp() {
	c.opMu.Lock()
	c.opCur = ""
	c.opMu.Unlock()
}

func (c *Collector) currentOp() string {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.opCur
}

func NewCollector(c Config) *Collector {
	return &Collector{cfg: c, netPrev: map[string][2]uint64{}, ctPrev: map[string][2]uint64{}}
}

func (c *Collector) note(f string, a ...any) {
	if len(c.notes) < 6 {
		c.notes = append(c.notes, fmt.Sprintf(f, a...))
	}
}

// ---------------- iPad 面板机（iOS 9.3.5，sshd 只讲老算法）----------------

type IPad struct {
	Configured bool      `json:"configured"`
	On         bool      `json:"on"` // SSH 拿得起来
	Err        string    `json:"err"`
	Ver        string    `json:"ver"`
	Uptime     float64   `json:"uptime"`
	Safari     bool      `json:"safari"` // MobileSafari 进程还在
	Clip       bool      `json:"clip"`   // 桌面图标那条路：/Applications/Web.app/Web
	Tabs       int       `json:"tabs"`   // WebContent 进程数
	MemTotal   float64   `json:"memTotal"`
	MemUsed    float64   `json:"memUsed"`
	MemPct     float64   `json:"memPct"`
	DiskUsed   float64   `json:"diskUsed"`
	DiskTotal  float64   `json:"diskTotal"`
	DiskPct    float64   `json:"diskPct"`
	Skew       float64   `json:"skew"` // 设备时钟减本机时钟，秒
	PageSeen   float64   `json:"pageSeen"`
	PageOK     bool      `json:"pageOk"` // 最近有人在页面上取过数
	Rebooted   bool      // 这一轮 uptime 比上一轮小很多 = 中间重启过
	ProbeT     time.Time `json:"-"`
}

// Link 是"线路质量"：家里到运营商这第一段走得顺不顺，以及今天掉过几次线。
// 网关和运营 DNS 的往返是真实链路值，实测确认过没被 OpenClash 的透明接管带走
// （DNS 3ms 对节点延迟 700ms，两路不同），所以这两个数字能拿来判断"慢在内网还是慢在外"。
// 解析耗时走的是本机 resolv.conf（dnsmasq→内核 DNS），那才是网页打开慢不慢的直接指标。
type Link struct {
	GW         string    // 拨号网关
	GwMs       float64   // 平均往返，毫秒
	GwLoss     float64   // 丢包 %
	Dns        string    // 运营 DNS
	DnsMs      float64   // 到它的往返
	ResolveMs  float64   // 一次真实解析耗时
	ResolveErr string    // 解析失败原因（比 0ms 更像实话）
	DialToday  int       // 今日拨号成功次数（pppd 日志里数出来的）
	DropToday  int       // 今日断线/超时次数
	NoLog      bool      // 日志里一条 pppd 都没有：数字不能当 0 用
	LastDial   time.Time // 上次拨号成功
	LastDrop   time.Time // 上次断线/超时
	ProbeT     time.Time `json:"-"`
	Err        string
}

// 设备上没有 ioreg、没有 cycript 可用的通道，能拿的只有 BSD 那套：
// sw_vers / sysctl / vm_stat / df / ps。全部一次 SSH 里跑完，别再一轮一个连接。
const ipadScript = `export PATH=/usr/bin:/bin:/usr/sbin:/sbin
echo '@ver'; sw_vers
echo '@phys'; /usr/sbin/sysctl -n hw.memsize
echo '@boot'; /usr/sbin/sysctl -n kern.boottime
echo '@now'; date +%s
echo '@mem'; vm_stat
echo '@disk'; df /var
echo '@proc'; ps -A -o pid,comm
echo '@end'`

// 30 秒一轮：这台是 512MB 的 iPhone OS 9，每次连接都要 fork 一个 sshd，
// 跟着面板 3 秒一跳会把人家压成幻灯片
const ipadEvery = 30 * time.Second

func (c *Collector) sshIPad() (string, error) {
	key, err := os.ReadFile(c.cfg.IPadKey)
	if err != nil {
		return "", fmt.Errorf("缺 iPad SSH 密钥")
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("密钥不可用")
	}
	cfg := &ssh.ClientConfig{
		User:            c.cfg.IPadUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
		// iOS 9 的 OpenSSH 6.7 没有 dh-group14-sha256，也不回 ed25519 主机密钥；
		// 这里不写死就协商不上，写死了也别指望它升级
		// 主机密钥同理：iOS 9 只有 ssh-rsa 和 ecdsa-nistp256
		HostKeyAlgorithms: []string{"ssh-rsa", "ecdsa-sha2-nistp256"},
		Config: ssh.Config{
			KeyExchanges: []string{"ecdh-sha2-nistp256", "diffie-hellman-group14-sha1"},
			Ciphers:      []string{"aes128-ctr", "aes256-ctr", "aes128-cbc"},
			MACs:         []string{"hmac-sha2-256", "hmac-sha1"},
		},
	}
	cli, err := ssh.Dial("tcp", c.cfg.IPadHost+":22", cfg)
	if err != nil {
		return "", err
	}
	defer cli.Close()
	sess, err := cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	done := make(chan struct{})
	var out []byte
	var serr error
	go func() { out, serr = sess.CombinedOutput(ipadScript); close(done) }()
	select {
	case <-done:
		return string(out), serr
	case <-time.After(9 * time.Second):
		cli.Close()
		return "", fmt.Errorf("SSH 取数超时")
	}
}

// num 取一行里最后一个数字，vm_stat 那种 "Pages free:  2449." 直接可剥掉句点
func num(line string) float64 {
	fs := strings.Fields(line)
	for i := len(fs) - 1; i >= 0; i-- {
		t := strings.Trim(fs[i], ".,:")
		if v, err := strconv.ParseFloat(t, 64); err == nil {
			return v
		}
	}
	return 0
}

func (c *Collector) ipad(s *Snap) {
	p := &s.IPad
	p.Configured = c.cfg.IPadHost != ""
	if !p.Configured {
		return
	}
	if !c.ipadT.IsZero() && time.Since(c.ipadT) < ipadEvery {
		s.IPad = c.ipadCache
		c.ipadPage(&s.IPad)
		return
	}
	out, err := c.sshIPad()
	c.ipadT = time.Now()
	if err != nil {
		// 保留上一次的数字，只把连接状态改成不通：屏幕不该因为一次抖动就空掉
		// Configured 也得补上：它是"该不该报这条提示"的开关，
		// 之前只在成功那一支赋值，SSH 一挂整个 iPad 状态就成了"未配置"，屏幕上连个屁都不提示
		c.ipadCache.Configured, c.ipadCache.On, c.ipadCache.Err = true, false, shortErr(err)
		c.ipadCache.ProbeT = c.ipadT
		s.IPad = c.ipadCache
		c.ipadPage(&s.IPad)
		return
	}
	np := IPad{Configured: true, On: true, ProbeT: c.ipadT}
	sec := map[string]float64{}
	sect := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "@") {
			sect = line[1:]
			continue
		}
		switch sect {
		case "ver":
			if v, ok := strings.CutPrefix(line, "ProductVersion:"); ok {
				np.Ver = strings.TrimSpace(v)
			}
		case "phys":
			np.MemTotal = atof(line)
		case "boot":
			// { sec = 1795511299, usec = 0 } Tue Nov 24 17:08:19 2026
			np.Uptime = -1
			boot := strings.Fields(line)
			for i, tk := range boot {
				if tk == "sec" && i+2 < len(boot) {
					np.Uptime = float64(s.Time.Unix()) - atof(strings.Trim(boot[i+2], ","))
				}
			}
		case "now":
			sec["now"] = atof(line)
		case "mem":
			switch {
			case strings.HasPrefix(line, "Pages free"):
				sec["free"] = num(line)
			case strings.HasPrefix(line, "Pages active"):
				sec["active"] = num(line)
			case strings.HasPrefix(line, "Pages inactive"):
				sec["inactive"] = num(line)
			case strings.HasPrefix(line, "Pages speculative"):
				sec["spec"] = num(line)
			case strings.HasPrefix(line, "Pages wired"):
				sec["wired"] = num(line)
			case strings.HasPrefix(line, "Pages occupied by compressor"):
				sec["cmpr"] = num(line)
			case strings.HasPrefix(line, "Mach Virtual"):
				if v := num(line); v > 0 {
					sec["page"] = v
				}
			}
		case "disk":
			f := strings.Fields(line)
			if len(f) >= 5 && strings.HasPrefix(f[0], "/dev/") {
				np.DiskTotal = atof(f[1]) * 1024
				np.DiskUsed = atof(f[2]) * 1024
				np.DiskPct = atof(strings.TrimSuffix(f[4], "%"))
			}
		case "proc":
			// 桌面图标（Web Clip）跑的是 /Applications/Web.app/Web，不是 MobileSafari。
			// 只认 MobileSafari 的话，他用图标打开着面板我这边却报"需重开"，等于面板说谎
			if strings.Contains(line, "MobileSafari.app/MobileSafari") {
				np.Safari = true
			}
			if strings.Contains(line, "/Applications/Web.app/Web") {
				np.Clip = true
			}
			if strings.Contains(line, "WebContent") {
				np.Tabs++
			}
		}
	}
	if ps := sec["page"]; ps > 0 {
		// 活跃页 + 常驻页 + 压缩池才是"真的占着的内存"，非活跃页随时能被回收
		np.MemUsed = (sec["active"] + sec["wired"] + sec["cmpr"]) * ps
		if np.MemTotal > 0 {
			np.MemPct = np.MemUsed / np.MemTotal * 100
		}
	}
	if sec["now"] > 1e9 {
		np.Skew = sec["now"] - float64(s.Time.Unix())
	}
	// 重启检测：上一轮明明已经运行了很久，这一轮 uptime 反而小了一大截，
	// 中间只能是重启过。这台设备的越狱是半绑定的，重启后 sshd 起不起得来是未知数，
	// 所以他必须第一时间知道——这件事只发生一次，进日志栏比在卡片上常驻有用
	if np.On && np.Uptime > 0 {
		if c.ipadPrevUp > np.Uptime+600 {
			np.Rebooted = true
			c.LogEvent("iPad", "面板机重启过（重启前已运行 %s），SSH 探针可能要重新配", HumanExposure(c.ipadPrevUp))
		}
		c.ipadPrevUp = np.Uptime
	}
	c.ipadCache = np
	s.IPad = np
	c.ipadPage(&s.IPad)
}

// pageProc 有没有承载面板页的进程还活着：Safari 和桌面图标（Web.app）都算
func (p *IPad) pageProc() bool { return p.Safari || p.Clip }

// ipadNote 每一帧都判，不按探测时刻判：探测 30 秒一次，
// 只在探测那一帧报的话，屏幕上这条提示十分之九的时间是消失的。
func (c *Collector) ipadNote(p *IPad) {
	switch {
	case !p.Configured:
	case p.PageOK && !p.On:
		c.note("iPad 页面在显示，但 SSH 细节取不到：%s", p.Err)
	case !p.On:
		c.note("iPad 面板机 SSH 不通：%s", p.Err)
	case p.Rebooted:
		c.note("iPad 刚重启过：越狱是半绑定的，探针可能要重新跑一次越狱才连得上")
	case !p.pageProc():
		c.note("iPad 上 Safari 和桌面图标都没在跑，面板页要重新点一次才会显示")
	case !p.PageOK && p.PageSeen >= 0:
		c.note("iPad 页面 %.0f 秒没取数了：熄屏或被挂后台了", p.PageSeen)
	// 卡片撤了以后，这台设备只以"出问题才出声"的方式存在，内存是唯一的先兆指标：
	// 768MB 的老 Safari 被系统回收，就是画面突然不动的前一步
	case p.On && p.MemPct >= 85:
		c.note("iPad 内存 %.0f%%，页面随时可能被系统回收，看到了就去重开一次", p.MemPct)
	}
}

// ipadPage 把"最近有没有人真的在页面上取数"接进来——这比进程表硬：
// SSE 还在跳就说明画面是活的。HTTP 没起来时（纯屏幕模式）保持未知，别乱报。
func (c *Collector) ipadPage(p *IPad) {
	if c.sees == nil || c.cfg.IPadHost == "" {
		p.PageSeen = -1 // 没起 HTTP，页面活跃与否无从判断，别让它显示成"刚刚"
		return
	}
	t := c.sees(c.cfg.IPadHost)
	if t.IsZero() {
		p.PageOK, p.PageSeen = false, -1
		return
	}
	p.PageSeen = time.Since(t).Seconds()
	p.PageOK = p.PageSeen < ipadEvery.Seconds()+20
}

const linkEvery = 15 * time.Second

// pingOne 跑一轮设备上的 ping 并把平均往返和丢包率拆出来。
// 这台机器 busybox 的实际输出（不是猜的）：
//
//	3 packets transmitted, 3 packets received, 0% packet loss
//	round-trip min/avg/max = 1.726/1.797/1.906 ms
//
// 全丢包时第二行根本没有，所以往返和丢包要分开判断，别把"没测到"写成 0ms。
func pingOne(host string) (ms, loss float64, got bool, err string) {
	out, e := runTO(9*time.Second, "ping", "-c", "3", "-W", "2", "-q", host)
	if e != nil {
		return 0, 0, false, strings.TrimSpace(fmt.Sprintf("%v", e))
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "% packet loss") {
			for _, f := range strings.Fields(line) {
				if strings.HasSuffix(f, "%") {
					loss = atof(strings.TrimSuffix(f, "%"))
				}
			}
			got = true
		}
		if i := strings.Index(line, "min/avg/max"); i >= 0 {
			if j := strings.Index(line[i:], "="); j >= 0 {
				fs := strings.Fields(line[i+j+1:])
				if len(fs) > 0 {
					if ps := strings.Split(fs[0], "/"); len(ps) == 3 {
						ms = atof(ps[1])
					}
				}
			}
		}
	}
	return ms, loss, got, ""
}

// resolveMs 走本机 resolv.conf 那条路（dnsmasq→内核 DNS），量的是"网页打开慢不慢"
// 里 DNS 那一段的真实体感，不是绕过代理直连某台 DNS 的理想值。
// 结果被 dnsmasq 缓存时会是亚毫秒，这没错——缓存命中本来就快，真要出问题的是超时那几次。
func resolveMs() (float64, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	r := &net.Resolver{PreferGo: true}
	start := time.Now()
	_, err := r.LookupHost(ctx, "www.baidu.com")
	dt := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		s := err.Error()
		switch {
		case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
			return dt, "超时"
		case strings.Contains(s, "no such host"):
			return dt, "域名解析不到"
		case strings.Contains(s, "server misbehaving"):
			return dt, "DNS 应答异常"
		}
		return dt, shorten(strings.TrimSpace(s), 40)
	}
	return dt, ""
}

// pppCounts 从 pppd 日志里数今天的拨号成功与断线。
// 日志是环形缓冲（现在能追到 10 小时前），所以这是"日志里看到的次数"，
// 面板重启或缓冲滚掉的部分不在内——因此一次都没有时不能当"今天没掉线"讲，
// 用 NoLog 标出来，界面上显示成"—"而不是 0。
func (c *Collector) pppCounts(l *Link) {
	out, _ := runTO(1500*time.Millisecond, "logread", "-e", "pppd")
	if strings.TrimSpace(out) == "" {
		l.NoLog = true
		return
	}
	now := time.Now()
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "pppd") {
			continue
		}
		t := syslogTime(line)
		if t.Year() != now.Year() || t.Month() != now.Month() || t.Day() != now.Day() {
			continue
		}
		switch {
		case strings.Contains(line, "local") && strings.Contains(line, "IP address"):
			l.DialToday++
			if t.After(l.LastDial) {
				l.LastDial = t
			}
		case strings.Contains(line, "Timeout waiting for PADS"),
			strings.Contains(line, "Connection terminated"),
			strings.Contains(line, "sent PADT"):
			l.DropToday++
			if t.After(l.LastDrop) {
				l.LastDrop = t
			}
		}
	}
}

// link 只做一件事：把后台那一圈探测的结果贴到本帧上。
// 探测本身绝不放在渲染循环里跑——ping 一轮最坏要十几秒，
// 而"外网正在断"恰好就是那几秒里，屏幕卡住不动比少一个数字糟得多。
func (c *Collector) link(s *Snap) {
	c.linkMu.Lock()
	l := c.linkCache
	first := !c.linkStarted
	if first {
		c.linkStarted = true
	}
	c.linkMu.Unlock()
	s.Link = l
	if first {
		go c.linkLoop()
	}
}

func (c *Collector) linkLoop() {
	for {
		c.lastMu.Lock()
		sn := c.lastSnap
		c.lastMu.Unlock()
		if sn == nil {
			// 第一帧还没采好，这时候说"WAN 没起来"是编的：留着空结果，
			// 界面显示"未探测"，页脚也不出声
			time.Sleep(time.Second)
			continue
		}
		l := Link{ProbeT: time.Now(), GW: sn.Wan.GW, Dns: firstHost(sn.Wan.DNS)}
		if l.GW == "" {
			l.Err = "没有拨号网关（WAN 没起来？）"
		} else {
			ms, loss, got, perr := pingOne(l.GW)
			l.GwMs, l.GwLoss = ms, loss
			if perr != "" {
				l.Err = "网关 ping 失败：" + perr
			} else if !got {
				l.Err = "网关 ping 输出读不懂"
			}
			if l.Dns != "" && l.Err == "" {
				dms, _, _, _ := pingOne(l.Dns)
				l.DnsMs = dms
			}
			l.ResolveMs, l.ResolveErr = resolveMs()
			c.pppCounts(&l)
		}
		c.linkMu.Lock()
		c.linkCache = l
		c.linkMu.Unlock()
		time.Sleep(linkEvery)
	}
}

// linkNote 只在链路本身不对劲时出声，正常态不进页脚：
// 页脚是给人看异常的，常驻一行"网关 2ms"只会把真正的那条挤掉
func (c *Collector) linkNote(l *Link) {
	switch {
	case l.Err != "":
		c.note("线路探测异常：%s", l.Err)
	case l.GwLoss >= 34:
		c.note("到网关 %s 丢包 %.0f%%，家里到运营商这一段有问题", l.GW, l.GwLoss)
	case l.GwLoss > 0:
		c.note("到网关 %s 丢包 %.0f%%", l.GW, l.GwLoss)
	case l.GwMs >= 50:
		c.note("到网关 %s 平均 %.0fms，比平时慢", l.GW, l.GwMs)
	}
	if l.ResolveErr != "" {
		c.note("DNS 解析异常：%s", l.ResolveErr)
	}
	if l.DropToday > 0 {
		c.note("今天 pppd 记到 %d 次断线超时，最近 %s", l.DropToday, l.LastDrop.Format("15:04"))
	}
}

// firstHost 从一串 DNS 服务器里取第一个能用的
func firstHost(list []string) string {
	for _, h := range list {
		if h != "" {
			return h
		}
	}
	return ""
}

// HumanExposure 专给"IP 已暴露多久"用：这个数是要人去处置的，
// 一上来只写"0 分"等于没说，所以秒级起步，超过一小时才收成小时
func HumanExposure(sec float64) string {
	n := int64(sec + 0.5)
	switch {
	case n < 60:
		return fmt.Sprintf("%d 秒", n)
	case n < 3600:
		return fmt.Sprintf("%d 分 %d 秒", n/60, n%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", n/3600, (n%3600)/60)
	}
}

// ---------------- 小工具 ----------------

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func runTO(to time.Duration, name string, arg ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil && out.Len() == 0 {
		return "", fmt.Errorf("%v %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), err
}

func getJSON(ctx context.Context, endpoint string, hdr map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	for k, s := range hdr {
		req.Header.Set(k, s)
	}
	res, err := (&http.Client{Timeout: 2500 * time.Millisecond}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("http %d", res.StatusCode)
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return json.Unmarshal(b, v)
}

func unixHTTP(sock, method, path string, to time.Duration) (string, error) {
	if _, err := os.Stat(sock); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, "http://unix"+path, nil)
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: to}).DialContext(ctx, "unix", sock)
	}}
	res, err := (&http.Client{Transport: tr, Timeout: to}).Do(req)
	if err != nil {
		tr.CloseIdleConnections() // 每轮都新建 Transport，不主动关就会把 socket 一直攥在手里
		return "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	tr.CloseIdleConnections()
	return string(b), nil
}

// ---------------- CPU / 温度 / 内存 ----------------

func (c *Collector) cpu(s *Snap) {
	data := readFile("/proc/stat")
	if data == "" {
		return
	}
	var total [2]uint64
	var cores [][2]uint64
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var t, idle uint64
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			t += n
			if i == 3 || i == 4 { // idle + iowait
				idle += n
			}
		}
		if f[0] == "cpu" {
			total = [2]uint64{t, idle}
		} else {
			cores = append(cores, [2]uint64{t, idle})
		}
	}
	if c.cpuTotal[0] > 0 {
		if dt := float64(total[0] - c.cpuTotal[0]); dt > 0 {
			s.CPUPct = clamp01(1-float64(total[1]-c.cpuTotal[1])/dt) * 100
		}
		for i, cur := range cores {
			if i >= len(c.cpuPrev) {
				break
			}
			p := c.cpuPrev[i]
			if dt := float64(cur[0] - p[0]); dt > 0 {
				s.CorePct = append(s.CorePct, clamp01(1-float64(cur[1]-p[1])/dt)*100)
			} else {
				s.CorePct = append(s.CorePct, 0)
			}
		}
	}
	c.cpuTotal, c.cpuPrev = total, cores

	if l := strings.Fields(readFile("/proc/loadavg")); len(l) >= 3 {
		s.Load = [3]float64{atof(l[0]), atof(l[1]), atof(l[2])}
	}
	for _, line := range strings.Split(readFile("/proc/cpuinfo"), "\n") {
		if strings.HasPrefix(line, "cpu MHz") {
			s.Freq = atof(strings.SplitN(line, ":", 2)[1]) / 1000
			break
		}
	}
}

func (c *Collector) temps(s *Snap) {
	zs, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	sort.Strings(zs)
	for _, z := range zs {
		typ := readFile(filepath.Join(z, "type"))
		v := atof(readFile(filepath.Join(z, "temp"))) / 1000
		if v <= 0 || v > 150 {
			continue
		}
		if strings.Contains(typ, "pkg") || strings.Contains(typ, "x86") {
			if s.TempPkg == 0 {
				s.TempPkg = v
			}
		} else if strings.Contains(typ, "acpitz") {
			if s.TempBoard == 0 {
				s.TempBoard = v
			}
		}
	}
}

func (c *Collector) mem(s *Snap) {
	m := map[string]float64{}
	for _, line := range strings.Split(readFile("/proc/meminfo"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			m[strings.TrimSuffix(f[0], ":")] = atof(f[1]) * 1024
		}
	}
	s.MemTotal = m["MemTotal"]
	avail, ok := m["MemAvailable"]
	if !ok {
		avail = m["MemFree"] + m["Buffers"] + m["Cached"]
	}
	if s.MemTotal > 0 {
		s.MemUsed = s.MemTotal - avail
		s.MemPct = s.MemUsed / s.MemTotal * 100
	}
}

// ---------------- 网络 ----------------

func virtualIface(n string) bool {
	for _, p := range []string{"lo", "br-", "veth", "docker", "tap-", "virbr", "dummy",
		"teql", "sit", "gre", "ip6", "erspan", "siit", "bonding", "ifb"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func (c *Collector) net(s *Snap) {
	now := time.Now()
	dt := 0.0
	if !c.netT.IsZero() {
		dt = now.Sub(c.netT).Seconds()
	}
	ifs, _ := filepath.Glob("/sys/class/net/*")
	sort.Strings(ifs)
	var aggRX, aggTX float64
	for _, p := range ifs {
		name := filepath.Base(p)
		if virtualIface(name) {
			continue
		}
		rxb := readFile(filepath.Join(p, "statistics", "rx_bytes"))
		if rxb == "" {
			continue
		}
		it := Iface{Name: name,
			Up: readFile(filepath.Join(p, "operstate")) == "up",
			RX: uint64(atof(rxb)),
			TX: uint64(atof(readFile(filepath.Join(p, "statistics", "tx_bytes"))))}
		if prev, seen := c.netPrev[name]; seen && dt > 0 {
			if it.RX >= prev[0] {
				it.RXr = float64(it.RX-prev[0]) * 8 / dt
			}
			if it.TX >= prev[1] {
				it.TXr = float64(it.TX-prev[1]) * 8 / dt
			}
		}
		aggRX += it.RXr
		aggTX += it.TXr
		s.Ifaces = append(s.Ifaces, it)
		c.netPrev[name] = [2]uint64{it.RX, it.TX}
	}
	c.netT = now

	out, err := runTO(2*time.Second, "ubus", "call", "network.interface.wan", "status")
	if err != nil && out == "" {
		c.note("wan 状态取不到: %v", err)
		return
	}
	// ubus 的 JSON 形状不固定：dns-server 可能是 {"ipv4":[...]} 也可能是 [...]，
	// 默认路由的 target 可能是 "0.0.0.0/0" 也可能是 "0.0.0.0"+mask。
	// 之前按一种形状写死，解析失败还静默 return，屏上就一直显示"未连接"。
	var w struct {
		Up     bool    `json:"up"`
		Uptime float64 `json:"uptime"`
		Proto  string  `json:"proto"`
		L3     string  `json:"l3_device"`
		Ipv4   []struct {
			Address string `json:"address"`
			Mask    int    `json:"mask"`
		} `json:"ipv4-address"`
		Route []struct {
			Target  string `json:"target"`
			Mask    int    `json:"mask"`
			Gateway string `json:"gateway"`
			Nexthop string `json:"nexthop"`
		} `json:"route"`
		DNS json.RawMessage `json:"dns-server"`
	}
	if e := json.Unmarshal([]byte(out), &w); e != nil {
		c.note("wan JSON 解析失败: %v", e)
		return
	}
	s.Wan = Wan{Up: w.Up, Proto: w.Proto, IfName: w.L3, Uptime: w.Uptime}
	for _, r := range w.Route {
		if r.Target != "0.0.0.0/0" && !(r.Target == "0.0.0.0" && r.Mask == 0) {
			continue
		}
		s.Wan.GW = defStr(r.Gateway, r.Nexthop)
	}
	if len(w.Ipv4) > 0 {
		s.Wan.IP, s.Wan.Mask = w.Ipv4[0].Address, w.Ipv4[0].Mask
	}
	// 默认路由才是"能不能出去"的证据。只看 network.interface.wan 的 up 会把
	// 旁路由（挂在主路由下面当二级设备，根本没有拨号口）报成"未连接"——假故障。
	dv, via, rproto := defaultRoute()
	s.Wan.Connected = w.Up || dv != ""
	if s.Wan.GW == "" {
		s.Wan.GW = via
	}
	s.Wan.Via = via
	s.Wan.Mode = wanMode(w.Proto, dv, rproto)
	// dns-server 两种形状都试一遍
	var dnsObj struct {
		IPv4 []string `json:"ipv4"`
	}
	if json.Unmarshal(w.DNS, &dnsObj) == nil && len(dnsObj.IPv4) > 0 {
		s.Wan.DNS = dnsObj.IPv4
	} else {
		var arr []string
		if json.Unmarshal(w.DNS, &arr) == nil {
			s.Wan.DNS = arr
		}
	}
	var keepDNS []string
	for _, d := range s.Wan.DNS {
		if !strings.HasPrefix(d, "127.") {
			keepDNS = append(keepDNS, d)
		}
	}
	s.Wan.DNS = keepDNS
	for _, it := range s.Ifaces {
		if it.Name == s.Wan.IfName {
			s.Wan.RXr, s.Wan.TXr = it.RXr, it.TXr
		}
	}
	if s.Wan.RXr == 0 && s.Wan.TXr == 0 { // pppoe 未拨通时退回物理口合计
		s.Wan.RXr, s.Wan.TXr = aggRX, aggTX
	}
	c.wanUp = s.Wan.Up

	o2, _ := runTO(1200*time.Millisecond, "ip", "-4", "-o", "addr", "show", "dev", "br-lan")
	f2 := strings.Fields(o2)
	for i, v := range f2 {
		if v == "inet" && i+1 < len(f2) {
			s.LanIP = f2[i+1]
		}
	}
	if b, err := os.ReadFile("/tmp/dhcp.leases"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) != "" {
				s.Leases++
			}
		}
	}
}

// lanNetOf 把 "192.168.1.1/24" 这种"地址+掩码"算成网段，用来判断某个 IP 是不是内网设备。
// 掩码丢了就按 /24 处理：绝大多数家用 LAN 就是这个，猜错也只是多列或少列几台设备，
// 不会把面板画坏。
func lanNetOf(cidr string) *net.IPNet {
	if strings.TrimSpace(cidr) == "" {
		return nil
	}
	if !strings.Contains(cidr, "/") {
		cidr += "/24"
	}
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil
	}
	return n
}

// defaultRoute 读默认路由，返回出接口 / 下一跳 / 协议。
// busybox 的 ip 输出是一行 `default via 192.168.1.1 dev eth0 proto dhcp src ...`，
// 有多条默认路由时按 ip 自己的顺序取第一条（metric 最小的那条）。
func defaultRoute() (dev, via, proto string) {
	o, _ := runTO(1200*time.Millisecond, "ip", "-4", "-o", "route", "show", "default")
	for _, l := range strings.Split(o, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != "default" {
			continue
		}
		for i, v := range f {
			switch v {
			case "dev":
				if i+1 < len(f) {
					dev = f[i+1]
				}
			case "via":
				if i+1 < len(f) {
					via = f[i+1]
				}
			case "proto":
				if i+1 < len(f) {
					proto = f[i+1]
				}
			}
		}
		if dev != "" || via != "" {
			return dev, via, proto
		}
	}
	return "", "", ""
}

// wanMode 把"这台机器到底是怎么出去的"翻译成人话。判断顺序是有讲究的：
// 先认拨号（ppp 口是硬证据），再认旁路由——旁路由的默认路由走的是自己那块桥，
// 这时 uci 里的 wan 口多半是空的，绝不能因为它空就说人没网。
// 走到"有出接口但认不出协议"这一档，退回"静态 IP"：手工配一条默认路由是唯一常见解释。
func wanMode(proto, dev, rproto string) string {
	p := strings.ToLower(proto)
	switch {
	case p == "pppoe" || strings.HasPrefix(dev, "ppp"):
		return "PPPoE"
	case strings.HasPrefix(dev, "br-") || strings.HasPrefix(dev, "lan"):
		return "旁路由"
	case p == "dhcp" || rproto == "dhcp":
		return "DHCP"
	case p == "static" || rproto == "static" || dev != "":
		return "静态 IP"
	default:
		return protoLabel(p)
	}
}

// ---------------- 接入设备 ----------------

// 每台设备能给三件事：谁（DHCP 名字 / MAC）、在不在线（ARP 状态）、
// 在跑多少（conntrack 按 IP 累计字节的差分 + 会话数）。
// 之所以用 conntrack 而不是建防火墙计数规则：那要改网络配置，用户明确不让动。
type Device struct {
	Name  string
	IP    string
	MAC   string
	Conns int
	// Panel 非空说明这台就是 iPad 面板机，值就是它的画面状态
	Panel string
	RXr   float64 // bps，路由器发给它 = 下行
	TXr   float64 // bps，它发给路由器 = 上行
	On    bool
	State string
}

func (c *Collector) devices(s *Snap) {
	lan := map[string]*Device{}
	touch := func(ip string) *Device {
		if d, ok := lan[ip]; ok {
			return d
		}
		d := &Device{IP: ip}
		lan[ip] = d
		return d
	}
	// DHCP 名字
	if b, err := os.ReadFile("/tmp/dhcp.leases"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 4 {
				continue
			}
			d := touch(f[2])
			d.MAC = f[1]
			if n := strings.TrimSpace(f[3]); n != "" && n != "*" && n != "-" {
				d.Name = n
			}
		}
	} else {
		c.note("读不到 /tmp/dhcp.leases")
	}
	// ARP 状态：REACHABLE/DELAY/STALE 算活着，FAILED 就是掉线
	// 网段从 br-lan 自己的地址+掩码算，不写死某个固定网段：那是作者自己的网，
	// 换一家就一台设备都列不出来。取不到 br-lan 时不做网段过滤，宁可多列也别空着。
	lanNet := lanNetOf(s.LanIP)
	gw := s.LanIP
	if i := strings.IndexByte(gw, '/'); i >= 0 {
		gw = gw[:i]
	}
	inside := func(ip string) bool {
		if lanNet == nil {
			return true
		}
		a := net.ParseIP(ip)
		return a != nil && lanNet.Contains(a)
	}
	o, _ := runTO(1200*time.Millisecond, "ip", "-4", "-o", "neigh", "show", "dev", "br-lan")
	for _, l := range strings.Split(o, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		ip := f[0]
		if !inside(ip) {
			continue
		}
		d := touch(ip)
		d.State = f[len(f)-1]
		switch d.State {
		case "REACHABLE", "DELAY", "STALE":
			d.On = true
			if d.MAC == "" {
				for i, v := range f {
					if v == "lladdr" && i+1 < len(f) {
						d.MAC = f[i+1]
					}
				}
			}
		}
	}
	// conntrack 累计字节 + 会话数
	if b, err := os.ReadFile("/proc/net/nf_conntrack"); err == nil {
		cur := map[string][2]uint64{}
		// OpenClash 转发时 reply 元组的 src 就是网关自己，先排掉网关，
		// 否则下行字节会被当成"路由器流量"丢掉（设备列表 RX 永远 0 就是这么来的）
		lanOf := func(ip string) bool {
			return inside(ip) && ip != gw
		}
		type tuple struct {
			src, dst string
			bytes    uint64
		}
		for _, l := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(l, "ipv4") {
				continue
			}
			// 一行里有 orig 和 reply 两个元组，各自带 src/dst/bytes。
			// 只读第一个元组的话，"设备收到的下行字节"永远算不出来（踩过：
			// 设备列表 RXr 全是 0）。元组内顺序固定是 src=…dst=…packets=…bytes=。
			var tups []tuple
			var cu tuple
			for _, tok := range strings.Fields(l) {
				k, v, ok := strings.Cut(tok, "=")
				if !ok {
					continue
				}
				switch k {
				case "src":
					if cu.src == "" {
						cu.src = v
					}
				case "dst":
					if cu.dst == "" {
						cu.dst = v
					}
				case "bytes":
					cu.bytes = uint64(atof(v))
					if cu.src != "" && cu.dst != "" {
						tups = append(tups, cu)
					}
					cu = tuple{}
				}
			}
			mine := ""
			for _, t := range tups {
				lanIP, isSrc := "", false
				switch {
				case lanOf(t.src):
					lanIP, isSrc = t.src, true
				case lanOf(t.dst):
					lanIP = t.dst
				default:
					continue
				}
				pair := cur[lanIP]
				if isSrc {
					pair[1] += t.bytes // 设备发出
				} else {
					pair[0] += t.bytes // 设备收到
				}
				cur[lanIP] = pair
				mine = lanIP
			}
			if mine != "" {
				touch(mine).Conns++ // 会话数按连接算，一条连接两个元组只数一次
			}
		}
		dt := time.Since(c.ctT).Seconds()
		if dt > 0.5 && c.ctPrev != nil {
			for ip, v := range cur {
				p := c.ctPrev[ip]
				d := touch(ip)
				// 连接老化会丢计数，差分为负就当 0，不能显示成爆发流量
				if v[0] > p[0] {
					d.RXr = float64(v[0]-p[0]) * 8 / dt
				}
				if v[1] > p[1] {
					d.TXr = float64(v[1]-p[1]) * 8 / dt
				}
			}
		}
		c.ctPrev, c.ctT = cur, time.Now()
	} else {
		c.note("读不到 nf_conntrack，设备速率不可用")
	}

	for ip, d := range lan {
		if d.Name == "" && d.State == "" {
			delete(lan, ip) // 只在 DHCP 里出现过、现在彻底没动静的，不占位置
			continue
		}
		if d.Name == "" {
			if ip == c.cfg.FnHost {
				d.Name = "fnOS 虚拟机"
			} else {
				d.Name = "未命名设备"
			}
		}
		if d.On || d.RXr+d.TXr > 0 {
			s.DevOnline++
		}
		s.Devs = append(s.Devs, *d)
	}
	sort.Slice(s.Devs, func(i, j int) bool {
		a, b := s.Devs[i], s.Devs[j]
		if a.On != b.On {
			return a.On
		}
		if x, y := a.RXr+a.TXr, b.RXr+b.TXr; x != y {
			return x > y
		}
		return a.IP < b.IP
	})
	if len(s.Devs) > 12 {
		s.Devs = s.Devs[:12]
	}
	c.ipadTag(s)
}

// ipadTag 给设备列表里那一行打状态标签。顶栏第四枚药丸已经排到日期底下了，
// 与其挤掉别的状态，不如把这件事标在它本来就在的地方——屏幕上那行右侧的"空闲"换成画面状态。
func (c *Collector) ipadTag(s *Snap) {
	if c.cfg.IPadHost == "" || !s.IPad.Configured {
		return
	}
	i := s.IPad
	// 顺序按"人能看到什么"排：页面在动就说明屏幕是好的，SSH 挂了只是拿不到细节，
	// 这时候报"失联"会把真相说反
	tag := "显示中"
	switch {
	case i.PageOK:
	case !i.On:
		tag = "失联"
	case !i.pageProc():
		tag = "需重开"
	case i.PageSeen >= 0:
		tag = "停止刷新"
	}
	for k := range s.Devs {
		if s.Devs[k].IP == c.cfg.IPadHost {
			s.Devs[k].Panel = tag
		}
	}
}

func (c *Collector) quickstart(s *Snap) {
	body, err := unixHTTP("/var/run/quickstart/local.sock", "GET", "/api/lcd/simple/?disk=1", 2*time.Second)
	if err != nil {
		c.note("quickstart: %v", err)
		return
	}
	var q Quick
	if json.Unmarshal([]byte(body), &q) != nil {
		c.note("quickstart 返回无法解析")
		return
	}
	q.OK = true
	s.Quick = q
	if q.Devices > 0 {
		s.DevCount = q.Devices
	}
}

// ---------------- OpenClash ----------------

// groupPref 现在只是"用户还没勾分组时"的兜底猜测：拿名字里出现 PROXY/节点选择 这类
// 常见字样排序，谁排第一就当主分组显示。它不是承诺——各家 yaml 名字不一样，
// 所以卡片上显示什么最终以用户勾的为准（见 pickGroups）。
var groupPref = []string{"PROXY", "节点选择", "手动选择", "机场", "Default", "GEO", "Fallback"}

// ShowGroups 取用户勾选的分组名（按勾选顺序）。第一次调用时从文件读，之后用内存副本。
func (c *Collector) ShowGroups() []string {
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()
	if !c.grpLoaded {
		c.grpLoaded = true
		if c.cfg.GroupsFile != "" {
			b, err := os.ReadFile(c.cfg.GroupsFile)
			if err == nil {
				for _, l := range strings.Split(string(b), "\n") {
					if n := strings.TrimSpace(l); n != "" {
						c.showGrp = append(c.showGrp, n)
					}
				}
			}
		}
	}
	return append([]string(nil), c.showGrp...)
}

// SetShowGroups 把勾选存下来。存文件用一行一个名字，而不是塞进 uci：
// 分组名是自由文本，中文、空格、引号都可能出现，多过一层转义就多一类
// "存进去了但读不出来"的查不动的毛病。空数组是合法值，意思是"我没选，退回兜底"。
func (c *Collector) SetShowGroups(names []string) error {
	if c.cfg.GroupsFile == "" {
		return fmt.Errorf("没有可写的分组配置文件路径")
	}
	body := ""
	for _, n := range names {
		body += n + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(c.cfg.GroupsFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(c.cfg.GroupsFile, []byte(body), 0o644); err != nil {
		return err
	}
	c.groupsMu.Lock()
	c.showGrp = append([]string(nil), names...)
	c.grpLoaded = true
	c.groupsMu.Unlock()
	return nil
}

// pickGroups 按用户勾的顺序捞出还活着的分组：yaml 改过名、订阅换过，勾过的名字
// 就自动掉出去，卡片上不会留一行空的或指错对象的。
func (c *Collector) pickGroups(all []ProxyGroup) []ProxyGroup {
	want := c.ShowGroups()
	var out []ProxyGroup
	for _, n := range want {
		for _, g := range all {
			if g.Name == n {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

func (c *Collector) clash(s *Snap) {
	// 有没有操作在跑，属于"当前状态"，每一帧都要带上，不能只在操作那一帧出现
	s.Clash.Op = c.currentOp()
	cl := &s.Clash
	if e, _ := runTO(1200*time.Millisecond, "uci", "-q", "get", "openclash.config.enable"); strings.TrimSpace(e) == "1" {
		cl.Enabled = true
	}
	if _, err := os.Stat("/etc/openclash"); err == nil {
		cl.Installed = true
	}
	if !cl.Installed {
		cl.Err = "未安装 OpenClash"
		return
	}

	ctrl := "127.0.0.1:9090"
	// OpenClash 运行时用的是 /etc/openclash/config/*.yaml，那份生成文件里往往
	// 没有 secret；真正的密钥在同级目录的源配置里。只认 config_path 就会一直 401，
	// 面板于是永远显示"内核未运行"。这里把几个来源都收进候选列表。
	var secrets []string
	var ports []string
	addPort := func(v string) {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if v == "" {
			return
		}
		for _, x := range ports {
			if x == v {
				return
			}
		}
		ports = append(ports, v)
	}
	var auth []string
	addAuth := func(v string) {
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if v != "" && !containsStr(auth, v) {
			auth = append(auth, v)
		}
	}
	addSecret := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), `"'`)
		if s == "" {
			return
		}
		for _, x := range secrets {
			if x == s {
				return
			}
		}
		secrets = append(secrets, s)
	}
	scan := func(path string) {
		y, err := os.ReadFile(path)
		if err != nil {
			return
		}
		inAuth := false
		for _, line := range strings.Split(string(y), "\n") {
			t := strings.TrimSpace(line)
			// authentication: 下面是 "- 用户: 密码"，代理口要 Basic 认证才认
			if strings.HasPrefix(line, "authentication:") {
				inAuth = len(strings.TrimSpace(strings.TrimPrefix(line, "authentication:"))) == 0
				continue
			}
			if inAuth {
				if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
					if v, ok := strings.CutPrefix(t, "-"); ok {
						addAuth(v)
					}
					continue
				}
				inAuth = false
			}
			if v, ok := strings.CutPrefix(t, "external-controller:"); ok && ctrl == "127.0.0.1:9090" {
				// yaml 里常写成 '127.0.0.1:9090'，引号不剥掉拼出来的 URL 就是非法端口
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				if i := strings.LastIndex(v, ":"); i >= 0 {
					ctrl = "127.0.0.1" + v[i:] // 0.0.0.0:9090 之类一律走本机回环
				} else if v != "" {
					ctrl = v
				}
			}
			if v, ok := strings.CutPrefix(t, "secret:"); ok {
				addSecret(v)
			}
			// mixed-port 同时支持 HTTP CONNECT 与 SOCKS，纯 port 口常常拒绝 CONNECT
			for _, k := range []string{"mixed-port:", "port:", "socks-port:"} {
				if v, ok := strings.CutPrefix(line, k); ok { // 只认顶格键，别把嵌套结构里的 port 当成口
					addPort(strings.Trim(strings.TrimSpace(v), `"'`))
				}
			}
		}
	}
	if p, _ := runTO(1200*time.Millisecond, "uci", "-q", "get", "openclash.config.config_path"); strings.TrimSpace(p) != "" {
		cl.ConfigFile = strings.TrimSpace(p)
		scan(cl.ConfigFile)
	}
	if ds, _ := filepath.Glob("/etc/openclash/*.yaml"); len(ds) > 0 {
		for _, d := range ds {
			scan(d)
		}
	}
	if s, _ := runTO(1200*time.Millisecond, "uci", "-q", "get", "openclash.config.secret"); s != "" {
		addSecret(s)
	}
	// OpenClash 真正把哪个口当混合口写在 uci 里，生成的 yaml 往往只有 port/socks-port
	for _, k := range []string{"mixed_port", "proxy_port", "http_port"} {
		if v, _ := runTO(1200*time.Millisecond, "uci", "-q", "get", "openclash.config."+k); strings.TrimSpace(v) != "" {
			addPort(strings.TrimSpace(v))
		}
	}
	var pxOrder []string
	if c.proxyOK != "" {
		pxOrder = append(pxOrder, c.proxyOK)
	}
	for _, p := range ports {
		if p != c.proxyOK {
			pxOrder = append(pxOrder, p)
		}
	}
	c.proxyPort = pxOrder
	c.proxyAuth = auth
	cl.CtrlAddr = ctrl
	base := "http://" + ctrl
	c.clashBase = base

	// 试到通为止；上次成功的那个放最前面，避免每轮都从 401 开始试
	var order []string
	if c.clashSecret != "" {
		order = append(order, c.clashSecret)
	}
	order = append(order, secrets...)
	order = append(order, "") // 有的配置根本没设密钥
	hdr := map[string]string{}
	type ver struct {
		Version string `json:"version"`
	}
	var vv ver
	ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
	defer cancel()
	var lastErr error
	for _, cand := range order {
		h := map[string]string{}
		if cand != "" {
			h["Authorization"] = "Bearer " + cand
		}
		vv = ver{}
		if err := getJSON(ctx, base+"/version", h, &vv); err != nil {
			lastErr = err
			continue
		}
		hdr, c.clashSecret = h, cand
		break
	}
	if vv.Version == "" {
		cl.Running = false
		if lastErr != nil {
			cl.Err = fmt.Sprintf("控制接口无响应: %v", lastErr)
		} else if cl.Enabled {
			cl.Err = "内核未运行"
		} else {
			cl.Err = "配置中已关闭"
		}
		return
	}
	cl.Running, cl.Version = true, vv.Version
	cl.Err = ""

	var cf struct {
		Mode string `json:"mode"`
	}
	getJSON(ctx, base+"/configs", hdr, &cf)
	cl.Mode = cf.Mode

	var px struct {
		Proxies map[string]struct {
			Type    string   `json:"type"`
			Now     string   `json:"now"`
			All     []string `json:"all"`
			History []struct {
				Delay float64 `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	if getJSON(ctx, base+"/proxies", hdr, &px) == nil {
		names := make([]string, 0, len(px.Proxies))
		for n := range px.Proxies {
			names = append(names, n)
		}
		sort.Strings(names)
		best := -1
		for _, n := range names {
			p := px.Proxies[n]
			switch p.Type {
			case "Selector", "URLTest", "Fallback":
				cl.Groups = append(cl.Groups, ProxyGroup{Name: n, Now: p.Now, All: p.All})
			case "Direct", "Reject", "Pass":
			default:
				cl.ProxyTotal++
			}
		}
		// 显示哪些分组由用户勾：勾了就照勾的顺序来，第一个兼作主分组；
		// 没勾才退回上面那套按名字猜的兜底逻辑。
		cl.Show = c.pickGroups(cl.Groups)
		if len(cl.Show) > 0 {
			cl.Primary = cl.Show[0]
		} else {
			for _, g := range cl.Groups {
				for i, pref := range groupPref {
					score := len(groupPref) - i
					if strings.EqualFold(g.Name, pref) {
						score += 100
					} else if strings.Contains(strings.ToUpper(g.Name), strings.ToUpper(pref)) {
						score += 10
					} else {
						continue
					}
					if score > best {
						best, cl.Primary = score, g
					}
				}
			}
			if cl.Primary.Name == "" && len(cl.Groups) > 0 {
				cl.Primary = cl.Groups[0]
			}
		}
		if cl.Primary.Name != "" {
			if p, ok := px.Proxies[cl.Primary.Name]; ok && len(p.History) > 0 {
				cl.Latency = p.History[len(p.History)-1].Delay
			}
			if p, ok := px.Proxies[cl.Primary.Now]; ok && len(p.History) > 0 {
				cl.Latency = p.History[len(p.History)-1].Delay
			}
		}
	}

	var cn struct {
		DownloadTotal float64 `json:"downloadTotal"`
		UploadTotal   float64 `json:"uploadTotal"`
		Connections   []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	if getJSON(ctx, base+"/connections", hdr, &cn) == nil {
		cl.DownTotal, cl.UpTotal = cn.DownloadTotal, cn.UploadTotal
		cl.ConnCount = len(cn.Connections)
	}

	if !c.cfg.NoEgress {
		c.probeEgress(cl)
	}
}

// api.ip.sb/geoip 实测返回 ip / country_code；早期文档里的 query / countryCode 一并兼容
type ipSbGeo struct {
	Query        string `json:"query"`
	IP           string `json:"ip"`
	Country      string `json:"country"`
	CountryCode  string `json:"country_code"`
	CountryCode2 string `json:"countryCode"`
	City         string `json:"city"`
	Region       string `json:"region"`
	Isp          string `json:"isp"`
	Organization string `json:"organization"`
}

func (g ipSbGeo) addr() string {
	return defStr(g.IP, g.Query)
}

func (g ipSbGeo) cc() string {
	return defStr(g.CountryCode, g.CountryCode2)
}

func (g ipSbGeo) isp() string {
	return defStr(g.Isp, g.Organization)
}

// isMainland 只认中国大陆：country_code CN，或国家名里带"中国"。
// HK / MO / TW 的 code 不是 CN，按他说的"回到中国大陆境内"不触发提醒。
func isMainland(cc, country string) bool {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if cc == "CN" {
		return true
	}
	return strings.Contains(country, "中国") && !strings.Contains(country, "香港") &&
		!strings.Contains(country, "澳门") && !strings.Contains(country, "台湾")
}

// 出口 IP：内核在跑时，本机自己发出去的包就会被 OpenClash 透明接管，所以"不带代理"
// 这一发拿到的就是真实出口；代理口只有在配置了鉴权时才通得上，留作兜底。
func (c *Collector) probeEgress(cl *Clash) {
	gap := 5 * time.Minute
	if !c.egress.CNSince.IsZero() {
		gap = egressEveryCN
	}
	if !c.egress.At.IsZero() && time.Since(c.egress.At) < gap {
		*cl = applyEgress(*cl, c.egress)
		return
	}
	direct := "直连"
	if cl.Running {
		direct = "经 OpenClash 转发"
	}
	var tries []attempt
	seen := map[string]bool{}
	addTry := func(via, proxy string, needUp bool) {
		if proxy != "" && seen[proxy] {
			return
		}
		seen[proxy] = true
		tries = append(tries, attempt{via, proxy, needUp})
	}
	if c.cfg.ProxyURL != "" {
		host, port := withPort(strings.TrimPrefix(c.cfg.ProxyURL, "http://"), "127.0.0.1")
		addTry("经代理 :"+port, proxyURL(host, port, c.proxyAuth), true)
	}
	addTry(direct, "", true)
	for _, p := range c.proxyPort { // 前面都不通才挨个试内核监听的端口
		if p == "" {
			continue
		}
		host, port := withPort(p, "127.0.0.1")
		addTry("经代理 :"+port, proxyURL(host, port, c.proxyAuth), cl.Running)
	}
	start := time.Now()
	var lastErr string
	var pxFail []string
	pxNote := func(t attempt, why string) {
		if t.proxy == "" || len(pxFail) >= 2 {
			return
		}
		pxFail = append(pxFail, strings.TrimPrefix(t.via, "经代理 ")+" "+why)
	}
	for _, t := range tries {
		if time.Since(start) > 12*time.Second {
			lastErr = "探测超时"
			break // 挂死的端口一个就够拖半分钟，别把整轮卡住
		}
		if t.needUp && !c.wanUp {
			lastErr = "外网未连接"
			continue
		}
		tr := &http.Transport{}
		defer tr.CloseIdleConnections()
		if t.proxy != "" {
			u, err := url.Parse(t.proxy)
			if err != nil {
				lastErr = "代理地址无效"
				pxNote(t, "地址无效")
				continue
			}
			tr.Proxy = http.ProxyURL(u)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ip.sb/geoip", nil)
		res, err := (&http.Client{Transport: tr, Timeout: 6 * time.Second}).Do(req)
		if err != nil {
			lastErr = t.via + " 无响应"
			pxNote(t, "无响应")
			cancel()
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		cancel()
		var g ipSbGeo
		if json.Unmarshal(b, &g) != nil || g.addr() == "" {
			lastErr = fmt.Sprintf("%s 响应异常(HTTP %d)", t.via, res.StatusCode)
			pxNote(t, fmt.Sprintf("HTTP %d", res.StatusCode))
			continue
		}
		c.egress = egressCache{IP: g.addr(), CC: g.cc(), City: defStr(g.City, g.Region),
			ISP: g.isp(), Via: t.via, At: time.Now()}
		c.egress.Err = ""
		// 暴露计时：探到国内就记住起点，回到境外立刻清零
		if isMainland(g.cc(), g.Country) {
			if c.egress.CNSince.IsZero() {
				c.egress.CNSince = time.Now()
			}
		} else {
			c.egress.CNSince = time.Time{}
		}
		if p := lastPort(t.proxy); p != "" {
			c.proxyOK = p
		} else if len(pxFail) > 0 && cl.Running {
			// 代理口没打通、靠本机发出去的：把原因留在页脚，否则只看到"转发"，猜不出为什么
			c.note("代理口未通（%s），出口按本机路由探测", strings.Join(pxFail, "、"))
		}
		break
	}
	if lastErr != "" && c.egress.IP == "" {
		c.egress.Err = lastErr
	}
	*cl = applyEgress(*cl, c.egress)
}

type attempt struct {
	via    string
	proxy  string
	needUp bool
}

// mihomo 的代理口配了 authentication 就必须带 Basic 认证，否则 CONNECT 直接 407
func proxyURL(host, port string, auth []string) string {
	base := "http://" + host + ":" + port
	if len(auth) == 0 {
		return base
	}
	if h := strings.SplitN(auth[0], ":", 2); len(h) == 2 {
		return "http://" + url.UserPassword(h[0], h[1]).String() + "@" + host + ":" + port
	}
	return base
}

// ClashDo 朝 mihomo 控制口发一次请求（method/path/body），给页面切换节点用。
// 密钥用采集时已经试通的那把，调用方不需要知道它。
func (c *Collector) ClashDo(method, path string, body []byte) (int, []byte, error) {
	if c.clashBase == "" {
		return 0, nil, fmt.Errorf("还没发现内核控制口")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.clashBase+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if c.clashSecret != "" {
		req.Header.Set("Authorization", "Bearer "+c.clashSecret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := (&http.Client{Timeout: 4500 * time.Millisecond}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, nil
}

// ClashNodes 取某个分组可选的节点和它们的测速结果
func (c *Collector) ClashNodes(group string) ([]NodeInfo, error) {
	scode, b, err := c.ClashDo("GET", "/proxies", nil)
	if err != nil || scode != 200 {
		return nil, fmt.Errorf("控制口返回 %d (%v)", scode, err)
	}
	var px struct {
		Proxies map[string]struct {
			Now     string   `json:"now"`
			All     []string `json:"all"`
			Type    string   `json:"type"`
			History []struct {
				Time  time.Time `json:"time"`
				Delay float64   `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	if json.Unmarshal(b, &px) != nil {
		return nil, fmt.Errorf("解析节点列表失败")
	}
	p, ok := px.Proxies[group]
	if !ok {
		return nil, fmt.Errorf("没有这个分组")
	}
	if p.Type != "Selector" {
		return nil, fmt.Errorf("这个分组不是手动选择型")
	}
	out := make([]NodeInfo, 0, len(p.All))
	for _, n := range p.All {
		ni := NodeInfo{Name: n, Current: n == p.Now}
		if q, ok2 := px.Proxies[n]; ok2 && len(q.History) > 0 {
			ni.Delay = q.History[len(q.History)-1].Delay
		}
		out = append(out, ni)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Current != out[j].Current {
			return out[i].Current
		}
		if out[i].Delay > 0 && out[j].Delay > 0 {
			return out[i].Delay < out[j].Delay
		}
		return out[i].Delay > out[j].Delay
	})
	return out, nil
}

// Last 返回最近采集好的一帧（可能为 nil）；只读，不触发采集
func (c *Collector) Last() *Snap {
	c.lastMu.Lock()
	defer c.lastMu.Unlock()
	return c.lastSnap
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// 端口可能已经带主机，统一拆成 (host, port)
func withPort(v, defHost string) (string, string) {
	v = strings.Trim(strings.TrimSpace(v), `"'`)
	if v == "" {
		return defHost, ""
	}
	if i := strings.LastIndex(v, ":"); i >= 0 {
		if h := v[:i]; h != "" {
			return h, v[i+1:]
		}
		return defHost, v[i+1:]
	}
	return defHost, v
}

func lastPort(u string) string {
	if u == "" {
		return ""
	}
	_, p := withPort(strings.TrimPrefix(u, "http://"), "")
	return p
}

func applyEgress(cl Clash, e egressCache) Clash {
	cl.EgressIP, cl.EgressCC, cl.EgressCity = e.IP, e.CC, e.City
	cl.EgressISP, cl.EgressVia = e.ISP, e.Via
	if !e.At.IsZero() {
		cl.EgressAge = time.Since(e.At).Seconds()
	}
	cl.EgressErr = e.Err
	if !e.CNSince.IsZero() {
		cl.EgressCN = true
		cl.EgressExposure = time.Since(e.CNSince).Seconds()
	}
	return cl
}

// ---------------- fnOS ----------------

func (c *Collector) fnos(s *Snap) {
	f := &s.Fnos
	f.Host = strings.TrimSpace(c.cfg.FnHost)
	if pid := c.qemuPID(); pid > 0 {
		if p := strings.Fields(readFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))); len(p) > 14 {
			cur := [2]uint64{uint64(atof(p[13])), uint64(atof(p[14]))}
			dt := time.Since(c.lastT).Seconds()
			if c.qemuPid == pid && dt > 0.5 && cur[0]+cur[1] >= c.qemuPrev[0]+c.qemuPrev[1] {
				f.QemuCores = float64(cur[0]+cur[1]-c.qemuPrev[0]-c.qemuPrev[1]) / 100 / dt
			}
			c.qemuPrev, c.qemuPid = cur, pid
		}
		f.VMRunning = true
		// 分配量宿主侧 cmdline 里就有，客户机 SSH 不通时内存卡也不该空着
		if cl, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")); err == nil {
			args := strings.Split(string(cl), "\x00")
			for i, a := range args {
				if a == "-m" && i+1 < len(args) {
					f.AllocBytes = parseMem(args[i+1])
					break
				}
			}
		}
	}
	if out, err := monCmd(c.cfg.FnMon, "info status"); err == nil {
		f.VMRunning = strings.Contains(out, "running")
	}
	if o, err := c.sshFnos(); err != nil {
		f.SSHO, f.SSHErr = false, shortErr(err)
	} else {
		f.SSHO = true
		f.SSHErr = ""
		for _, line := range strings.Split(strings.TrimSpace(o), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "memtotal":
				f.MemTotal = atof(v)
			case "memavail":
				f.MemAvail = atof(v)
			case "load1":
				f.Load1 = atof(v)
			case "cpu": // user nice system idle iowait irq softirq steal
				n := strings.Fields(v)
				if len(n) >= 8 {
					// busy 里算上 iowait 之外的全部；idle 那一列单独留给分母
					busy := atof(n[0]) + atof(n[1]) + atof(n[2]) + atof(n[5]) + atof(n[6]) + atof(n[7])
					tot := busy + atof(n[3]) + atof(n[4])
					if c.fnCPUPrev[1] > 0 && tot > c.fnCPUPrev[1] {
						f.CpuPct = (busy - c.fnCPUPrev[0]) / (tot - c.fnCPUPrev[1]) * 100
					}
					c.fnCPUPrev = [2]float64{busy, tot}
				}
			case "dsk": // 读字节 写字节（累计）
				n := strings.Fields(v)
				if len(n) == 2 {
					r1, w1 := atof(n[0]), atof(n[1])
					dt := time.Since(c.fnDiskT).Seconds()
					// 计数器只增不减；客户机重启会归零，那就跳过这一轮，别画出负数
					if !c.fnDiskT.IsZero() && dt > 0.5 && r1 >= c.fnDisk[0] && w1 >= c.fnDisk[1] {
						f.DiskR = (r1 - c.fnDisk[0]) / dt
						f.DiskW = (w1 - c.fnDisk[1]) / dt
					}
					c.fnDisk = [2]float64{r1, w1}
					c.fnDiskT = time.Now()
				}
			case "upt":
				f.Uptime = atof(v)
			case "nv": // name|driver|util|memUsed|memTotal|temp
				v = strings.TrimSpace(v)
				switch {
				case v == "" || v == "NONE":
					f.GPUOk, f.GPUErr = false, "没有 nvidia-smi"
				case v == "ERR":
					f.GPUOk, f.GPUErr = false, "nvidia-smi 无响应"
				default:
					ps := strings.Split(v, "|")
					if len(ps) >= 6 {
						f.GPUName = strings.TrimSpace(ps[0])
						f.GPUDriver = strings.TrimSpace(ps[1])
						f.GPUUtil = atof(ps[2])
						f.GPUMem = atof(ps[3])
						f.GPUMemTot = atof(ps[4])
						f.GPUTemp = atof(ps[5])
						f.GPUOk, f.GPUErr = true, ""
					}
				}
			case "md":
				if v != "" {
					f.MDRaw = append(f.MDRaw, v)
				}
			case "vol": // /vol1:used:total:pct
				ps := strings.Split(v, ":")
				if len(ps) == 4 {
					f.Vols = append(f.Vols, Vol{Name: ps[0], Used: atof(ps[1]),
						Total: atof(ps[2]), Pct: atof(ps[3]), Available: true})
				}
			}
		}
	}
	code := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if res, err := (&http.Client{Timeout: 2200 * time.Millisecond}).Get(
		"http://" + c.cfg.FnHost + ":5666/"); err == nil {
		code = res.StatusCode
		res.Body.Close()
	} else {
		_ = ctx
	}
	f.Web = code
}

func (c *Collector) qemuPID() int {
	ds, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range ds {
		cl := readFile(filepath.Join(d, "cmdline"))
		if strings.Contains(cl, "qemu-system") && strings.Contains(cl, "fnos") {
			n, _ := strconv.Atoi(filepath.Base(d))
			return n
		}
	}
	return 0
}

// qemu 的 HMP 是 readline 模式：先原样回显命令（带一堆 ANSI），再另发结果。
// 只 Read 一次会拿到回显就以为没跑起来 —— 实测踩过，必须读到超时为止再判。
func monCmd(sock, cmd string) (string, error) {
	conn, err := net.DialTimeout("unix", sock, 1200*time.Millisecond)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(1800 * time.Millisecond)
	conn.SetDeadline(deadline)
	io.WriteString(conn, cmd+"\n")
	var acc []byte
	b := make([]byte, 4096)
	for {
		n, err := conn.Read(b)
		if n > 0 {
			acc = append(acc, b[:n]...)
			if time.Now().After(deadline.Add(-1200*time.Millisecond)) &&
				(strings.Contains(string(acc), "VM status:") || strings.Contains(string(acc), "error")) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return string(acc), nil
}

const fnosScript = `
awk '/^MemTotal/{printf "memtotal=%.0f\n",$2*1024} /^MemAvailable/{printf "memavail=%.0f\n",$2*1024}' /proc/meminfo
cut -d" " -f1 /proc/loadavg | sed "s/^/load1=/"
# CPU 和磁盘只取"累计量"，利用率和速率由面板侧两轮相减得到。在机器上 sleep
# 再采第二次会把整轮采集拖慢，而面板本来就 2 秒一跳，差分的结果一样。
# 用 %.0f 不用 %d：跑上一个月，jiffies 和扇区数都会超出 mawk 的整数范围。
awk '/^cpu /{printf "cpu=%.0f %.0f %.0f %.0f %.0f %.0f %.0f %.0f\n",$2,$3,$4,$5,$6,$7,$8,$9}' /proc/stat
# 只算整盘（sda / nvme0n1 / vda），分区和 loop 会把同一份读写重复计一遍
awk '($3 ~ /^sd[a-z]+$/ || $3 ~ /^nvme[0-9]+n[0-9]+$/ || $3 ~ /^vd[a-z]+$/) {r+=$6; w+=$10} END{printf "dsk=%.0f %.0f\n", r*512, w*512}' /proc/diskstats
cut -d" " -f1 /proc/uptime | sed "s/^/upt=/"
for v in /vol1 /vol2 /vol3 /vol4 /vol5; do
  [ -d "$v" ] || continue
  eval "$(df -P -B1 "$v" 2>/dev/null | awk "NR==2{printf \"tot=%s use=%s\n\", \$2, \$3}")"
  [ -n "$tot" ] && [ "$tot" != 0 ] && printf "vol=%s:%s:%s:%s\n" "$v" "$use" "$tot" "$(( use*100/tot ))"
done
awk "/^md/{printf \"md=%s\n\", \$0}" /proc/mdstat
# P620 是 Quadro（NVIDIA GP107GL），驱动是闭源 nvidia.ko，只能问 nvidia-smi；
# gpu_busy_percent 那是 AMD 的 sysfs，以前拿它探，探不到就误报"驱动未装"。
if command -v nvidia-smi >/dev/null 2>&1; then
  n=$(timeout 3 nvidia-smi --query-gpu=name,driver_version,utilization.gpu,memory.used,memory.total,temperature.gpu --format=csv,noheader,nounits 2>/dev/null | head -1)
  if [ -n "$n" ]; then printf "nv=%s\n" "$(echo "$n" | tr "," "|")"; else echo "nv=ERR"; fi
else
  echo "nv=NONE"
fi
`

// parseMem 认 qemu 的 16384 / 16G / 512M 三种写法
func parseMem(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mul := 1024.0 * 1024 // 裸数字是 MB
	switch s[len(s)-1] {
	case 'k', 'K':
		mul, s = 1024.0, s[:len(s)-1]
	case 'm', 'M':
		mul, s = 1024.0*1024, s[:len(s)-1]
	case 'g', 'G':
		mul, s = 1024.0*1024*1024, s[:len(s)-1]
	}
	return atof(s) * mul
}

func (c *Collector) sshFnos() (string, error) { return c.sshFnosRun(fnosScript) }

func (c *Collector) sshFnosRun(script string) (string, error) {
	// 地址或用户没填就当这个功能没开。少了这道判断，ssh.Dial 会拿到 ":22"，
	// 那连的是路由器自己的 sshd——既不是用户要看的东西，也是一次没人要求的登录。
	if strings.TrimSpace(c.cfg.FnHost) == "" || strings.TrimSpace(c.cfg.FnUser) == "" {
		return "", fmt.Errorf("未配置 fnOS 地址或用户")
	}
	key, err := os.ReadFile(c.cfg.FnKey)
	if err != nil {
		return "", fmt.Errorf("缺 SSH 密钥")
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("密钥不可用")
	}
	cfg := &ssh.ClientConfig{
		User: c.cfg.FnUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// 同机室内网设备；主机密钥在部署时由 root 直接写进 known_hosts，此处只做连通
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
		Config: ssh.Config{
			Ciphers: []string{"aes128-ctr", "aes192-ctr", "aes256-ctr", "aes128-gcm@openssh.com",
				"aes256-gcm@openssh.com", "chacha20-poly1305@openssh.com"},
		},
	}
	cli, err := ssh.Dial("tcp", c.cfg.FnHost+":22", cfg)
	if err != nil {
		return "", err
	}
	defer cli.Close()
	sess, err := cli.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	done := make(chan struct{})
	var out []byte
	var serr error
	go func() { out, serr = sess.CombinedOutput(script); close(done) }()
	select {
	case <-done:
		return string(out), serr
	case <-time.After(4500 * time.Millisecond):
		cli.Close()
		return "", fmt.Errorf("SSH 取数超时")
	}
}

func splitRepeat(line string) (string, int, bool) {
	i := strings.LastIndex(line, "  ×")
	if i < 0 {
		return line, 0, false
	}
	n, err := strconv.Atoi(line[i+3:])
	if err != nil {
		return line, 0, false
	}
	return line[:i], n, true
}

func shortErr(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, "ssh: ", "")
	if len([]rune(s)) > 28 {
		return string([]rune(s)[:28]) + "…"
	}
	return s
}

// ---------------- 融合日志栏 ----------------

// OpenClash 的日志：内核那份 /tmp/openclash.log 每连接一行，直接搬会把整块日志区吃掉，
// 所以只留"非 info"和"不是 [TCP]/[UDP] 的 info"（启动、加载规则、geodata 这类）；
// 脚本自己的 /tmp/openclash_start.log 平时是空的，正好在开关插件那一刻有内容。
// stripTimeHead 去掉正文自己带的时间头：融合栏外面已经统一打了一次时刻，
// 再留一遍就是 "20:01:43 [OpenClash] 2026-11-24 20:01:43 [Tip] ..."
func stripTimeHead(t string) string {
	if len(t) >= 19 && t[4] == '-' && t[7] == '-' && t[10] == ' ' && t[13] == ':' && t[16] == ':' {
		return strings.TrimLeft(t[19:], " ")
	}
	if len(t) >= 11 && t[0] == '[' && t[9] == ']' && t[10] == ' ' {
		return t[11:]
	}
	return t
}

func (c *Collector) clashLogFeed() []logRec {
	if !c.clLogT.IsZero() && time.Since(c.clLogT) < 10*time.Second {
		return c.clLog
	}
	c.clLogT = time.Now()
	out, _ := runTO(1500*time.Millisecond, "sh", "-c",
		"tail -n 4000 /tmp/openclash.log 2>/dev/null | grep -vE 'msg=\"\\[(TCP|UDP|DNS|ICMP)\\] ' | tail -n 8; echo '@@start'; tail -n 12 /tmp/openclash_start.log 2>/dev/null")
	var rows []logRec
	sect := "core"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "@@start" {
			sect = "script"
			continue
		}
		if sect == "script" {
			// start.log 里两种行都有：内核转写的带 time= 行，和脚本自己打的无戳行；
			// 后者只能拿抓取时刻兜底
			ts, ok := mihomoTime(line)
			if !ok {
				if t2, ok2 := scriptTime(line); ok2 {
					ts, ok = t2, true
				}
			}
			if !ok {
				ts = c.clLogT
			}
			rows = append(rows, logRec{At: ts, Src: "OpenClash",
				Text: shorten(stripTimeHead(stripMihomo(line)), 110)})
			continue
		}
		lvl, msg, ok := parseMihomo(line)
		if !ok {
			continue
		}
		if lvl == "info" && (strings.Contains(msg, "[TCP] ") || strings.Contains(msg, "[UDP] ") ||
			strings.Contains(msg, "[DNS] ") || strings.Contains(msg, "[ICMP] ")) {
			continue
		}
		ts, ok := mihomoTime(line)
		if !ok {
			ts = c.clLogT
		}
		rows = append(rows, logRec{At: ts, Src: "OpenClash", Text: shorten(msg, 110)})
	}
	if len(rows) > 6 {
		rows = rows[len(rows)-6:]
	}
	c.clLog = rows
	return rows
}

// fnosLogScript 只挑世界可读、且真正说明"发生了什么"的行：
// 装包历史 + 快照/媒体任务里去掉 30 秒一次的心跳
const fnosLogScript = `tail -n 500 /var/log/dpkg.log 2>/dev/null | grep -E " (install|upgrade|remove|status installed) " | tail -n 3
for f in /var/log/apps/trim.snapshots.log /var/log/apps/trim.media.log /var/log/apps/trim.photos.log; do
  [ -f "$f" ] && tail -n 60 "$f" 2>/dev/null | grep -v "status_daemon" | tail -n 2
done
true`

func (c *Collector) fnosLogFeed() []logRec {
	if !c.fnosLogT.IsZero() && time.Since(c.fnosLogT) < 30*time.Second {
		return c.fnosLog
	}
	c.fnosLogT = time.Now()
	out, err := c.sshFnosRun(fnosLogScript)
	if err != nil {
		// 取不到就沿用上一次的结果，别把日志栏整块清空
		return c.fnosLog
	}
	var rows []logRec
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\t", " "))
		if len(line) < 20 {
			continue
		}
		ts, e := time.Parse("2006-01-02 15:04:05", line[:19])
		if e != nil {
			continue
		}
		rows = append(rows, logRec{At: ts, Src: "fnOS", Text: shorten(line[20:], 110)})
	}
	if len(rows) > 4 {
		rows = rows[len(rows)-4:]
	}
	c.fnosLog = rows
	return rows
}

// scriptTime 认 OpenClash 脚本自己那套 "2026-11-24 19:51:00 ..." 或 "[19:51:00] ..."
func scriptTime(line string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02 15:04:05", line[:19]); err == nil && len(line) >= 19 {
		return t, true
	}
	if t, err := time.Parse("15:04:05", line[:8]); err == nil && len(line) >= 8 {
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location()), true
	}
	return time.Time{}, false
}

// stripMihomo 把 "time=\"...\" level=info msg=\"...\"" 收成纯正文，
// 脚本段走的是这条路径，不能把整行原始字段甩到日志栏里
func stripMihomo(line string) string {
	if _, msg, ok := parseMihomo(line); ok {
		return msg
	}
	return line
}

func parseMihomo(line string) (lvl, msg string, ok bool) {
	i := strings.Index(line, "level=")
	j := strings.Index(line, "msg=\"")
	if i < 0 || j < 0 {
		return "", "", false
	}
	lvl = line[i+6:]
	if k := strings.IndexAny(lvl, " \""); k > 0 {
		lvl = lvl[:k]
	}
	msg = strings.TrimSuffix(line[j+5:], "\"")
	return lvl, msg, true
}

// mihomoTime 从 "time=\"2026-11-24T19:51:00.905+08:00\" level=..." 里取时刻。
// 解析不出来时返回 false，让调用方决定兜底值——start.log 里既有内核转写的带戳行，
// 也有脚本自己打的无戳行，全用抓取时间就会把 19:51 的事显示成"刚刚"。
func mihomoTime(line string) (time.Time, bool) {
	i := strings.Index(line, "time=\"")
	if i < 0 {
		return time.Time{}, false
	}
	// time=" 是 6 个字符，切 7 位会把年份的第一位吃掉，两个格式就都解析不动了
	v := line[i+6:]
	if k := strings.IndexByte(v, '"'); k > 0 {
		v = v[:k]
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02T15:04:05", v[:minInt(len(v), 19)]); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------- 磁盘（statfs，不依赖任何外部接口）----------------

func (c *Collector) disks(s *Snap) {
	seen := map[string]bool{}
	add := func(mount, name string, root, vm bool) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			return
		}
		bs := float64(st.Bsize)
		total := float64(st.Blocks) * bs
		free := float64(st.Bfree) * bs
		// OpenWrt 的 / 是 overlayfs，statfs 直接透传 /overlay 的数字，
		// 按 (总量,剩余) 去重比按名字判断可靠
		key := fmt.Sprintf("%.0f/%.0f", total, free)
		if total == 0 || seen[key] {
			return
		}
		seen[key] = true
		s.Disks = append(s.Disks, Disk{Name: name, Mount: mount, Size: total,
			Used: total - free, Pct: (total - free) / total * 100, IsRoot: root, IsVMStore: vm})
	}
	add("/", "根分区", true, false)
	add("/overlay", "/overlay", false, false)
	add("/mnt/nvme0n1-4", "VM 存储区", false, true)
	add("/boot", "/boot", false, false)
}

// ---------------- Docker / 日志 ----------------

func (c *Collector) docker(s *Snap) {
	body, err := unixHTTP("/var/run/docker.sock", "GET", "/containers/json?all=1", 2*time.Second)
	if err != nil {
		return
	}
	var cs []struct {
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(body), &cs) != nil {
		return
	}
	s.DockerAll = len(cs)
	for _, x := range cs {
		if x.State == "running" {
			s.DockerRun++
		}
	}
}

var logNoise = []string{"dropbear[", "Pubkey auth succeeded", "Child connection from",
	"Exit (", "routerpanel", "dnsmasq-dhcp", "udhcpd", "netdata[",
	// 面板每次摘挂 fbcon，内核就刷一行 Console: switching，属于自feedback
	"Console: switching", "fbsysrq",
	// miniupnpd 每隔几分钟把同一条 redirect 规则删了又加，正文一模一样，占栏不给出信息
	"delete_redirect_and_filter_rules", "is not suitable for",
	// ttyd（iStore 的网页终端）每次有人开终端页就报一行网关探测结果，和面板无关
	"lws_system_cpd_set",
	// OpenClash 每半分钟对一次表，"Sync system time success" / "clock is behind NTP by 175ms"
	// 会整屏霸占融合栏，把真正的事件挤掉——它是心跳，不是事件
	"Sync system time success", "System clock is behind NTP"}

// noisy 是四路融合共用的过滤器：以前只作用在 iStore 那一路，
// 结果 OpenClash 的时钟心跳把整块地方占满。噪声就是噪声，不分来源
func noisy(text string) bool {
	for _, x := range logNoise {
		if strings.Contains(text, x) {
			return true
		}
	}
	return false
}

// logread 的行是 "Mon Nov 23 07:50:59 2026 daemon.info netdata[24432]: 正文"。
// 屏幕上只留 时:分:秒 + 进程 + 正文，进程去掉 [pid]，这样同一条重复消息能合并计数。
func logLine(line string) string {
	f := strings.SplitN(line, " ", 6)
	if len(f) < 6 || !isStampDay(f[0]) || len(f[4]) != 4 || !allDigits(f[4]) {
		return line
	}
	stamp, rest := f[3], f[5]
	if g := strings.SplitN(rest, " ", 2); len(g) == 2 && !strings.Contains(g[0], ":") &&
		strings.Contains(g[0], ".") {
		rest = g[1] // facility.level
	}
	proc := ""
	if h := strings.SplitN(rest, " ", 2); len(h) == 2 && strings.HasSuffix(h[0], ":") {
		proc = strings.TrimSuffix(h[0], ":")
		if i := strings.Index(proc, "["); i > 0 {
			proc = proc[:i]
		}
		proc += ": "
		rest = h[1]
	}
	return stamp + "  " + proc + rest
}

func isStampDay(s string) bool {
	switch s {
	case "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun":
		return true
	}
	return false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func (c *Collector) logs(s *Snap) {
	var rows []logRec

	// 1) iStoreOS 本机 syslog
	if out, _ := runTO(1500*time.Millisecond, "logread", "-l", "150"); out != "" {
		n := 0
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			n++
			if noisy(line) {
				continue
			}
			l := logLine(line)
			// logLine 已经把 "HH:MM:SS  进程: 正文" 排在前头，融合栏外面还要再打一次时刻，
			// 不剥掉就是 "19:46:39 [iStore] 19:46:39 miniupnpd: ..."
			if len(l) > 10 && l[2] == ':' && l[5] == ':' && l[8] == ' ' {
				l = strings.TrimLeft(l[9:], " ")
			}
			rows = append(rows, logRec{At: syslogTime(line), Src: "iStore", Text: shorten(l, 130)})
		}
		if len(rows) == 0 {
			c.note("最近 %d 条 syslog 都是登录/监控噪声，已过滤", n)
		}
	}

	// 2) OpenClash 插件日志 3) fnOS 活动日志
	rows = append(rows, c.clashLogFeed()...)
	rows = append(rows, c.fnosLogFeed()...)

	// 4) 面板操作 + 设备侧事件：谁在什么时候点了什么、哪台设备刚出过状况
	c.opMu.Lock()
	for _, o := range c.ops {
		src := o.Src
		if src == "" {
			src = "面板"
		}
		rows = append(rows, logRec{At: o.At, Src: src, Text: o.Text})
	}
	c.opMu.Unlock()

	// 噪声过滤对四路一起生效：融合栏最怕一路心跳把整块地方占满
	clean := rows[:0]
	for _, r := range rows {
		if !noisy(r.Text) {
			clean = append(clean, r)
		}
	}
	rows = clean

	// 每一路内部按时刻排好，各留最近几条：融合栏最怕的就是"最啰嗦的那一路独占整块地方"
	bySrc := map[string][]logRec{}
	for _, r := range rows {
		bySrc[r.Src] = append(bySrc[r.Src], r)
	}
	quota := map[string]int{"iStore": 3, "OpenClash": 3, "fnOS": 2, "面板": 5, "iPad": 2}
	rows = rows[:0]
	for src, list := range bySrc {
		sort.SliceStable(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
		n, ok := quota[src]
		if !ok {
			n = 2 // 新加的一路忘了登记也不能整条被丢掉（配额查不到时 n=0 等于全删）
		}
		if len(list) > n {
			list = list[len(list)-n:]
		}
		rows = append(rows, list...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })

	// 相邻同源同文的合并计数（odhcpcd 那种一条反复刷屏的）
	type outRow struct {
		at        time.Time
		src, text string
		cnt       int
	}
	var merged []outRow
	for _, r := range rows {
		n := len(merged)
		if n > 0 && merged[n-1].src == r.Src && merged[n-1].text == r.Text {
			merged[n-1].cnt++
			merged[n-1].at = r.At
			continue
		}
		merged = append(merged, outRow{at: r.At, src: r.Src, text: r.Text})
	}
	if len(merged) > 8 {
		merged = merged[len(merged)-8:]
	}
	out := make([]string, 0, len(merged))
	for _, m := range merged {
		line := fmt.Sprintf("%s  [%s] %s", m.at.Format("15:04:05"), m.src, m.text)
		if m.cnt > 1 {
			line += fmt.Sprintf("  ×%d", m.cnt)
		}
		out = append(out, line)
	}
	s.Logs = out
}

// syslogTime 把 "Mon Nov 24 19:47:44 2026" 这种行首还原成可排序的时刻；
// 解析不出来就用抓取时刻，宁可排在末尾也不要整栏乱掉
func syslogTime(line string) time.Time {
	f := strings.Fields(line)
	if len(f) < 5 {
		return time.Now()
	}
	t, err := time.Parse("2006 Jan 2 15:04:05", fmt.Sprintf("%d %s %s %s",
		time.Now().Year(), f[1], f[2], f[3]))
	if err != nil {
		return time.Now()
	}
	return t
}

// ---------------- 汇总 ----------------

func (c *Collector) Collect() *Snap {
	s := &Snap{Time: time.Now(), Title: strings.TrimSpace(c.cfg.Title)}
	c.lastT = s.Time
	s.Hostname = readFile("/proc/sys/kernel/hostname")
	if h, err := runTO(1200*time.Millisecond, "ubus", "call", "system", "board"); err == nil || h != "" {
		var b struct {
			Kernel   string `json:"kernel"`
			Model    string `json:"model"`
			System   string `json:"system"`
			Hostname string `json:"hostname"`
		}
		if json.Unmarshal([]byte(h), &b) == nil {
			s.Kernel = b.Kernel
			m := b.Model
			if m == "" {
				m = b.System
			}
			s.Model = m
			if s.Hostname == "" {
				s.Hostname = b.Hostname
			}
		}
	}
	for _, line := range strings.Split(readFile("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			s.OSName = strings.Trim(v, `"`)
		}
	}
	if up := strings.Fields(readFile("/proc/uptime")); len(up) > 0 {
		s.Uptime = atof(up[0])
	}
	c.cpu(s)
	c.temps(s)
	c.mem(s)
	c.net(s)
	c.quickstart(s)
	c.clash(s)
	c.fnos(s)
	c.ipad(s)
	c.ipadNote(&s.IPad)
	c.link(s)
	c.linkNote(&s.Link)
	c.disks(s)
	c.devices(s)
	c.docker(s)
	c.logs(s)
	s.Notes = c.notes
	c.notes = nil
	c.lastMu.Lock()
	c.lastSnap = s
	c.lastMu.Unlock()
	if len(s.CorePct) > 0 {
		s.Primed = true
	}
	return s
}
