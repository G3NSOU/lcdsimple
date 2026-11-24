// 把 /dev/fb0 的原始转储还原成 PNG，用来确认"屏幕上真的是什么"。
// PNG 调试回路证明的是程序想画什么，这个证明的是写进帧缓冲的东西对不对
// （字节序反了、分辨率不对、有残影，只有这一步能看出来）。
// 用法: go run ./tools/fb2png -in fb.raw -size 1920x1080 -stride 7680 -pixfmt bgra -out /tmp/fb.png
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
)

func main() {
	in := flag.String("in", "", "帧缓冲原始转储")
	size := flag.String("size", "1920x1080", "WxH")
	stride := flag.Int("stride", 0, "每行字节数，0=W*4")
	pixfmt := flag.String("pixfmt", "bgra", "bgra 或 rgba")
	out := flag.String("out", "/tmp/fb.png", "输出 PNG")
	flag.Parse()

	var W, H int
	if _, err := fmt.Sscanf(*size, "%dx%d", &W, &H); err != nil {
		fmt.Println("size 要写成 1920x1080")
		os.Exit(1)
	}
	if *stride == 0 {
		*stride = W * 4
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if len(data) < *stride*H {
		fmt.Printf("转储只有 %d 字节，需要 %d\n", len(data), *stride*H)
		os.Exit(1)
	}
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	// 与 main.go 的 packPixels 互为反函数：
	// bgra(默认) 内存里是 B,G,R,X；rgba 是 R,G,B,X
	for y := 0; y < H; y++ {
		row := data[y**stride:]
		for x := 0; x < W; x++ {
			p := row[x*4:]
			r, g, b := p[2], p[1], p[0]
			if *pixfmt == "rgba" {
				r, b = p[0], p[2]
			}
			i := y*img.Stride + x*4
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, 255
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
	fmt.Println(*out, W, H, "stride", *stride, *pixfmt)
}

