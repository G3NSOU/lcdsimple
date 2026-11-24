package main

// routerpanel —— iStoreOS 本机控制台上的状态面板，直接画 /dev/fb0。
//
// 为什么不走 LuCI/浏览器：这台路由器上没有 python、没有字体、没有 X，也没有任何
// 浏览器包；官方 lcdsimple 同样是"自带字体 + 裸写帧缓冲"，这是这块板子上唯一可行路线。
//
// 关键调试开关 -png：把当前这一帧导成 PNG。屏在路由器上、我在电脑上，
// 没有这个回路就只能靠人肉描述排版，改一轮要跑一次现场。

import (
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "0.1.0"

type fbInfo struct {
	W, H, Stride, BPP int
}

func readFB(fbPath string) (*fbInfo, error) {
	base := "/sys/class/graphics"
	// 从设备节点反推 sysfs 目录名（fb0 -> 0）
	idx := strings.TrimPrefix(filepath.Base(fbPath), "fb")
	d := filepath.Join(base, "fb"+idx)
	vs := strings.TrimSpace(readFile(filepath.Join(d, "virtual_size")))
	parts := strings.Split(vs, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%s/virtual_size 读不到或不是预期格式(%q)", d, vs)
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	bpp := int(atof(readFile(filepath.Join(d, "bits_per_pixel"))))
	stride := int(atof(readFile(filepath.Join(d, "stride"))))
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("分辨率异常 %dx%d", w, h)
	}
	if bpp == 0 {
		bpp = 32
	}
	if stride <= 0 {
		stride = w * bpp / 8
	}
	return &fbInfo{W: w, H: h, Stride: stride, BPP: bpp}, nil
}

// fbcon 绑在哪个 vtcon 上不固定，按名字找，别写死 vtcon1
// releaseConsole 把显示器真的交还给系统控制台。
// 光把 fbcon 挂回去不够：上一帧面板还冻在屏幕上，控制台不重绘就看不见，
// 用户在设置里点"关掉屏幕输出"看到的就是"屏幕明明还在输出"。
// 所以这里做三件事：fbcon 回来、帧缓冲刷黑、点亮背光并把光标还给 tty1。
func releaseConsole(fbPath string) {
	if p := fbconPath(); p != "" {
		if ok, msg := setFBCon(p, "1"); ok {
			fmt.Fprintf(os.Stderr, "fbcon 已交还（%s）\n", msg)
		}
	}
	if fb, err := readFB(fbPath); err == nil {
		if f, err := os.OpenFile(fbPath, os.O_WRONLY, 0); err == nil {
			_, _ = f.Write(make([]byte, fb.Stride*fb.H))
			_ = f.Close()
		}
	}
	os.WriteFile("/sys/class/graphics/fb0/blank", []byte("0"), 0)
	os.WriteFile("/dev/tty1", []byte("\033[?25h\033[2J\033[H"), 0)
}

func fbconPath() string {
	ds, _ := filepath.Glob("/sys/class/vtconsole/vtcon*")
	for _, d := range ds {
		if strings.Contains(readFile(filepath.Join(d, "name")), "frame buffer") {
			return filepath.Join(d, "bind")
		}
	}
	return ""
}

func setFBCon(p, val string) (bool, string) {
	if p == "" {
		return false, "找不到 fbcon 节点"
	}
	old := readFile(p)
	if err := os.WriteFile(p, []byte(val), 0); err != nil {
		return false, fmt.Sprintf("写 %s 失败: %v", p, err)
	}
	return true, fmt.Sprintf("%s: %s -> %s", p, strings.TrimSpace(old), val)
}

func main() {
	var (
		fbPath   = flag.String("fb", "/dev/fb0", "帧缓冲设备")
		pngOut   = flag.String("png", "", "只渲染一帧写到这里然后退出（调试回路）")
		mask     = flag.Bool("mask", false, "截图模式：这一帧里的 IP/MAC/主机地址全部打码，版式不变")
		sizeStr  = flag.String("size", "", "PNG 模式下的分辨率，如 1920x1080；留空则读实际设备")
		interval = flag.Int("interval", 3, "刷新间隔秒")
		fontL    = flag.String("font-light", "/etc/routerpanel/SHS-Light.ttf", "细体（Light，文档里的 editorial 字重）")
		fontN    = flag.String("font-normal", "/etc/routerpanel/SHS-Normal.ttf", "常规字体（必须是 glyf 的 TTF）")
		fontB    = flag.String("font-bold", "/etc/routerpanel/SHS-Bold.ttf", "粗体字体")
		pixfmt   = flag.String("pixfmt", "bgra", "帧缓冲字节序：bgra 或 rgba")
		keepCons = flag.Bool("keep-console", false, "不解绑 fbcon（内核日志会继续盖住面板）")
		dumpJSON = flag.Bool("dump-json", false, "只采集一次并打印 JSON，不绘制")
		once     = flag.Bool("once", false, "只画一帧")
		warm     = flag.Int("warm", 0, "-png/-once 下先空跑 N 轮（每轮 1 秒），让折线有历史再出图")
		// fnOS 这三项一律留空当默认：地址、用户、qemu monitor 路径都是各人环境里的东西，
		// 预置成任何一个人的值都会让第二个人一装上就对着错误的机器伸手。
		// 空 = 这块功能不探，卡片上写"未配置"，用户在设置页里填。
		fnHost   = flag.String("fnos-host", "", "fnOS 地址，留空不探")
		fnUser   = flag.String("fnos-user", "", "fnOS SSH 用户，留空不探")
		fnKey    = flag.String("fnos-key", "/etc/routerpanel/id_panel", "fnOS 只读私钥")
		fnMon    = flag.String("fnos-mon", "", "fnOS qemu monitor socket，留空就用宿主侧进程判断")
		proxyURL = flag.String("proxy", "http://127.0.0.1:7890", "测出口 IP 用的代理（clash http 口）")
		noEgress = flag.Bool("no-egress", false, "不探测出口 IP（离线/省流量）")
		pidfile  = flag.String("pidfile", "", "写 pid 到这里")
		mock     = flag.Bool("mock", false, "用一帧假数据渲染（本机验证运行态/告警排版，不碰路由器）")
		httpPort = flag.Int("http", 0, ">0 时在局域网端口上提供只读 JSON/SSE 和 iPad 页面")
		httpBind = flag.String("http-bind", "", "HTTP 监听地址，留空=自动用 br-lan 的地址（不监听 WAN）")
		noFB     = flag.Bool("no-fb", false, "完全不碰帧缓冲，只跑 HTTP（屏幕关了、只留 iPad 时用）")
		control  = flag.Bool("control", false, "开放写接口：页面上可以切节点、切模式（要令牌）")
		token    = flag.String("token", "", "写接口令牌，留空且开了 -control 时自动生成并写进 uci")
		ipadHost = flag.String("ipad-host", "", "iPad 面板机地址，留空不探（iOS 9 的 sshd 只吃老算法）")
		barStyle = flag.String("status-bar", "black", "全屏网页的状态栏：black=不透明黑，translucent=内容顶到状态栏下、由页面涂黑")
		ipadUser = flag.String("ipad-user", "root", "iPad SSH 用户")
		ipadKey  = flag.String("ipad-key", "/etc/routerpanel/id_panel", "连 iPad 用的私钥，默认复用面板那把")
		grpFile  = flag.String("groups-file", "/etc/routerpanel/groups", "存“卡片上显示哪几个分组”的文件，一行一个名字")
		title    = flag.String("title", "Regisseur", "面板名字：屏幕顶栏、网页标题、iPad 桌面图标都用它")
	)
	flag.Parse()

	W, H := 1920, 1080
	var fbi *fbInfo
	if *noFB {
		*fbPath = ""
	} else if *pngOut == "" || *sizeStr == "" {
		if v, err := readFB(*fbPath); err == nil {
			fbi = v
			W, H = v.W, v.H
		} else if *pngOut == "" && *httpPort == 0 {
			fmt.Fprintf(os.Stderr, "读不到帧缓冲: %v\n", err)
			os.Exit(1)
		} else {
			fmt.Fprintf(os.Stderr, "帧缓冲不可用(%v)，只跑 HTTP 模式\n", err)
		}
	}
	if *sizeStr != "" {
		if _, err := fmt.Sscanf(*sizeStr, "%dx%d", &W, &H); err != nil {
			fmt.Fprintf(os.Stderr, "-size 解析失败: %v\n", err)
			os.Exit(1)
		}
	}

	col := NewCollector(Config{FnHost: *fnHost, FnUser: *fnUser, FnKey: *fnKey,
		FnMon: *fnMon, ProxyURL: *proxyURL, NoEgress: *noEgress,
		IPadHost: *ipadHost, IPadUser: *ipadUser, IPadKey: *ipadKey,
		GroupsFile: *grpFile, Title: *title})

	// iPad 那条链路：同一个进程里顺手起一个只读 HTTP/SSE。
	// 帧缓冲画的是客厅那块屏，HTTP 画的是手上的 iPad，两份共用同一份采集结果，
	// 不会出现"屏幕说连上了、页面说没连上"这种自相矛盾。
	var ws *webServer
	if *httpPort > 0 && !*mock {
		ws = startWeb(*httpPort, *httpBind, col, *control, *token, *barStyle)
		if ws != nil {
			// 采集器要能回查"iPad 那个页面还在不在取数"，这是唯一不靠猜的存活证据
			col.sees = ws.lastSeen
			defer ws.stop()
		}
	}

	if *mock {
		// OpenClash 跑起来之后的样子，以及 80%/90% 两档告警色，
		// 现在没有外网没法真跑，所以用假数据把版式先验一遍
		cv := NewCanvas(W, H)
		refreshSeconds = *interval
		if err := cv.loadFonts(*fontL, *fontN, *fontB); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		hist := NewHistory()
		st := mockSnap()
		for i := 0; i < 40; i++ {
			hist.Push(st)
		}
		Draw(cv, st, hist)
		for _, o := range cv.Overlaps() {
			fmt.Fprintf(os.Stderr, "排版重叠: %s\n", o)
		}
		f, err := os.Create(*pngOut)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := png.Encode(f, cv.img); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		f.Close()
		fmt.Println("mock ->", *pngOut)
		return
	}

	if *dumpJSON {
		col.Collect()
		s := col.Collect() // 跑两轮，CPU% 才有差分
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(b))
		return
	}

	cv := NewCanvas(W, H)
	refreshSeconds = *interval
	if !*noFB {
		// 纯 HTTP 模式根本不画屏，字体缺了也不该把 iPad 的页面一起拖死
		if err := cv.loadFonts(*fontL, *fontN, *fontB); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	if *pidfile != "" {
		os.WriteFile(*pidfile, []byte(strconv.Itoa(os.Getpid())), 0644)
	}

	// 帧缓冲写屏准备
	var raw []byte
	var f *os.File
	if *pngOut == "" && fbi != nil && !*noFB {
		raw = make([]byte, fbi.Stride*fbi.H)
		ff, err := os.OpenFile(*fbPath, os.O_WRONLY, 0)
		if err != nil {
			if *httpPort == 0 {
				fmt.Fprintf(os.Stderr, "打不开 %s: %v\n", *fbPath, err)
				os.Exit(1)
			}
			// 屏坏了/没接屏，但 iPad 那边还要数据：丢掉帧缓冲继续跑
			fmt.Fprintf(os.Stderr, "打不开 %s(%v)，只跑 HTTP 模式\n", *fbPath, err)
			raw, fbi = nil, nil
		} else {
			f = ff
			defer f.Close()
		}
	}

	if *noFB && *pngOut == "" {
		// uci screen=0 走过来的：进程只服务 iPad 网页，屏幕一寸都不该占着
		releaseConsole(*fbPath)
	}

	// 把内核控制台从帧缓冲上摘下来，否则 log 一行一行盖在面板上
	vp := fbconPath()
	// PNG 调试模式只是出一张图，绝不能顺手把控制台摘了 —— 那会让屏幕黑一下
	if !*keepCons && vp != "" && *pngOut == "" && f != nil {
		if ok, msg := setFBCon(vp, "0"); ok {
			defer setFBCon(vp, "1")
			fmt.Fprintf(os.Stderr, "fbcon 已让位（%s）\n", msg)
		} else {
			fmt.Fprintf(os.Stderr, "fbcon 摘除失败：%s\n", msg)
		}
		os.WriteFile("/sys/class/graphics/fb0/blank", []byte("0"), 0) // 别让我熄屏
	}

	// 清屏 + 关光标，避免残留一个闪烁的方块
	if f != nil {
		os.WriteFile("/dev/tty1", []byte("\033[?25l\033[2J"), 0)
	}

	col.Collect() // 预热：差分计数器的第一轮基线
	hist := NewHistory()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		// 信号在这里处理，不等的就是"下一轮采集跑完再说"：一轮采集可能要好几秒
		// （SSH 进 NAS、进 iPad 各带超时），procd 等不到就改发 SIGKILL，
		// 那时候清理代码一行都跑不到，屏幕会一直冻在最后一帧上。
		s := <-sig
		fmt.Fprintf(os.Stderr, "收到 %v，退出\n", s)
		if f != nil {
			releaseConsole(*fbPath)
		}
		os.Exit(0)
	}()

	tick := time.NewTicker(time.Duration(*interval) * time.Second)
	defer tick.Stop()
	lastBlank := time.Time{}
	fbBad := 0
	lastFbErr := time.Time{}

	drawOne := func() bool {
		s := col.Collect()
		if *mask {
			// 在进画布和进接口之前打码：屏幕和 iPad 页面用的是同一份快照，
			// 只遮一边会出现"截图里屏上打了码、页面上还挂着真实地址"
			MaskSnap(s)
		}
		ws.push(buildPayload(s))
		hist.Push(s)
		if f == nil && *pngOut == "" {
			return true // 纯 HTTP 模式：不画屏，只喂接口
		}
		Draw(cv, s, hist)
		if *pngOut != "" || *once {
			// 调试回路顺手做排版自检：文字行盒 / 图表带互相压住就报出来，
			// 不用我把图拉回来看第二眼才发现重叠
			for _, o := range cv.Overlaps() {
				fmt.Fprintf(os.Stderr, "排版重叠: %s\n", o)
			}
		}
		if *pngOut != "" {
			tmp := *pngOut + ".tmp"
			fh, err := os.Create(tmp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "写 %s 失败: %v\n", tmp, err)
				return false
			}
			if err := png.Encode(fh, cv.img); err != nil {
				fh.Close()
				fmt.Fprintf(os.Stderr, "png 编码失败: %v\n", err)
				return false
			}
			fh.Close()
			os.Rename(tmp, *pngOut)
			return true
		}
		if f != nil && fbi != nil {
			// DPMS 熄屏是按"控制台有没有动"算的，面板自己不算输入，
			// 隔一分钟顶一次背光，不然挂机一小时屏幕就黑了
			if time.Since(lastBlank) > 60*time.Second {
				os.WriteFile("/sys/class/graphics/fb0/blank", []byte("0"), 0)
				lastBlank = time.Now()
			}
			packPixels(cv, raw, fbi, *pixfmt)
			// 必须 WriteAt：/dev/fb0 是设备不是文件，普通 Write 会把游标
			// 推到 8MB 之外，第二帧开始就 ENOSPC（踩过：第一帧好好的，之后全失败）
			if _, err := f.WriteAt(raw, 0); err != nil {
				fbBad++
				// 刷屏的失败日志会把 syslog 顶掉，半小时说一次就够了
				if fbBad == 1 || time.Since(lastFbErr) > 30*time.Second {
					fmt.Fprintf(os.Stderr, "写帧缓冲失败(连续 %d 次): %v\n", fbBad, err)
					lastFbErr = time.Now()
				}
				// 连续 20 帧写不进去就是设备状态坏了，退出让 procd 重开一次
				if fbBad >= 20 {
					fmt.Fprintln(os.Stderr, "连续写屏失败，退出交给 procd 重启")
					os.Exit(1)
				}
				return false
			}
			fbBad = 0
		}
		return true
	}

	if (*pngOut != "" || *once) && *warm > 0 {
		// 出图前喂历史：不然截图里折线永远是空的，排版好不好根本看不出来
		for i := 0; i < *warm; i++ {
			hist.Push(col.Collect())
			time.Sleep(time.Second)
		}
	}

	drawOne()
	// -png 就是一次性出图，别掉进下面的常驻循环（踩过：进程不退出，
	// 后续 scp 覆盖二进制直接被 ETXTBSY 挡掉，看起来像"卡住"）
	if *once || *pngOut != "" {
		return
	}
	for {
		select {
		case <-tick.C:
			drawOne()
		case <-sig:
			// 正常不会走到这儿：信号由下面那个 goroutine 处理，见那里的说明
			return
		}
	}
}

// image.RGBA 是 R,G,B,A；i915 的 32bpp 帧缓冲通常是小端 ARGB，即内存里 B,G,R,X。
// 万一红蓝反了，-pixfmt rgba 翻一下即可 —— 这也是先做 PNG 回路的好处：
// PNG 永远是按逻辑颜色出的，屏幕上偏色就说明只是字节序问题。
func packPixels(cv *Canvas, out []byte, fb *fbInfo, fmtName string) {
	if fb.BPP != 32 {
		return
	}
	swapped := fmtName == "rgba"
	for y := 0; y < fb.H; y++ {
		rowOff := y * fb.Stride
		for x := 0; x < fb.W; x++ {
			i := (y*cv.img.Stride + x*4)
			r, g, b := cv.img.Pix[i], cv.img.Pix[i+1], cv.img.Pix[i+2]
			o := rowOff + x*4
			if swapped {
				out[o], out[o+1], out[o+2], out[o+3] = r, g, b, 255
			} else {
				out[o], out[o+1], out[o+2], out[o+3] = b, g, r, 255
			}
		}
	}
}
