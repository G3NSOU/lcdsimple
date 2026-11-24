// 看图工具：从整帧 PNG 里裁一块并放大，用于确认排版细节。
// 用法: go run ./tools/crop -in shot/x.png -rect 1450,180,1880,410 -scale 2 -out /tmp/crop.png
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"
)

func main() {
	in := flag.String("in", "", "源 PNG")
	rect := flag.String("rect", "", "x0,y0,x1,y1")
	scale := flag.Int("scale", 2, "放大倍数（整数）")
	out := flag.String("out", "/tmp/crop.png", "输出")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	p := strings.Split(*rect, ",")
	if len(p) != 4 {
		fmt.Println("rect 需要 4 个数")
		os.Exit(1)
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	x0, y0, x1, y1 := n(p[0]), n(p[1]), n(p[2]), n(p[3])
	b := img.Bounds()
	if x1 > b.Dx() {
		x1 = b.Dx()
	}
	if y1 > b.Dy() {
		y1 = b.Dy()
	}
	w, h := x1-x0, y1-y0
	dst := image.NewRGBA(image.Rect(0, 0, w*(*scale), h*(*scale)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.At(x0+x, y0+y)
			for sy := 0; sy < *scale; sy++ {
				for sx := 0; sx < *scale; sx++ {
					dst.Set(x*(*scale)+sx, y*(*scale)+sy, c)
				}
			}
		}
	}
	oh, err := os.Create(*out)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer oh.Close()
	if err := png.Encode(oh, dst); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println(*out, w, h)
}
