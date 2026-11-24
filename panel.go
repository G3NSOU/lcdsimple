package main

import (
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"time"
)

// 版式：用户给的 Meta 设计系统（DESIGN.md）翻成暗黑长亮屏 + 自由 bento。
//
// 这一版解决的三类问题，全部有实拍或自检背书：
//  1. 文字压边框 / 压分隔线 —— 卡片内一律留 pad(24) 安全边距，
//     游标推进用 inkH()（看得见的墨迹高度），不再用名义行高；
//  2. 图表盖住数据 —— 图表带单独登记，渲染完 Overlaps() 自检并报行；
//  3. 满屏大字 —— 字号只用 token 常量，大数字只在 KPI 出现，
//     其余靠字重（Light/Regular/Bold）和颜色分层级。
//
// 栅格：12 栏，卡片跨 3/4/5/12 栏，高度 184/430/剩余，形成大小不同的方块与长条。
// 颜色：文档没给暗色 token（Known Gaps 里写了），按它自己的色阶推，
// 强调色用 {colors.primary-soft}，语义色只在越界时出现。

type History struct {
	CPU, Mem, Down, Up *Ring
}

func NewHistory() *History {
	return &History{CPU: NewRing(150), Mem: NewRing(150), Down: NewRing(150), Up: NewRing(150)}
}

func (h *History) Push(s *Snap) {
	h.CPU.Push(s.CPUPct)
	h.Mem.Push(s.MemPct)
	h.Down.Push(s.Wan.RXr / 1e6)
	h.Up.Push(s.Wan.TXr / 1e6)
}

type rc struct{ x0, y0, x1, y1 int }

func (r rc) w() int { return r.x1 - r.x0 }
func (r rc) h() int { return r.y1 - r.y0 }

const (
	pageMargin = 40
	gridGap    = 20
	barH       = 76
	rowAH      = 184
	rowBH      = 460
	footerH    = 30
	pad        = spXl // 卡片内边距 24 = {spacing.xl}
	cols       = 12
)

// KPI 卡内部槽位
const (
	slotLabel = 18
	slotValue = 44
	slotViz   = 96
	slotVizH  = 40
	slotFoot  = 148
)

func Draw(cv *Canvas, s *Snap, h *History) *image.RGBA {
	W, H := cv.W, cv.H
	cv.ResetMarks()
	cv.Fill(cCanvas)

	colw := (W - 2*pageMargin - (cols-1)*gridGap) / cols
	col := func(x, n int) int { return x + n*colw + (n-1)*gridGap }

	x0, x1 := pageMargin, W-pageMargin
	y := pageMargin

	drawTopBar(cv, s, rc{x0, y, x1, y + barH})
	y += barH + gridGap

	// ---- 第一行：四个小方块 ----
	x := x0
	for i, n := range []int{3, 3, 3, 3} {
		r := rc{x, y, col(x, n), y + rowAH}
		switch i {
		case 0:
			drawCPUTile(cv, s, r)
		case 1:
			drawMemTile(cv, s, h, r)
		case 2:
			drawNetTile(cv, s, h, r)
		case 3:
			drawEgressTile(cv, s, r)
		}
		x = r.x1 + gridGap
	}
	y += rowAH + gridGap

	// ---- 第二行：5 / 4 / 3 三种宽度 ----
	clash := rc{x0, y, col(x0, 5), y + rowBH}
	fnos := rc{clash.x1 + gridGap, y, col(clash.x1+gridGap, 4), y + rowBH}
	dev := rc{fnos.x1 + gridGap, y, x1, y + rowBH}
	drawClashCard(cv, s, clash)
	drawFnosCard(cv, s, fnos)
	drawDevCard(cv, s, dev)
	y += rowBH + gridGap

	// ---- 第三行：整宽日志条，吃掉剩下的高度 ----
	drawLogCard(cv, s, rc{x0, y, x1, H - pageMargin - footerH - spXs})
	drawFooter(cv, s, H-pageMargin-footerH+spXs)
	return cv.img
}

// ---------------- 公共件 ----------------

// 卡片底：面是 cSurface，描边 1px hairline，扁平无阴影
func box(cv *Canvas, r rc, radius int) { boxWith(cv, r, radius, cHairSoft) }

func boxWith(cv *Canvas, r rc, radius int, border RGBA) {
	cv.Card(r.x0, r.y0, r.x1, r.y1, radius, 1, cSurface, border)
	cv.Mark(r.x0, r.y0, r.x1, r.y1, "panel")
}

// 卡片标题行：标题 + 说明 + 右侧状态，返回内容游标
func cardHead(cv *Canvas, r rc, title, sub string, right func(int, int)) int {
	top := r.y0 + spLg
	w := cv.TextTop(title, r.x0+pad, top, fSub, WBold, cInkDeep)
	if sub != "" {
		sw := cv.TextWidth(sub, fCaption, WRegular)
		// 说明文字不能顶到右侧状态上：先算位置，放不下就整体省略
		if r.x0+pad+w+spSm+sw < r.x1-pad-spXxl {
			cv.TextTop(sub, r.x0+pad+w+spSm, top+1, fCaption, WRegular, cStone)
		}
	}
	if right != nil {
		right(r.x1-pad, top-4)
	}
	y := top + inkH(fSub) + spMd
	cv.Rule(r.x0+pad, r.x1-pad, y, cHairSoft)
	return y + spMd
}

// 规格行：{component-tech-specs-table} —— 左标签 body-sm-bold，右数值 body-sm，
// 行间 1px hairline；返回值是下一行的游标
func specRow(cv *Canvas, r rc, y int, label, val string, valCol RGBA, valWeight int) int {
	lw := cv.TextWidth(label, fBodyS, WBold)
	cv.TextTop(label, r.x0+pad, y, fBodyS, WBold, cInk)
	vw := r.w() - 2*pad - lw - spMd
	cv.TextRight(cv.Ellipsis(val, vw, fBodyS, WRegular), r.x1-pad, y, fBodyS, valWeight, valCol)
	y += inkH(fBodyS) + spSm
	cv.Rule(r.x0+pad, r.x1-pad, y, cHairSoft)
	return y + spSm
}

// 指标格：小标签 + 稍大的值
func metric(cv *Canvas, x, y, w int, label, val string, col RGBA) {
	cv.TextTop(label, x, y, fCaption, WRegular, cSteel)
	cv.TextTop(cv.Ellipsis(val, w, fHeadingM, WLight), x, y+inkH(fCaption)+spXs, fHeadingM, WLight, col)
}

// metricRowH 是指标格占的高度，游标靠它推进
func metricRowH() int { return inkH(fCaption) + spXs + inkH(fHeadingM) + spLg }

// barColor：正常态就是灰白，越线才升级到 attention / critical。
// 整套界面除告警外没有第二种颜色 —— 蓝紫渐变那类"AI 仪表盘色"已经去掉。
// badgeBG：告警色在实心块上要压暗一档，否则白字压在亮红上会晃眼
func badgeBG(c RGBA) RGBA {
	if c == cCritical {
		return cCriticalSolid
	}
	return c
}

func barColor(v float64) RGBA {
	// 颜色契约：正常白/灰，吃紧橙，接近打满才红（红只留给"显著错误"）
	switch {
	case v >= 0.95:
		return cCritical
	case v >= 0.75:
		return cWarn
	default:
		return cInk
	}
}

// barRow：文字在上、5px 条在下，条永远在墨迹下面，不会再切到字上
func barRow(cv *Canvas, x0, x1, y int, name, val string, pct float64) int {
	nw := cv.TextWidth(name, fBodyS, WBold)
	cv.TextTop(name, x0, y, fBodyS, WBold, cInk)
	cv.TextRight(cv.Ellipsis(val, x1-x0-nw-spMd, fCaption, WRegular), x1, y, fCaption, WRegular, cSteel)
	by := y + inkH(fBodyS) + spXs
	cv.Bar(x0, by, x1, 5, pct, barColor(pct))
	return by + 5
}

// ---------------- 顶栏 ----------------

func drawTopBar(cv *Canvas, s *Snap, r rc) {
	// 顶栏是平铺 + 底部 1px hairline，不做成卡片（文档的 Top Navigation）
	cv.Rule(r.x0, r.x1, r.y1-spSm, cHairSoft)

	nameTop := r.y0 + spXs
	// 顶栏大字还是主机名——它是这台机器的身份，不该被产品名顶掉。
	// 取不到主机名才用面板名字占位，两个都没有才写"网络面板"。
	name := s.Hostname
	if name == "" {
		name = s.Title
	}
	if name == "" {
		name = "网络面板"
	}
	cv.Tracked(name, r.x0, nameTop, fHeadingL, WBold, 0.5, cInkDeep)
	// 副标题只有型号会被截：它是这行里唯一"看不全也不影响判断"的字段，
	// 系统名和运行时长是看状态的核心，必须完整留着。
	sub := topSub(cv, s, topTextCol)
	cv.TextTop(sub, r.x0+spXxs, nameTop+inkH(fHeadingL)+spSm, fCaption, WRegular, cSteel)

	// 时钟与日期共用一条基线
	base := r.y0 + 52
	clock := s.Time.Format("15:04")
	cw := cv.TextWidth(clock, fHero, WLight)
	cv.TextTop(clock, r.x1-cw, topFor(fHero, base), fHero, WLight, cInkDeep)
	date := s.Time.Format("2006年1月2日") + " " + weekdayCN(s.Time)
	dw := cv.TextWidth(date, fBodyS, WRegular)
	cv.TextTop(date, r.x1-cw-spLg-dw, topFor(fBodyS, base), fBodyS, WRegular, cSteel)

	// 状态药丸固定从文本列后面起笔。以前起点跟着实测文字宽度走，
	// 主机名或型号一长，第三枚就被挤到第二行去压日期——"始终一行"只能靠固定列宽。
	lx := r.x0 + topTextCol + spXxl
	// 限宽必须把日期也算进去：只留时钟的宽度时，第四枚药丸会直接压在"24日 周二"上
	limit := r.x1 - cw - spLg - dw - spXxl
	lx = drawChip(cv, lx, r.y0+24, wanPill(s), wanColor(s), limit, false)
	lx = drawChip(cv, lx, r.y0+24, clashPill(s), clashColor(s), limit, false)
	lx = drawChip(cv, lx, r.y0+24, fnosPill(s), fnosColor(s), limit, false)

	if !s.Wan.Connected {
		drawChip(cv, lx, r.y0+24, "外网中断", cCritical, limit, true)
	}
}

// 顶栏左边那列的固定宽度：大字 + 小字都在这里面，药丸从它后面起笔。
const topTextCol = 740

// topSub 组顶栏那行小字：系统 · 型号 · 内核 · 已运行。
// 放不下就一档一档收短型号（12 档试到底），再放不下才整行省略号——
// 宁可型号看不全，也不能把运行时间挤没，或者让三枚药丸掉到第二行。
func topSub(cv *Canvas, s *Snap, limit int) string {
	for _, n := range []int{40, 30, 24, 18, 12, 8} {
		t := fmt.Sprintf("%s  ·  %s  ·  kernel %s  ·  已运行 %s",
			s.OSName, shorten(s.Model, n), s.Kernel, HumanUptime(s.Uptime))
		if cv.TextWidth(t, fCaption, WRegular) <= limit {
			return t
		}
	}
	return cv.Ellipsis(fmt.Sprintf("%s  ·  %s  ·  kernel %s  ·  已运行 %s",
		s.OSName, shorten(s.Model, 8), s.Kernel, HumanUptime(s.Uptime)),
		limit, fCaption, WRegular)
}

// drawChip：false = {component-button-pill-tab}，true = {component-badge-critical}
func drawChip(cv *Canvas, x, y int, text string, c RGBA, limit int, solid bool) int {
	w := cv.TextWidth(text, fBodyS, WBold) + spLg + 14
	if x+w > limit {
		return x // 放不下就不画，绝不压到时钟上
	}
	if solid {
		return cv.Badge(x, y, text, RGBA{255, 255, 255, 255}, badgeBG(c), fBodyS, WBold) + spSm
	}
	txt := cInk
	if c == cCritical || c == cWarn || c == cGood {
		txt = c
	}
	return cv.Tab(x, y, text, c, txt) + spSm
}

func wanPill(s *Snap) string {
	// 模式是真识别出来的（拨号 / DHCP / 静态 / 旁路由），不是假定 PPPoE；
	// 连不连看默认路由，旁路由那种没有拨号口的接法不该被报成"未连接"。
	p := s.Wan.Mode
	if p == "" {
		p = protoLabel(s.Wan.Proto)
	}
	switch {
	case !s.Wan.Connected:
		return p + " 未连接"
	case p == "旁路由":
		// 旁路由没有自己的公网地址，能说明问题的就是"我把包交给谁"
		return p + " " + defStr(s.Wan.Via, "已连接")
	case s.Wan.IP == "":
		return p + " 已连接"
	default:
		return p + " " + s.Wan.IP
	}
}

// wanColor：网络"已连接"属于一般正常状态，用灰白；拨不上才是错误，用红。
func wanColor(s *Snap) RGBA {
	if s.Wan.Connected {
		return cInk
	}
	return cCritical
}

func protoLabel(p string) string {
	switch strings.ToLower(p) {
	case "pppoe":
		return "PPPoE"
	case "dhcp":
		return "DHCP"
	case "static":
		return "静态 IP"
	case "":
		return "WAN"
	default:
		return strings.ToUpper(p)
	}
}

func clashPill(s *Snap) string {
	switch {
	case !s.Clash.Installed:
		return "OpenClash 未装"
	case s.Clash.Running:
		return "OpenClash 运行中"
	case s.Clash.Enabled:
		return "OpenClash 未运行"
	default:
		return "OpenClash 已关闭"
	}
}
func clashColor(s *Snap) RGBA {
	switch {
	case s.Clash.Running:
		return cInk
	case s.Clash.Enabled:
		return cCritical
	default:
		return cStone
	}
}

func fnosPill(s *Snap) string {
	if s.Fnos.VMRunning {
		return "fnOS 运行中"
	}
	return "fnOS 已停止"
}
func fnosColor(s *Snap) RGBA {
	if s.Fnos.VMRunning {
		return cInk
	}
	return cCritical
}

func weekdayCN(t time.Time) string {
	return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[int(t.Weekday())]
}

// ---------------- 第一行：四个小方块 ----------------

func tileHead(cv *Canvas, r rc, label string) { tileHeadB(cv, r, label, cHairSoft) }

func tileHeadB(cv *Canvas, r rc, label string, border RGBA) {
	boxWith(cv, r, rXl, border)
	cv.TextTop(label, r.x0+pad, r.y0+slotLabel, fCaption, WBold, cSteel)
}

func tileFootText(cv *Canvas, r rc, text string) {
	tileFootColor(cv, r, text, cSteel)
}

func tileFootColor(cv *Canvas, r rc, text string, c RGBA) {
	cv.TextTop(cv.Ellipsis(text, r.w()-2*pad, fCaption, WRegular),
		r.x0+pad, r.y0+slotFoot, fCaption, WRegular, c)
}

// 大数字 + 单位 + 右上角补充信息，三者同基线
func tileValue(cv *Canvas, r rc, v, unit, right string, size int) {
	top := r.y0 + slotValue
	base := top + inkTop(size)
	w := cv.TextTop(v, r.x0+pad, top, size, WLight, cInkDeep)
	if unit != "" {
		cv.TextTop(unit, r.x0+pad+w+spXs, topFor(fBodyM, base), fBodyM, WRegular, cStone)
	}
	if right != "" {
		cv.TextRight(cv.Ellipsis(right, r.w()/2, fCaption, WRegular),
			r.x1-pad, topFor(fCaption, base), fCaption, WRegular, cStone)
	}
}

func drawCPUTile(cv *Canvas, s *Snap, r rc) {
	tileHead(cv, r, "CPU 占用")
	n := len(s.CorePct)
	v := fmt.Sprintf("%.0f", s.CPUPct)
	if n == 0 {
		v = "—"
	}
	tileValue(cv, r, v, "%", fmt.Sprintf("%d 线程", n), fDisplay)
	if n > 0 {
		gw := (r.w() - 2*pad - (n-1)*4) / n
		top := r.y0 + slotViz + 4
		hh := slotVizH - 8
		cv.Mark(r.x0+pad, top, r.x1-pad, top+hh, "track")
		for i, p := range s.CorePct {
			x := r.x0 + pad + i*(gw+4)
			cv.Rect(x, top, x+gw, top+hh, cHairSoft)
			fh := int(float64(hh) * clamp01(p/100))
			if fh > 0 && fh < 2 {
				fh = 2
			}
			c := cCharcoal
			if p >= 80 {
				c = barColor(p / 100)
			}
			cv.Rect(x, top+hh-fh, x+gw, top+hh, c)
		}
	}
	foot := fmt.Sprintf("负载 %.2f", s.Load[0])
	if s.TempPkg > 0 {
		foot += fmt.Sprintf("  ·  %.0f°C", s.TempPkg)
	}
	if s.Freq > 0 {
		foot += fmt.Sprintf("  ·  %.2f GHz", s.Freq)
	}
	tileFootText(cv, r, foot)
}

func drawMemTile(cv *Canvas, s *Snap, h *History, r rc) {
	tileHead(cv, r, "内存占用")
	tileValue(cv, r, fmt.Sprintf("%.0f", s.MemPct), "%", "共 "+HumanBytes(s.MemTotal), fDisplay)
	cv.Spark(h.Mem.Slice(), r.x0+pad, r.y0+slotViz, r.x1-pad, r.y0+slotViz+slotVizH, cCharcoal, 100)
	foot := "已用 " + HumanBytes(s.MemUsed)
	alloc := s.Fnos.MemTotal
	if alloc == 0 {
		alloc = s.Fnos.AllocBytes
	}
	if alloc > 0 {
		foot += fmt.Sprintf("  ·  fnOS 占 %s", HumanBytes(alloc))
	}
	if s.DockerRun > 0 {
		foot += fmt.Sprintf("  ·  容器 %d", s.DockerRun)
	}
	tileFootText(cv, r, foot)
}

func drawNetTile(cv *Canvas, s *Snap, h *History, r rc) {
	tileHead(cv, r, "WAN 流量")
	base := r.y0 + slotValue + inkTop(fHeadingL)
	d := "↓ " + HumanBits(s.Wan.RXr)
	u := "↑ " + HumanBits(s.Wan.TXr)
	x := r.x0 + pad
	x += cv.TextTop(d, x, topFor(fHeadingL, base), fHeadingL, WLight, cInk) + spMd
	cv.TextTop(cv.Ellipsis(u, r.x1-pad-x, fHeadingL, WLight), x, topFor(fHeadingL, base),
		fHeadingL, WLight, cStone)

	mx := 0.05
	for _, v := range append(append([]float64{}, h.Down.Slice()...), h.Up.Slice()...) {
		if v > mx {
			mx = v
		}
	}
	cv.Spark(h.Down.Slice(), r.x0+pad, r.y0+slotViz, r.x1-pad, r.y0+slotViz+slotVizH, cInk, mx)
	drawOverlay(cv, h.Up.Slice(), r.x0+pad, r.y0+slotViz, r.x1-pad, r.y0+slotViz+slotVizH, cStone, mx)

	dev := s.DevOnline
	if dev == 0 {
		dev = s.DevCount
	}
	foot := fmt.Sprintf("在线设备 %d", dev)
	if n := len(s.Ifaces); n > 0 {
		up := 0
		for _, it := range s.Ifaces {
			if it.Up {
				up++
			}
		}
		foot += fmt.Sprintf("  ·  网口 %d/%d", up, n)
	}
	if s.Wan.GW != "" {
		foot += "  ·  网关 " + s.Wan.GW
	}
	tileFootText(cv, r, foot)
}

// viaLabel 把取数层的长句压成页脚放得下的短词
func viaLabel(v string) string {
	switch {
	case strings.Contains(v, "代理"):
		return "代理 " + v[strings.LastIndex(v, ":")+1:]
	case strings.Contains(v, "OpenClash"):
		return "内核转发"
	default:
		return v
	}
}

// 出口方块：把"我现在的 IP 在哪儿"提到第一行，这是用户第一眼要看的
func drawEgressTile(cv *Canvas, s *Snap, r rc) {
	cl := s.Clash
	// 出口 IP 回到大陆境内 = 真实身份暴露，属于"一般警告"，按约定的橙色调处理：
	// 描边 + 页脚计时，不升级成正红（正红留给已经出错的链路）
	if cl.EgressCN {
		tileHeadB(cv, r, "网络出口", cWarn)
	} else {
		tileHead(cv, r, "网络出口")
	}
	via := "直连"
	if cl.Running && strings.Contains(cl.EgressVia, "代理") {
		via = "代理"
	} else if strings.Contains(cl.EgressVia, "转发") {
		via = "转发"
	}
	if cl.EgressIP == "" {
		tileValue(cv, r, "—", "", via, fHeadingL)
		msg := defStr(cl.EgressErr, "等待外网后探测")
		cv.TextTop(cv.Ellipsis(msg, r.w()-2*pad, fBodyS, WRegular),
			r.x0+pad, r.y0+slotViz+8, fBodyS, WRegular, cSteel)
		foot := "出口 IP 探测走 " + via
		if !s.Wan.Up {
			foot += " · 外网中断时不探测"
		}
		tileFootText(cv, r, foot)
		return
	}
	tileValue(cv, r, cl.EgressIP, "", "", fHeadingL)
	// 归属：城市 · 国家码 · ISP，先拼完整再画（以前先画后拼，ISP 永远看不见）
	parts := []string{}
	if loc := defStr(cl.EgressCity, ""); loc != "" {
		parts = append(parts, loc)
	}
	if cc := cl.EgressCC; cc != "" {
		parts = append(parts, cc)
	}
	if isp := shorten(cl.EgressISP, 16); isp != "" {
		parts = append(parts, isp)
	}
	line := strings.Join(parts, " · ")
	if line == "" {
		line = "归属未知"
	}
	cv.TextTop(cv.Ellipsis(line, r.w()-2*pad, fHeadingS, WLight),
		r.x0+pad, r.y0+slotViz+6, fHeadingS, WLight, cInk)
	foot := fmt.Sprintf("%.0f 秒前探测", cl.EgressAge)
	if cl.EgressVia != "" {
		foot = viaLabel(cl.EgressVia) + "  ·  " + foot
	}
	if cl.EgressCN {
		// 只留"暴露多久 + 什么时候探的"：把代理口字样也拼上，这行就会被省略号截断，
		// 而这条恰恰是不能被截断的那句
		foot = "IP 已暴露 " + HumanExposure(cl.EgressExposure) +
			fmt.Sprintf("  ·  %.0f 秒前探测", cl.EgressAge)
	}
	if !s.Wan.Up {
		foot = "外网中断时不探测"
	}
	tileFootText(cv, r, foot)
}

func drawOverlay(cv *Canvas, xs []float64, x0, y0, x1, y1 int, c RGBA, mx float64) {
	if len(xs) < 2 || mx <= 0 {
		return
	}
	prevX, prevY := 0, 0
	for i, v := range xs {
		x := x0 + (x1-x0-1)*i/(len(xs)-1)
		y := y1 - 2 - int(float64(y1-y0-2)*clamp01(v/mx))
		if i > 0 {
			cv.Line(prevX, prevY, x, y, c)
		}
		prevX, prevY = x, y
	}
}

// ---------------- OpenClash ----------------

func drawClashCard(cv *Canvas, s *Snap, r rc) {
	box(cv, r, rXxxl)
	cl := s.Clash
	y := cardHead(cv, r, "OpenClash", cl.Version, func(rx, ty int) {
		txt, dot := "已关闭", cStone
		switch {
		case cl.Running:
			txt, dot = "运行中", cGood
		case cl.Enabled:
			txt, dot = "未运行", cCritical
		}
		if cl.Running || !cl.Enabled {
			w := cv.TextWidth(txt, fBodyS, WBold) + spLg + 14
			cv.Tab(rx-w, ty, txt, dot, cInk)
		} else {
			w := cv.TextWidth(txt, fBodyS, WBold) + 2*spMd
			cv.Badge(rx-w, ty, txt, RGBA{255, 255, 255, 255}, cCriticalSolid, fBodyS, WBold)
		}
	})

	if !cl.Installed {
		cv.TextTop("未安装 OpenClash", r.x0+pad, y+spXl, fHeadingM, WLight, cStone)
		return
	}
	if !cl.Running {
		msg := "内核未运行 · 配置中已关闭"
		if cl.Enabled {
			msg = "配置里是开启的，但内核没起来"
		}
		cv.TextTop(msg, r.x0+pad, y+spMd, fHeadingM, WLight, cCharcoal)
		// 大字号后面留足呼吸：这里曾经只留 8px，22px 的字和 15px 的标签
		// 看起来就是叠在一起（自检按墨迹算不算重叠，肉眼算算）
		y += inkH(fHeadingM) + spXxl
		if cl.Enabled && cl.Err != "" {
			y = specRow(cv, r, y, "最近错误", cl.Err, cCritical, WRegular)
		}
		if cl.CtrlAddr != "" {
			y = specRow(cv, r, y, "控制接口", cl.CtrlAddr, cCharcoal, WRegular)
		}
		if cl.Mode != "" {
			y = specRow(cv, r, y, "运行模式", cl.Mode, cCharcoal, WRegular)
		}
		if cl.ConfigFile != "" {
			y = specRow(cv, r, y, "配置文件", filepath.Base(cl.ConfigFile), cCharcoal, WRegular)
		}
		cv.TextTop("内核起来后，这里显示当前节点、出口归属、活跃连接与累计流量。",
			r.x0+pad, y+spMd, fCaption, WRegular, cStone)
		return
	}

	// 当前节点
	g := cl.Primary
	cv.TextTop("当前节点", r.x0+pad, y, fCaption, WRegular, cSteel)
	nw := r.w() - 2*pad - spXl
	if g.Name != "" {
		nw -= cv.TextWidth("分组 "+shorten(g.Name, 16), fBodyS, WRegular) + spLg
	}
	// CJK 方头括号【】的墨迹会顶出名义墨框，这里按肉眼而不是按 inkH 留白
	ny := y + inkH(fCaption) + spMd
	cv.TextTop(cv.Ellipsis(defStr(g.Now, "（未选择）"), nw, fHeadingL, WRegular),
		r.x0+pad, ny, fHeadingL, WRegular, cInkDeep)
	if g.Name != "" {
		cv.TextRight("分组 "+shorten(g.Name, 16), r.x1-pad,
			topFor(fBodyS, ny+inkTop(fHeadingL)), fBodyS, WRegular, cSteel)
	}
	y = ny + inkH(fHeadingL) + spLg

	// 出口地址
	cv.TextTop("出口地址", r.x0+pad, y, fCaption, WRegular, cSteel)
	ey := y + inkH(fCaption) + spXxs
	if cl.EgressIP != "" {
		iw := cv.TextTop(cl.EgressIP, r.x0+pad, ey, fHeadingM, WLight, cInkDeep)
		if loc := defStr(cl.EgressCity, cl.EgressCC); loc != "" {
			txt := loc + " · " + cl.EgressCC
			if cl.EgressISP != "" {
				txt += " · " + shorten(cl.EgressISP, 18)
			}
			tx := r.x0 + pad + iw + spMd
			cv.TextTop(cv.Ellipsis(txt, r.x1-pad-tx, fBodyS, WRegular),
				tx, topFor(fBodyS, ey+inkTop(fHeadingM)), fBodyS, WRegular, cInk)
		}
		det := viaLabel(cl.EgressVia)
		if cl.EgressAge > 20 {
			det += fmt.Sprintf("  ·  %.0f 秒前", cl.EgressAge)
		}
		cv.TextRight(det, r.x1-pad, topFor(fCaption, ey+inkTop(fHeadingM)), fCaption, WRegular, cStone)
	} else {
		msg := defStr(cl.EgressErr, "探测中…")
		cv.TextTop(cv.Ellipsis(msg, r.w()-2*pad, fHeadingM, WRegular), r.x0+pad, ey,
			fHeadingM, WLight, cWarn)
	}
	y = ey + inkH(fHeadingM) + spLg

	colsSpec := []struct{ k, v string }{
		{"模式", defStr(cl.Mode, "-")},
		{"节点数", fmt.Sprintf("%d", cl.ProxyTotal)},
		{"活跃连接", fmt.Sprintf("%d", cl.ConnCount)},
		{"节点延迟", latencyText(cl.Latency)},
		{"累计下行", HumanBytes(cl.DownTotal)},
		{"累计上行", HumanBytes(cl.UpTotal)},
	}
	cw := (r.w() - 2*pad) / 3
	rh := metricRowH()
	for i, c := range colsSpec {
		metric(cv, r.x0+pad+(i%3)*cw, y+(i/3)*rh, cw-spLg, c.k, c.v, cInk)
	}
	y += ((len(colsSpec) + 2) / 3) * rh

	// 其他策略组：用户在页面上勾了分组，就整份照他勾的顺序显示，一个都不藏——
	// 挪一个去当"当前节点"、列表里就少一行，看起来像丢了。
	// 没勾过才退回"主分组之外、按名字排序"。
	var others []ProxyGroup
	title := "其他分组"
	if len(cl.Show) > 0 {
		others = cl.Show
		title = "勾选分组"
	} else {
		for _, g := range cl.Groups {
			if g.Name != cl.Primary.Name {
				others = append(others, g)
			}
		}
	}
	if len(others) > 0 {
		cv.TextTop(title, r.x0+pad, y, fCaption, WBold, cSteel)
		y += inkH(fCaption) + spSm
		cv.Rule(r.x0+pad, r.x1-pad, y, cHairSoft)
		y += spSm
		room := (r.y1 - pad - y) / 34
		if room > 5 {
			room = 5
		}
		for i, g := range others {
			if i >= room {
				// 摆不下就明说还剩几个，别让人以为订阅里就这几个组。
				// 这里不能写"少勾两个"——勾多是 iPad 那张屏放得下、这块屏放不下，
				// 让人少勾等于为了路由器上的显示器牺牲 iPad 上的信息。
				y = specRow(cv, r, y, "另有 "+fmt.Sprintf("%d", len(others)-room)+" 个",
					"iPad 上都在", cStone, WRegular)
				break
			}
			y = specRow(cv, r, y, g.Name, defStr(g.Now, "未选择"), cCharcoal, WRegular)
		}
	}
}

func latencyText(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// ---------------- fnOS ----------------

// spec 是卡片里"小标题 + 数值"的一行，颜色留给语义（正常灰白、警告橙、错误红）
type spec struct {
	k, v string
	c    RGBA
}

func drawFnosCard(cv *Canvas, s *Snap, r rc) {
	box(cv, r, rXxxl)
	f := s.Fnos
	// 地址是用户在设置里填的，卡片只显示它；没填就写"未配置"，不预置任何人的内网 IP
	meta := f.Host
	if meta == "" {
		meta = "未配置"
	}
	y := cardHead(cv, r, "fnOS 飞牛", meta, func(rx, ty int) {
		if f.VMRunning {
			txt := "虚拟机运行中"
			w := cv.TextWidth(txt, fBodyS, WBold) + spLg + 14
			cv.Tab(rx-w, ty, txt, cGood, cInk)
		} else {
			txt := "虚拟机已停止"
			w := cv.TextWidth(txt, fBodyS, WBold) + 2*spMd
			cv.Badge(rx-w, ty, txt, RGBA{255, 255, 255, 255}, cCriticalSolid, fBodyS, WBold)
		}
	})

	specs := []spec{}
	if !f.SSHO {
		msg := defStr(f.SSHErr, "SSH 未就绪")
		cv.TextTop(cv.Ellipsis("取不到客户机内部数据："+msg, r.w()-2*pad, fBodyS, WRegular),
			r.x0+pad, y, fBodyS, WRegular, cWarn)
		y += inkH(fBodyS) + spXs
		cv.TextTop("面板仍显示宿主机侧能看到的状态。", r.x0+pad, y, fCaption, WRegular, cStone)
		y += inkH(fCaption) + spMd
		alloc := f.AllocBytes
		if f.VMRunning && alloc > 0 {
			specs = []spec{
				{"分配内存", HumanBytes(alloc), cInk},
				{"宿主占用", fmt.Sprintf("%.2f 核", f.QemuCores), cInk},
				{"Web 面板", fmt.Sprintf("%d", f.Web), cInk},
			}
			// SSH 不通时读不到客户机内部，"Web 面板"是宿主侧唯一还能验证的东西，
			// 这一栏就只在这个降级视图里留着
		}
	} else {
		memPct := 0.0
		if f.MemTotal > 0 {
			memPct = (f.MemTotal - f.MemAvail) / f.MemTotal * 100
		}
		specs = []spec{
			{"CPU 利用率", pctText(f.CpuPct), cInk},
			{"内存利用率", pctText(memPct), cInk},
			{"已运行", HumanUptime(f.Uptime), cInk},
			{"读取", HumanBytesPerSec(f.DiskR), cInk},
			{"写入", HumanBytesPerSec(f.DiskW), cInk},
			{gpuLabel(f), p620Text(f), gpuColor(f)},
		}
	}
	// fnOS 卡只有 4 栏宽，指标用 2 列：3 列会把"15% / 15.6GB"截成"15% / 15.6…"
	cw := (r.w() - 2*pad) / 2
	rh := metricRowH() - spSm
	for i, m := range specs {
		metric(cv, r.x0+pad+(i%2)*cw, y+(i/2)*rh, cw-spLg, m.k, m.v, m.c)
	}
	y += ((len(specs)+1)/2)*rh + spSm

	// 存储卷：留够 4 行的位置，以后往池里加卷不用改版面
	freeNote := ""
	for _, d := range s.Disks {
		if !d.IsVMStore {
			continue
		}
		free := d.Size - d.Used
		freeNote = fmt.Sprintf("镜像盘余量 %s", HumanBytes(free))
		if free < 20e9 {
			freeNote = fmt.Sprintf("镜像盘余量仅 %s", HumanBytes(free))
		}
	}
	cv.TextTop("存储卷", r.x0+pad, y, fCaption, WBold, cSteel)
	if freeNote != "" {
		c := cStone
		if strings.Contains(freeNote, "仅") {
			c = cCritical
		}
		cv.TextRight(freeNote, r.x1-pad, y, fCaption, WRegular, c)
	}
	y += inkH(fCaption) + spSm
	cv.Rule(r.x0+pad, r.x1-pad, y, cHairSoft)
	y += spSm
	// 卷行按 38px 一条排，卡里能容纳 4 条：以后往池里加卷有位置
	room := (r.y1 - pad - y) / 38
	if room > 4 {
		room = 4
	}
	for i, v := range f.Vols {
		if i >= room {
			break
		}
		if !v.Available {
			cv.TextTop(v.Name+"  不可用", r.x0+pad, y, fBodyS, WRegular, cStone)
			y += 38
			continue
		}
		pct := 0.0
		if v.Total > 0 {
			pct = v.Used / v.Total
		}
		barRow(cv, r.x0+pad, r.x1-pad, y, v.Name,
			HumanBytes(v.Used)+" / "+HumanBytes(v.Total), pct)
		y += 38
	}
	if len(f.Vols) == 0 && f.SSHO {
		cv.TextTop("没读到卷信息", r.x0+pad, y, fBodyS, WRegular, cStone)
	}
}

// gpuLabel 卡上那行的小标题：知道卡型就别只写"P620"了
func gpuLabel(f Fnos) string {
	if f.GPUOk && f.GPUName != "" {
		return f.GPUName
	}
	return "P620 显卡"
}

// p620Text 利用率 + 显存占用；驱动真不在就写清为什么，不再拿"驱动未装"当兜底
// pctText 小于 10% 时留一位小数：空转时 CPU 常年在 1~2%，写成 "2%" 和 "1%"
// 来回跳看着像抖动，"1.4%" 才知道它真的在动。
func pctText(p float64) string {
	if p <= 0 {
		return "—"
	}
	if p < 10 {
		return fmt.Sprintf("%.1f%%", p)
	}
	return fmt.Sprintf("%.0f%%", p)
}

func p620Text(f Fnos) string {
	if !f.GPUOk {
		if f.GPUErr != "" {
			return f.GPUErr
		}
		return "未检出"
	}
	if f.GPUMemTot > 0 {
		// 显存写成 GB：卡片那栏只有半列宽，"0/2048MB" 会顶到边
		return fmt.Sprintf("%.0f%% · %.1f/%.0fG", f.GPUUtil, f.GPUMem/1024, f.GPUMemTot/1024)
	}
	return fmt.Sprintf("%.0f%%", f.GPUUtil)
}

func gpuColor(f Fnos) RGBA {
	if !f.GPUOk {
		return cWarn
	}
	return barColor(f.GPUUtil / 100)
}

// ---------------- 接入设备 ----------------

// panelTagColor：显示中算正常态（白/灰），要点一下是橙，连不上才是红
func panelTagColor(t string) RGBA {
	switch t {
	case "失联":
		return cCritical
	case "需重开", "停止刷新":
		return cWarn
	default:
		return cStone
	}
}

func drawDevCard(cv *Canvas, s *Snap, r rc) {
	box(cv, r, rXxxl)
	n := len(s.Devs)
	on := s.DevOnline
	if on == 0 {
		for _, d := range s.Devs {
			if d.On {
				on++
			}
		}
	}
	y := cardHead(cv, r, "接入设备", fmt.Sprintf("在线 %d / 记录 %d", on, n), nil)
	if n == 0 {
		cv.TextTop("ARP 与 DHCP 都没读到设备", r.x0+pad, y+spMd, fBodyS, WRegular, cStone)
		return
	}
	// 两行一条：名字 + 速率在上，IP + 会话数在下；比硬塞一行好读
	pitch := 46
	room := (r.y1 - pad - y) / pitch
	if room > 7 {
		room = 7
	}
	for i, d := range s.Devs {
		if i >= room {
			break
		}
		dot := cDisabled
		txt := cSteel
		if d.On {
			dot, txt = cInk, cInkDeep
		}
		cv.Dot(r.x0+pad+4, y+inkTop(fBodyS)/2+2, 4, dot)
		nx := r.x0 + pad + 16
		cv.TextTop(cv.Ellipsis(d.Name, r.w()-2*pad-16-140, fBodyS, WBold), nx, y, fBodyS, WBold, txt)
		if d.Panel != "" {
			// 面板机这一行右侧固定给画面状态：那点上流量本来就是面板自己拉数产生的，
			// 报给它自己没有意义，"要不要去点一下"才是有用的
			cv.TextRight(d.Panel, r.x1-pad, y, fCaption, WRegular, panelTagColor(d.Panel))
		} else if d.On && d.RXr+d.TXr > 200 {
			// 哪一侧没流量就不写哪一侧，避免出现"↓ 0 bps ↑ 2.4 Kbps"这种废话
			rt := ""
			if d.RXr > 200 {
				rt += "↓" + HumanBits(d.RXr)
			}
			if d.TXr > 200 {
				if rt != "" {
					rt += " "
				}
				rt += "↑" + HumanBits(d.TXr)
			}
			cv.TextRight(rt, r.x1-pad, y, fCaption, WRegular, cInkDeep)
		} else if d.On {
			cv.TextRight("空闲", r.x1-pad, y, fCaption, WRegular, cStone)
		} else {
			cv.TextRight("离线", r.x1-pad, y, fCaption, WRegular, cStone)
		}
		// 第二行左边是身份（IP / MAC 尾段），右边是会话数，
		// 全塞左边会被省略成"12 会…"
		sub := d.IP
		if d.Name == "未命名设备" && len(d.MAC) >= 8 {
			sub += "  ·  " + strings.ToUpper(d.MAC[len(d.MAC)-8:])
		}
		if d.State != "" && !d.On {
			sub += "  ·  " + d.State
		}
		sy := y + inkH(fBodyS) + spXxs
		// 第二行可用宽度只受"会话数"那一列影响，和上一行的名字宽度无关
		// （之前把 nw 也减掉，MAC 尾段被省略成"A6:E2:…"）
		cv.TextTop(cv.Ellipsis(sub, r.x1-pad-nx-70, fMicro, WRegular), nx, sy, fMicro, WRegular, cStone)
		if d.Conns > 0 {
			cv.TextRight(fmt.Sprintf("%d 会话", d.Conns), r.x1-pad, sy, fMicro, WRegular, cSteel)
		}
		y += pitch
	}
}

// ---------------- 日志与页脚 ----------------

func drawLogCard(cv *Canvas, s *Snap, r rc) {
	box(cv, r, rXxxl)
	y := cardHead(cv, r, "系统日志", "iStore · OpenClash · fnOS · 面板操作", func(rx, ty int) {
		cv.TextRight("最近 "+fmt.Sprintf("%d", len(s.Logs))+" 条", rx, ty+spXs, fCaption, WRegular, cStone)
	})
	if len(s.Logs) == 0 {
		cv.TextTop("没有可显示的系统日志", r.x0+pad, y+spMd, fBodyS, WRegular, cStone)
		return
	}
	// 屏幕放得下的行数：融合栏是 tail 视图，从头画会把"刚刚点的那一下"挤出屏幕，
	// 而这块屏就是给不在 iPad 前的人看动作结果的
	pitch := inkH(fBodyS) + spSm + spXs
	list := s.Logs
	if fit := (r.y1 - pad - y + spXs) / pitch; fit > 0 && len(list) > fit {
		list = list[len(list)-fit:]
	}
	for _, l := range list {
		if y+inkH(fBodyS) > r.y1-pad {
			break
		}
		stamp, msg := "", l
		if len(l) > 10 && l[8] == ' ' && l[9] == ' ' && l[2] == ':' && l[5] == ':' {
			stamp, msg = l[:8], l[10:]
		}
		tx := r.x0 + pad
		if stamp != "" {
			tx += cv.TextTop(stamp, r.x0+pad, y, fCaption, WRegular, cStone) + spMd
		}
		// 来源标签单独着色：这一栏现在是四路融合日志，"这条是谁说的"必须先看清
		if strings.HasPrefix(msg, "[") {
			if k := strings.Index(msg, "] "); k > 0 && k < 12 {
				tx += cv.TextTop(msg[:k+1], tx, y, fCaption, WRegular, cSteel) + spSm
				msg = msg[k+2:]
			}
		}
		lvl := cInk
		switch {
		case strings.Contains(msg, "error"), strings.Contains(msg, "fail"),
			strings.Contains(msg, "segfault"), strings.Contains(msg, "Fail"):
			lvl = cCritical
		case strings.Contains(msg, "warn"), strings.Contains(msg, "denied"),
			strings.Contains(msg, "refused"), strings.Contains(msg, "no route"):
			lvl = cWarn
		}
		cv.TextTop(cv.Ellipsis(msg, r.x1-pad-tx, fBodyS, WRegular), tx, y, fBodyS, WRegular, lvl)
		y += inkH(fBodyS) + spSm
		cv.Rule(r.x0+pad, r.x1-pad, y, cHairSoft)
		y += spXs
	}
}

func drawFooter(cv *Canvas, s *Snap, y int) {
	left := fmt.Sprintf("routerpanel  ·  刷新 %s", s.Time.Format("15:04:05"))
	if len(s.Notes) > 0 {
		left += "   取数提示：" + strings.Join(s.Notes, " | ")
	}
	cv.TextTop(cv.Ellipsis(left, cv.W-2*pageMargin-300, fMicro, WRegular), pageMargin, y, fMicro, WRegular, cStone)
	cv.TextRight(fmt.Sprintf("%d×%d  ·  每 %ds 刷新", cv.W, cv.H, refreshSeconds),
		cv.W-pageMargin, y, fMicro, WRegular, cStone)
}

// refreshSeconds 由 main.go 在启动时写进来，页脚要说实话
var refreshSeconds = 3

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
