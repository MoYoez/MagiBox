// Command preview renders the bundle chat-style HTML (real renderer + embedded sample data).
//
//	go run ./cmd/preview            # write preview.html
//	go run ./cmd/preview <path>     # write to the given path
//	go run ./cmd/preview -serve     # serve on :8099 for a browser/preview tool
package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"

	"github.com/moyoez/magibox/internal/bundle"
)

func main() {
	htmlStr := bundle.RenderHTML(demo())

	if len(os.Args) > 1 && os.Args[1] == "-serve" {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(htmlStr))
		})
		fmt.Println("preview serving on :8099")
		_ = http.ListenAndServe(":8099", nil)
		return
	}

	out := "preview.html"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.WriteFile(out, []byte(htmlStr), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("wrote", out)
}

func demo() *bundle.Bundle {
	const t0 = 1717977600
	return &bundle.Bundle{
		ID:      "demo",
		Title:   "周末爬山小分队",
		Started: t0,
		Ended:   t0 + 420,
		Messages: []bundle.Message{
			{Name: "小林", Username: "alice01", Text: "你好,周末一起去爬山吗?", Time: t0},
			{Name: "阿北", Username: "bobzhang", Text: "好啊!几点出发", Time: t0 + 90},
			{Name: "阿北", Username: "bobzhang", Text: "顺便问下,带不带帐篷?山顶过夜的话晚上挺冷的", Time: t0 + 110},
			{Name: "小林", Username: "alice01", Kind: "photo", Text: "上次在山顶拍的,景色绝了", Media: dataPNG(360, 200, color.RGBA{90, 160, 240, 255}), Time: t0 + 150},
			{Name: "小林", Username: "alice01", Kind: "photo", Media: dataPNG(360, 200, color.RGBA{120, 190, 140, 255}), Time: t0 + 160},
			{Name: "阿北", Username: "bobzhang", Kind: "sticker", Media: dataPNG(140, 140, color.RGBA{250, 200, 80, 255}), Time: t0 + 170},
			{Name: "Carol", Text: "带我一个!我有辆车,可以捎四个人 🚗", Time: t0 + 240},
			{Name: "Carol", Kind: "video", Media: dataPNG(340, 192, color.RGBA{60, 60, 70, 255}), Time: t0 + 300},
			{Name: "小林", Username: "alice01", Text: "[视频过大,未打包]", Time: t0 + 350},
			{Name: "阿北", Username: "bobzhang", Text: "哈哈,那记得带相机 📷 周六早上 7 点老地方见", Time: t0 + 420},
		},
	}
}

// dataPNG generates a data URI for a solid-color PNG (so the preview works offline).
func dataPNG(w, h int, c color.Color) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
