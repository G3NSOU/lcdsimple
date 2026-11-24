package main

// 画布与绘制原语。全部画在一张 image.RGBA 上，再按 /dev/fb0 的字节序转出去，
// 这样 "-png 导出" 和 "写屏" 用的是同一份像素，排版可以离线看图迭代。

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type RGBA = color.RGBA

// 设计令牌取自用户给的 DESIGN.md（Meta 设计系统）。文档的 Known Gaps 里写明
// "没有公开的暗色 token"，而这块屏长亮要省电，所以按文档自己的色阶逻辑推了一套：
// 画布压到近黑、hairline 提亮一档、灰阶保持中性（R≈G≈B，不要偏蓝的"科技灰"），
// 语义色收敛成白/灰 + 绿 + 橙 + 红四种，各管一件事。
var (
	cCanvas      = RGBA{9, 10, 11, 255}
	cSurface     = RGBA{17, 18, 20, 255}    // 卡面
	cSurfaceSoft = RGBA{27, 29, 32, 255}    // 轨道 / 次级面
	cHairline    = RGBA{50, 54, 58, 255}    // {colors.hairline} 的暗色对应物
	cHairSoft    = RGBA{34, 37, 41, 255}    // 卡片描边、行分隔
	cInkDeep     = RGBA{240, 241, 242, 255} // 主文字
	cInk         = RGBA{208, 211, 215, 255}
	cCharcoal    = RGBA{168, 173, 178, 255}
	cSteel       = RGBA{126, 132, 138, 255}
	cStone       = RGBA{88, 94, 100, 255}
	// 语义色只有四种，分工是用户定的：
	//   白/灰 = 普通信息，以及"网络已连接"这类一般正常状态
	//   绿    = 服务在跑：OpenClash 运行中、fnOS 虚拟机运行中
	//   橙    = 一般提示 / 警告，还没到错误
	//   红    = 一切显著错误
	// 文档里的 {colors.primary} 品牌蓝整块去掉：长亮屏上它只会显得像模板生成的
	// 蓝紫仪表盘；critical 的 #e41e3f 蓝通道太高、暗底上发粉，所以换成正红。
	cGood          = RGBA{38, 200, 106, 255} // #26C86A 运行正常
	cWarn          = RGBA{255, 145, 30, 255} // #FF911E 提示 / 警告
	cCritical      = RGBA{255, 51, 51, 255}  // #FF3333 错误：文字、圆点、越线进度条
	cCriticalSolid = RGBA{211, 16, 30, 255}  // #D3101E 实心错误 badge 底色
	cDisabled      = RGBA{56, 61, 67, 255}   // {colors.disabled-text} 的暗色版
)

// 字重：0=Light(300) 1=Regular(400/500) 2=Bold(700)。
// 文档里 editorial subhead 用 300，大数字用 400/500，标签用 700，
// 三档都在，才拉得开"简约科技感"的层次。
const (
	WLight = iota
	WRegular
	WBold
)

// 字号直接对应文档的 typography token，名字保持一致方便对照
const (
	fHero     = 44 // {typography.hero-display} 48，屏上只给页头时钟
	fDisplay  = 36 // {typography.display-lg}
	fHeadingL = 26 // {typography.heading-lg}
	fHeadingM = 22 // {typography.heading-md}
	fHeadingS = 18 // {typography.heading-sm}
	fSub      = 17 // {typography.subtitle-lg}
	fBodyM    = 16 // {typography.body-md}
	fBodyS    = 15 // {typography.body-sm}
	fCaption  = 14 // {typography.caption}
	fMicro    = 13 // {typography.caption} 页脚细则
)

// 间距与圆角同样照 token 走
const (
	spXxs, spXs, spSm, spMd, spBase, spLg, spXl, spXxl, spXxxl = 4, 8, 10, 12, 16, 20, 24, 32, 40
	rMd, rLg, rXl, rXxl, rXxxl                                 = 6, 8, 16, 24, 32
)

// ---------------- 杂项 ----------------

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func defStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

type Canvas struct {
	img    *image.RGBA
	W, H   int
	faces  map[[2]int]font.Face // [字号, 字重] -> face
	parsed map[int]*opentype.Font
	marks  []mark // 排版自检用的矩形登记
}

type mark struct {
	x0, y0, x1, y1 int
	tag            string
}

func NewCanvas(w, h int) *Canvas {
	return &Canvas{img: image.NewRGBA(image.Rect(0, 0, w, h)), W: w, H: h,
		faces: map[[2]int]font.Face{}, parsed: map[int]*opentype.Font{}}
}

// 三档字重各自的 TTF（必须是 glyf，Go 的纯 Go 光栅器读不了 CFF）。
// Light 缺失就退回 Regular，不至于因为少一个字体文件整个面板起不来。
func (cv *Canvas) loadFonts(lightPath, normalPath, boldPath string) error {
	for i, p := range []string{lightPath, normalPath, boldPath} {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			if i == WLight {
				continue
			}
			return fmt.Errorf("读字体 %s 失败: %w", p, err)
		}
		f, err := opentype.Parse(b)
		if err != nil {
			return fmt.Errorf("解析字体 %s 失败: %w", p, err)
		}
		cv.parsed[i] = f
	}
	if cv.parsed[WRegular] == nil {
		return fmt.Errorf("没有可用的常规字重字体")
	}
	if cv.parsed[WLight] == nil {
		cv.parsed[WLight] = cv.parsed[WRegular]
	}
	return nil
}

func (cv *Canvas) face(size, weight int) font.Face {
	if weight < WLight || weight > WBold {
		weight = WRegular
	}
	k := [2]int{size, weight}
	if f, ok := cv.faces[k]; ok {
		return f
	}
	src := cv.parsed[WRegular]
	if weight == WBold {
		src = cv.parsed[WBold]
	} else if weight == WLight {
		src = cv.parsed[WLight]
	}
	f, err := opentype.NewFace(src, &opentype.FaceOptions{
		Size: float64(size), DPI: 96, Hinting: font.HintingNone})
	if err != nil {
		f = cv.faces[[2]int{size, WRegular}]
	}
	cv.faces[k] = f
	return f
}

// 参考包用的 OTF 是 CFF 曲线，纯 Go 光栅器读不了，所以部署前必须先转成 TTF。
// 这里再兜一道：万一个别字符不在子集里（emoji、生僻字），画出来是豆腐块，先过滤。
var keepRanges = [][2]rune{
	{0x20, 0x7E}, {0xA0, 0xFF}, {0x100, 0x17F},
	{0x2000, 0x206F}, {0x2100, 0x214F}, {0x2190, 0x21FF},
	{0x2500, 0x25CF}, {0x2600, 0x26FF}, {0x2700, 0x27BF},
	{0x3000, 0x303F}, {0x4E00, 0x9FFF}, {0xFE10, 0xFE4F}, {0xFF00, 0xFFEF},
}

func supported(r rune) bool {
	if r == '\n' || r == '\t' {
		return true
	}
	if unicode.Is(unicode.Han, r) {
		return true
	}
	for _, rg := range keepRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

func Sanitize(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if !supported(r) {
			continue // 直接丢掉，不补空格：节点名里多是国旗 emoji
		}
		if r == '\t' {
			r = ' '
		}
		if r == ' ' && prevSpace {
			continue
		}
		prevSpace = r == ' '
		b.WriteRune(r)
	}
	return b.String()
}

func (cv *Canvas) px(x, y int, c color.Color) {
	if x < 0 || y < 0 || x >= cv.W || y >= cv.H {
		return
	}
	cv.img.Set(x, y, c)
}

func (cv *Canvas) Fill(c color.Color) {
	for y := 0; y < cv.H; y++ {
		for x := 0; x < cv.W; x++ {
			cv.img.Set(x, y, c)
		}
	}
}

func (cv *Canvas) Rect(x0, y0, x1, y1 int, c color.Color) {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > cv.W {
		x1 = cv.W
	}
	if y1 > cv.H {
		y1 = cv.H
	}
	if x1 <= x0 || y1 <= y0 {
		return
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cv.img.Set(x, y, c)
		}
	}
}

func (cv *Canvas) RoundRect(x0, y0, x1, y1, r int, c color.Color) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if inRoundRect(x, y, x0, y0, x1, y1, r) {
				cv.img.Set(x, y, c)
			}
		}
	}
}

func inRoundRect(x, y, x0, y0, x1, y1, r int) bool {
	if x < x0 || y < y0 || x >= x1 || y >= y1 {
		return false
	}
	cx, cy := -1, -1
	switch {
	case x < x0+r && y < y0+r:
		cx, cy = x0+r, y0+r
	case x >= x1-r && y < y0+r:
		cx, cy = x1-r-1, y0+r
	case x < x0+r && y >= y1-r:
		cx, cy = x0+r, y1-r-1
	case x >= x1-r && y >= y1-r:
		cx, cy = x1-r-1, y1-r-1
	default:
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// 描边框：先填一圈边框色，再在里面填内色，避免逐像素判边界
func (cv *Canvas) Card(x0, y0, x1, y1, r, bw int, bg, border color.Color) {
	if border != nil && bw > 0 {
		cv.RoundRect(x0, y0, x1, y1, r, border)
		cv.RoundRect(x0+bw, y0+bw, x1-bw, y1-bw, r, bg)
	} else {
		cv.RoundRect(x0, y0, x1, y1, r, bg)
	}
}

func (cv *Canvas) HLine(x0, x1, y, h int, c color.Color) { cv.Rect(x0, y, x1, y+h, c) }

// Rule 是文档里的 hairline：1px 分隔线，卡片行与行之间用它而不是边框
func (cv *Canvas) Rule(x0, x1, y int, c color.Color) { cv.Rect(x0, y, x1, y+1, c) }

// Tint 是真正的 alpha 叠色。浅色底上要的是"12% 钴蓝"这种淡底，
// 不是预先 blend 好的死颜色，所以这里读回目标像素再混。
func (cv *Canvas) Tint(x0, y0, x1, y1 int, c RGBA, a float64) {
	if a <= 0 {
		return
	}
	if a > 1 {
		a = 1
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if x < 0 || y < 0 || x >= cv.W || y >= cv.H {
				continue
			}
			i := y*cv.img.Stride + x*4
			d := cv.img.Pix
			d[i+0] = uint8(float64(c.R)*a + float64(d[i+0])*(1-a))
			d[i+1] = uint8(float64(c.G)*a + float64(d[i+1])*(1-a))
			d[i+2] = uint8(float64(c.B)*a + float64(d[i+2])*(1-a))
		}
	}
}

// 墨迹行盒：源光的 hhea 是 ascent 1.55em / descent 0.4em（CJK 字体的通病），
// 按 ascent 放基线会让文字整体掉到"名义顶边"下面半行 —— 上一版所有
// "进度条切到文字下沿""文字压出卡片边框"都是这一个原因。
// 这里改成按看得见的墨迹定位：大写/CJK 顶到基线约 0.86em，下行约 0.18em。
func inkTop(size int) int { return int(float64(size)*0.86 + 0.5) }
func inkBot(size int) int { return int(float64(size)*0.18 + 0.5) }

func (cv *Canvas) Text(s string, x, baseline int, size, weight int, c color.Color) int {
	s = Sanitize(s)
	if s == "" {
		return 0
	}
	f := cv.face(size, weight)
	adv := font.MeasureString(f, s)
	d := &font.Drawer{Dst: cv.img, Src: image.NewUniform(c), Face: f,
		Dot: fixed.Point26_6{X: fixed.I(x), Y: fixed.I(baseline)}}
	d.DrawString(s) // DrawString 没有返回值，宽度只能自己量
	return adv.Round()
}

// top 是文字块看得见的顶边。行盒按墨迹登记给重叠自检用。
func (cv *Canvas) TextTop(s string, x, top, size, weight int, c color.Color) int {
	w := cv.Text(s, x, top+inkTop(size), size, weight, c)
	if w > 0 {
		cv.Mark(x, top, x+w, top+inkTop(size)+inkBot(size), "text")
	}
	return w
}

func (cv *Canvas) TextWidth(s string, size, weight int) int {
	s = Sanitize(s)
	if s == "" {
		return 0
	}
	return font.MeasureString(cv.face(size, weight), s).Round()
}

// 超长截断加省略号，中文按宽度算
func (cv *Canvas) Ellipsis(s string, maxW, size, weight int) string {
	if cv.TextWidth(s, size, weight) <= maxW {
		return s
	}
	rs := []rune(Sanitize(s))
	for i := range rs {
		part := string(rs[:i])
		if cv.TextWidth(part+"…", size, weight) > maxW {
			return strings.TrimRight(string(rs[:i]), " ") + "…"
		}
	}
	return s
}

// 右对齐，用于卡片里"标签 ......... 值"的排法；返回文字左边缘的 x，方便接着往左排
func (cv *Canvas) TextRight(s string, xRight, top, size, weight int, c color.Color) int {
	w := cv.TextWidth(s, size, weight)
	cv.TextTop(s, xRight-w, top, size, weight, c)
	return xRight - w
}

// Tracked 逐字加字距。Go 的 font.Drawer 不支持 letterSpacing，
// 而文档里 wordmark / 标签全靠字距拉开，这里用一个最小实现补上。
func (cv *Canvas) Tracked(s string, x, top, size, weight int, extra float64, c color.Color) int {
	total := 0
	for _, r := range Sanitize(s) {
		w := cv.Text(string(r), x+total, top+inkTop(size), size, weight, c)
		total += w + int(extra)
	}
	total -= int(extra) // 最后一个字后面不加距
	if total > 0 {
		cv.Mark(x, top, x+total, top+inkTop(size)+inkBot(size), "text")
	}
	return total
}

// inkH 是一行看得见的墨迹高度；游标推进必须用它，
// 用 lineH(1.2em) 会把上一行的下沿和下一行的上沿算重。
func inkH(size int) int { return inkTop(size) + inkBot(size) }

// 文档里的 lineHeight 1.4~1.5，用于需要"行框"而不是"墨迹"的场合
func lineH(size int) int { return int(float64(size)*1.45 + 0.5) }

// 同一基线对齐不同字号：返回该字号该放在哪条 top 上
func topFor(size, baseline int) int { return baseline - inkTop(size) }

// Badge 是 {component-badge-*}：填充药丸 + caption-bold，状态一律用它
func (cv *Canvas) Badge(x, y int, text string, fg, bg RGBA, size, weight int) int {
	tw := cv.TextWidth(text, size, weight)
	ih := inkTop(size) + inkBot(size)
	h := ih + spXs + 4
	padY := (h - ih) / 2
	cv.Mark(x, y, x+tw+2*spMd, y+h, "pill")
	cv.RoundRect(x, y, x+tw+2*spMd, y+h, h/2, bg)
	cv.TextTop(text, x+spMd, y+padY, size, weight, fg)
	return x + tw + 2*spMd
}

// Tab 是 {component-button-pill-tab}：白底 + hairline 描边 + 前置状态点
func (cv *Canvas) Tab(x, y int, text string, dot color.Color, txt color.Color) int {
	tw := cv.TextWidth(text, fBodyS, WBold)
	ih := inkTop(fBodyS) + inkBot(fBodyS)
	h := ih + spXs + 4
	padY := (h - ih) / 2
	w := tw + spLg + 14
	cv.Mark(x, y, x+w, y+h, "pill")
	// 暗底上药丸要比卡面亮一档，而不是比卡面暗，否则像挖了个洞
	cv.RoundRect(x, y, x+w, y+h, h/2, cSurfaceSoft)
	cv.strokePill(x, y, x+w, y+h, cHairline)
	if dot != nil {
		cv.Dot(x+spMd+3, y+h/2, 4, dot)
	}
	cv.TextTop(text, x+spMd+14, y+padY, fBodyS, WBold, txt)
	return x + w
}

func (cv *Canvas) strokePill(x0, y0, x1, y1 int, c color.Color) {
	r := (y1 - y0) / 2
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if inRoundRect(x, y, x0, y0, x1, y1, r) &&
				!inRoundRect(x, y, x0+1, y0+1, x1-1, y1-1, r) {
				cv.px(x, y, c)
			}
		}
	}
}

// 进度条：轨道 surfaceSoft，填充按语义色，圆角走 {rounded.sm}
func (cv *Canvas) Bar(x0, y, x1, h int, ratio float64, c color.Color) {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	cv.Mark(x0, y, x1, y+h, "bar")
	cv.RoundRect(x0, y, x1, y+h, h/2, cSurfaceSoft)
	w := int(float64(x1-x0) * ratio)
	if w >= h {
		cv.RoundRect(x0, y, x0+w, y+h, h/2, c)
	} else if w > 0 {
		cv.Rect(x0, y, x0+w, y+h, c)
	}
}

func (cv *Canvas) Dot(x, y, r int, c color.Color) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				cv.px(x+dx, y+dy, c)
			}
		}
	}
}

// 迷你折线：文档里没有坐标轴和网格，走势只留一条 2px 线 + 10% 面积淡底。
// 之前那版画了底色块 + 4 条网格线，在 40px 高的条带里糊成条纹，
// 还把下面的文字盖住了 —— 浅色主题下更要克制。
func (cv *Canvas) Spark(xs []float64, x0, y0, x1, y1 int, c RGBA, maxHint float64) {
	if len(xs) < 2 {
		return
	}
	cv.Mark(x0, y0, x1, y1, "spark")
	mx := maxHint
	if mx <= 0 {
		for _, v := range xs {
			if v > mx {
				mx = v
			}
		}
	}
	if mx <= 0 {
		mx = 1
	}
	n := len(xs)
	base := y1 - 1
	pt := func(i int) (int, int) {
		v := xs[i]
		if v > mx {
			v = mx
		}
		x := x0 + (x1-x0-1)*i/(n-1)
		y := base - int((float64(y1-y0-2))*(v/mx))
		return x, y
	}
	for i := 1; i < n; i++ {
		ax, ay := pt(i - 1)
		bx, by := pt(i)
		for x := ax; x <= bx; x++ {
			y := ay
			if bx > ax {
				y = ay + (by-ay)*(x-ax)/(bx-ax)
			}
			cv.Tint(x, y, x+1, base, c, 0.10)
		}
	}
	prevX, prevY := pt(0)
	for i := 1; i < n; i++ {
		x, y := pt(i)
		cv.Line(prevX, prevY, x, y, c)
		cv.Line(prevX, prevY+1, x, y+1, c)
		prevX, prevY = x, y
	}
}

// ---------------- 排版自检 ----------------

func (cv *Canvas) Mark(x0, y0, x1, y1 int, tag string) {
	cv.marks = append(cv.marks, mark{x0, y0, x1, y1, tag})
}

func (cv *Canvas) ResetMarks() { cv.marks = nil }

// benign 组合：卡片底、药丸底本来就要包住里面的文字；
// 同一条 spark 的两次登记也不算冲突。
func benign(a, b string) bool {
	for _, t := range []string{"panel", "pill", "track"} {
		if a == t || b == t {
			return true
		}
	}
	return a == b
}

// Overlaps 找出互相压住的行盒/图表区。用户抱怨"元素重叠"，
// 靠肉眼看缩略图不可靠，所以每帧渲染完自己报一次。
func (cv *Canvas) Overlaps() []string {
	var out []string
	for i := 0; i < len(cv.marks); i++ {
		for j := i + 1; j < len(cv.marks); j++ {
			a, b := cv.marks[i], cv.marks[j]
			if benign(a.tag, b.tag) {
				continue
			}
			ox := min(a.x1, b.x1) - max(a.x0, b.x0)
			oy := min(a.y1, b.y1) - max(a.y0, b.y0)
			if ox > 1 && oy > 1 {
				out = append(out, fmt.Sprintf("%s(%d,%d,%d,%d) × %s(%d,%d,%d,%d) 交叠 %dx%d",
					a.tag, a.x0, a.y0, a.x1, a.y1, b.tag, b.x0, b.y0, b.x1, b.y1, ox, oy))
			}
			if len(out) >= 12 {
				return out
			}
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func blend(a, b RGBA, ka float64) RGBA {
	kb := 1 - ka
	return RGBA{uint8(float64(a.R)*kb + float64(b.R)*ka),
		uint8(float64(a.G)*kb + float64(b.G)*ka),
		uint8(float64(a.B)*kb + float64(b.B)*ka), 255}
}

func (cv *Canvas) Line(x0, y0, x1, y1 int, c color.Color) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		cv.px(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// Ring 是折线用的环形缓冲
type Ring struct {
	buf []float64
	i   int
	n   int
}

func NewRing(cap int) *Ring { return &Ring{buf: make([]float64, cap)} }

func (r *Ring) Push(v float64) {
	r.buf[r.i] = v
	r.i = (r.i + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

func (r *Ring) Slice() []float64 {
	out := make([]float64, 0, r.n)
	start := (r.i - r.n + len(r.buf)) % len(r.buf)
	for k := 0; k < r.n; k++ {
		out = append(out, r.buf[(start+k)%len(r.buf)])
	}
	return out
}

func (r *Ring) Last() float64 {
	if r.n == 0 {
		return 0
	}
	return r.buf[(r.i-1+len(r.buf))%len(r.buf)]
}

// 速率人性化：bps 传进来就自己换算，别在外面写一长串 if
func HumanBits(bps float64) string {
	units := []string{"bps", "Kbps", "Mbps", "Gbps"}
	i := 0
	for bps >= 1000 && i < len(units)-1 {
		bps /= 1024
		i++
	}
	if bps < 10 && i > 0 {
		return fmt.Sprintf("%.1f %s", bps, units[i])
	}
	return fmt.Sprintf("%.0f %s", bps, units[i])
}

func HumanBytesPerSec(bps float64) string {
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	i := 0
	for bps >= 1024 && i < len(units)-1 {
		bps /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", bps, units[i])
	}
	return fmt.Sprintf("%.1f %s", bps, units[i])
}

func HumanBytes(b float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f%s", b, units[i])
	}
	return fmt.Sprintf("%.1f%s", b, units[i])
}

func HumanUptime(sec float64) string {
	d := int(sec) / 86400
	h := int(sec) % 86400 / 3600
	m := int(sec) % 3600 / 60
	if d > 0 {
		return fmt.Sprintf("%d天%d小时", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%d小时%d分", h, m)
	}
	return fmt.Sprintf("%d分", m)
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
