package main

// iPad 端的"装机配置"：一个 .mobileconfig 描述文件。
//
// 它只干一件能干的事：Web Clip——全屏、无地址栏、不可误删的桌面图标，指向本机面板地址。
//
// 载荷类型必须是 com.apple.webClip.managed（Apple MDM 里的正式名）。
// 之前写成老式的 com.apple.webclip，iOS 9 认不出这个类型，装完就是"没有承载资料"、
// 桌面也不出图标——描述文件本身装上了，但里面是空的，症状和没装一样。
// 另外 WebClip 要求带显示名和图标，图标用 base64：新键 Icon / 老键 TouchIcon 都给
// （180 那份），再加 TouchIcon57x57（120 那份）。iOS 9 只认老键，新系统只认新键，
// 多写一个不认识的键不会让载荷失效，少写就真的没图标。
//
// 别指望它管熄屏：passcode 载荷里的 maxInactivity 是"锁屏后多久要重新输密码"的宽限时间，
// 不是"自动锁定"。iOS 9 上自动锁定没有可靠的描述文件键，只能 设置→亮度→自动锁定→永不。
// 之前这页写着"装了就永不熄屏"，那是假的，已经删掉；引导式访问同理，它只防误触退出。
//
// 状态栏和启动白屏：这条路在 iOS 9 上是死的，别再往设备上写东西。
// 之前在这里写过"改容器里的 Info.plist + 放 Default*.png 就能解决"，那是猜的，
// 后来去设备上把二进制里的字符串挖了一遍，结论正相反：
//   - /Applications/Web.app/Web 里 WebClip 相关代码命中 0 次，承载图标的是 MobileSafari
//     （WebClipMetadataFetcher / WebClipLinkTags / setStartupImage / setStartupLandscapeImage），
//     启动图是它从网页的 link 抓来的，SpringBoard 只在拉起那一刻取用（getStartupImage）；
//     往容器里手放 Default.png 没人读。
//   - 容器 Info.plist 里那个 WebClipStatusBarStyle 键，SpringBoard 命中 0 次；
//     真实存在的键是 DefaultStatusBarStyle / DefaultStatusBarHidden / DefaultPNGName，
//     但它们属于 SpringBoard 自己的 activation/suspension 记录，不是 clip 的 plist 能喂的。
// 所以：想要黑启动 + 黑状态栏 + 有 logo，就用 Safari「添加到主屏幕」，
// 那条路会走 WebClipMetadataFetcher，页面里的 apple-touch-icon /
// apple-touch-startup-image / status-bar-style 全都算数（实测过）。
// 描述文件那份图标只多一个"防误删"，代价是白栏白启动，两个别同时留。
//
// plist 手写而不引第三方库：结构就两层，多一个依赖不如多十二行字符串。

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

//go:embed web/assets/ipad-120.png
var ipadIcon120 []byte

//go:embed web/assets/ipad-180.png
var ipadIcon180 []byte

// 桌面图标的另外两档：Safari「添加到主屏幕」是拿不到描述文件里那份图标的，
// 它只读页面里的 <link rel="apple-touch-icon">，所以这四档必须由网页自己发出去，
// 少一档就是"图标没了"——非视网膜 iPad 要 76，视网膜要 152/180。
//
//go:embed web/assets/ipad-76.png
var ipadIcon76 []byte

//go:embed web/assets/ipad-152.png
var ipadIcon152 []byte

// 启动图（StartupImage）不放在这里：Apple 的 WebClip 载荷只有
// FullScreen / Icon / IsRemovable / Label / Precomposed / URL 六个键，
// 没有 StartupImage；描述文件里塞不认识的键等于没塞（当年 com.apple.webclip
// 拼错一次就是这么栽的）。启动那一瞬的白屏归页面管，
// 用 <link rel="apple-touch-startup-image"> 提供，见 web/index.html 头部
// 和 serve.go 的 /assets/ 路由。
//
//go:embed web/assets/ipad-startup-land.png
var ipadStartupLand []byte

//go:embed web/assets/ipad-startup-port.png
var ipadStartupPort []byte

// 视网膜屏的 iPad 尺寸不一样，缺了那张就是白屏闪一下：给 2x 版一起备着。
//
//go:embed web/assets/ipad-startup-land2x.png
var ipadStartupLand2x []byte

//go:embed web/assets/ipad-startup-port2x.png
var ipadStartupPort2x []byte

// panelURL 面板在局域网里的地址，三层：
//  1. uci 里显式配的 http_url——想固定成别名/域名就用它，优先级最高；
//  2. 装描述文件这台设备实际访问到的 Host。iPad 能连上来，说明这个地址在它的网段里就是通的，
//     照着它写图标地址最不容易错；
//  3. 兜底取本机 br-lan 的地址。
//
// 这里以前写死过作者自己路由器的网关地址：换一家就不是了，
// 装完图标打不开还查不出来——不能拿自己的环境当默认值。
func panelURL(r *http.Request, port int) string {
	if v, _ := runTO(1200*time.Millisecond, "uci", "-q", "get", "routerpanel.main.http_url"); strings.TrimSpace(v) != "" {
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	host := ""
	if r != nil {
		host = strings.TrimSpace(r.Host)
	}
	// 本机回环不算数：那地址 iPad 走不到
	if strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "localhost") || host == "[::1]:80" || host == "[::1]" {
		host = ""
	}
	if host != "" {
		if !strings.Contains(host, ":") && port != 80 { // 没带端口才补，80 是默认口不写也行
			host = fmt.Sprintf("%s:%d", host, port)
		}
		return "http://" + host + "/"
	}
	if ip := lanBind(); ip != "" {
		return fmt.Sprintf("http://%s:%d/", ip, port)
	}
	return ""
}

func xmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// clipName 桌面图标的名字：用面板名字（uci title / -title），没设过就退回路由器主机名。
// 两个都没有才落到"网络面板"这个通用词上——图标不能没有名字，但也不该拿别人的机器名当默认值。
func (ws *webServer) clipName() string {
	if s := ws.col.Last(); s != nil {
		if n := strings.TrimSpace(s.Title); n != "" {
			return n
		}
		if n := strings.TrimSpace(s.Hostname); n != "" {
			return n
		}
	}
	return "网络面板"
}

func buildMobileConfig(r *http.Request, port int, name string) []byte {
	url := panelURL(r, port)
	ic180 := base64.StdEncoding.EncodeToString(ipadIcon180)
	ic120 := base64.StdEncoding.EncodeToString(ipadIcon120)
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>PayloadContent</key>
	<array>
		<dict>
			<key>FullScreen</key><true/>
			<key>IsRemovable</key><false/>
			<key>Precomposed</key><true/>
			<key>Label</key><string>` + xmlEsc(name) + `</string>
			<key>URL</key><string>` + xmlEsc(url) + `</string>
			<key>Icon</key><data>` + ic180 + `</data>
			<key>TouchIcon</key><data>` + ic180 + `</data>
			<key>TouchIcon57x57</key><data>` + ic120 + `</data>
			<key>PayloadType</key><string>com.apple.webClip.managed</string>
			<key>PayloadVersion</key><integer>1</integer>
			<key>PayloadIdentifier</key><string>com.routerpanel.ipad.webclip.managed</string>
			<key>PayloadUUID</key><string>6b0e1f42-3c7d-4f18-9a2e-5d1c07be4a91</string>
			<key>PayloadEnabled</key><true/>
		</dict>
	</array>
	<key>PayloadDisplayName</key><string>` + xmlEsc(name) + ` iPad 面板</string>
	<key>PayloadOrganization</key><string>` + xmlEsc(name) + `</string>
	<key>PayloadDescription</key><string>全屏网页图标（不可误删）。不签名、允许随时删除。熄屏请另外设 自动锁定=永不。</string>
	<key>PayloadRemovalDisallowed</key><false/>
	<key>PayloadType</key><string>Configuration</string>
	<key>PayloadVersion</key><integer>1</integer>
	<key>PayloadIdentifier</key><string>com.routerpanel.ipad.panel</string>
	<key>PayloadUUID</key><string>6b0e1f42-0a5b-4f18-9a2e-1d7c07be4a02</string>
</dict>
</plist>
`)
}

func (ws *webServer) ipadProfile(w http.ResponseWriter, r *http.Request) {
	if panelURL(r, ws.port) == "" { // 地址取不到就别发一个空图标出去，说清楚为什么
		setNoStore(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(`取不到面板的局域网地址：请用 http://<路由器地址>:端口/ipad 打开这一页再装，` +
			`或在 uci 里显式设 routerpanel.main.http_url。` + "\n"))
		return
	}
	setNoStore(w)
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	// 文件名保持 ASCII：iOS 9 对 Content-Disposition 里的中文文件名不保证认，
	// 认不了就是"下载失败"，而内容其实没问题——这种错最难查
	w.Header().Set("Content-Disposition", `attachment; filename="routerpanel-ipad.mobileconfig"`)
	_, _ = w.Write(buildMobileConfig(r, ws.port, ws.clipName()))
}

// ipadSetup 是给人在电脑上也能照着点的一页说明
func (ws *webServer) ipadSetup(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// panelURL 末尾带 "/"，拼子页面时要先去掉，否则出来的是 .../8788//ipad
	url := strings.TrimSuffix(panelURL(r, ws.port), "/")
	disp := url
	if disp == "" {
		disp = "http://<路由器地址>:" + fmt.Sprint(ws.port)
	}
	name := ws.clipName()
	fmt.Fprintf(w, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>把 iPad 变成面板</title><style>
body{background:#090a0b;color:#d0d3d7;font:15px/1.7 -apple-system,Helvetica,Arial,sans-serif;margin:0;padding:34px 22px}
.wrap{max-width:680px;margin:0 auto}
h1{font-size:19px;color:#f0f1f2;margin:0 0 4px}
p{color:#a8adb2;margin:0 0 14px}
ol{padding-left:20px;color:#d0d3d7}li{margin-bottom:10px}
code{background:#1b1d20;border:1px solid #32363a;padding:1px 5px;border-radius:5px;color:#f0f1f2;font-size:13px}
a.btn{display:inline-block;margin-top:8px;padding:9px 16px;border:1px solid #32363a;border-radius:999px;
color:#f0f1f2;text-decoration:none;background:#1b1d20}
.note{margin-top:24px;padding-top:14px;border-top:1px solid #22262a;font-size:13px;color:#7e848a}
</style></head><body><div class="wrap">
<h1>把 iPad 变成面板</h1>
<p>两件事各管各的：<b>自动锁定</b>决定屏会不会熄，<b>主屏幕图标</b>决定点开是什么样子。
图标有两条路，先说结论：<b>用 Safari「添加到主屏幕」</b>，描述文件那条留给需要"防误删"的人。</p>
<ol>
<li><b>设置 → 亮度 → 自动锁定 → 永不</b>。这一条才是"不熄屏"，跟装不装描述文件没关系。</li>
<li>在这台 iPad 的 Safari 里打开 <code>%s</code> → 底部分享按钮 → <b>添加到主屏幕</b>。
这条路出来的图标：点开那一瞬是黑的（启动图由页面自己发），状态栏也是黑的，logo 也在。</li>
<li>图标名字取自页面标题，也就是 <b>%s</b>；想改名字就改面板的 title 设置。</li>
<li>怕被人误删再用下面那个描述文件按钮，但代价要说清楚：iOS 9 走描述文件这条路时
<b>不读</b>页面的状态栏设置，也<b>不读</b>启动图——去设备上的二进制里挖过，
那条路上 SpringBoard 只认自己那份记录，图标容器里的 <code>WebClipStatusBarStyle</code>
没有任何代码在读。结果就是白启动 + 白条状态栏。两个图标指向同一个页面，留一个就够。</li>
<li>想防误触退出：设置 → 通用 → 辅助功能 → 引导式访问，进页面后连按三次 Home。
它只锁操作，不接管熄屏。</li>
</ol>
<a class="btn" href="%s/ipad.mobileconfig">装描述文件（防误删；iOS 9 上会是白栏）</a>
<div class="note">
面板地址：<code>%s</code><br>
描述文件不签名（iOS 会提示"未签名"，正常），也不禁止删除：
不想要时 设置 → 通用 → 描述文件 删掉即可，只有图标会消失，自动锁定不受影响。<br>
屏幕真熄了怎么知道？软路由那块屏和页面自己的心跳会报——面板上出现
<b>停止刷新</b> 或 <b>需重开页面</b>，就是该去点一下了。图标（全屏）和 Safari 打开都算数。<br>
phœnix 是半绑定越狱：彻底断电重启后要重新跑一次越狱。不过面板本身是网页，
越狱掉了也照样显示，只是 AFC2 那类越狱插件要重装。
</div></div></body></html>`, disp, name, url, disp)
}
