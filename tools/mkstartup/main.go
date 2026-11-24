// 生成 Web Clip 的启动图（StartupImage）：一张纯黑 PNG。
//
// 为什么需要：iOS 9 从桌面图标打开全屏网页时，在页面第一帧画出来之前会先铺一张
// 白底启动画面，状态栏那一条也跟着是白的——页面自己的背景色再黑也来不及。
// 描述文件里给了 StartupImage 之后，启动瞬间就是这张黑图，白条不再出现。
//
// 用法: go run ./tools/mkstartup -out web/assets/ipad-startup.png -size 1024x768
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	out := flag.String("out", "web/assets/ipad-startup.png", "输出 PNG")
	size := flag.String("size", "1024x768", "WxH，跟 iPad 横屏一致")
	flag.Parse()

	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		fmt.Println("size 要写成 1024x768")
		os.Exit(1)
	}

	// 和页面底色同一个黑，启动画面切到第一帧时看不出跳变。
	// 用 1 色调色板而不是 RGBA：纯色图这样出来是 1KB 级，RGBA 那版要 14KB。
	// 不是洁癖——iPad 上没有 base64、没有 perl，往设备里塞文件只能把字节转成
	// printf 的八进制转义塞进一条命令里，小一个数量级就少一次"命令太长"。
	ink := color.RGBA{R: 9, G: 10, B: 11, A: 255}
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{ink})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, ink)
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println(*out, w, "x", h)
}
