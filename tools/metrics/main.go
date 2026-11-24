// 字体度量探针：把各字号真实的 ascent/descent/height 打出来。
// 排版游标和重叠自检都必须用真值，不能按"1em"猜 —— 思源黑体的
// hhea 行距很宽，猜小了就出现"进度条切到文字下沿"那种缺陷。
package main

import (
	"fmt"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

func main() {
	for _, p := range os.Args[1:] {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Println(err)
			continue
		}
		f, err := opentype.Parse(b)
		if err != nil {
			fmt.Println(p, "parse:", err)
			continue
		}
		fmt.Println("==", p)
		for _, size := range []int{13, 14, 15, 16, 17, 18, 22, 26, 36, 44} {
			fa, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(size), DPI: 96})
			if err != nil {
				continue
			}
			m := fa.Metrics()
			// 顺带量一个真实串的行盒：数字+中文混排
			asc, desc := m.Ascent.Round(), m.Descent.Round()
			fmt.Printf("  size=%2d ascent=%2d descent=%2d height=%2d  ratio=%.2f\n",
				size, asc, desc, m.Height.Round(), float64(asc+desc)/float64(size))
			_ = font.Face(fa)
		}
	}
}
