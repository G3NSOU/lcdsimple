package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type geo struct {
	IP          string `json:"ip"`
	Query       string `json:"query"`
	CountryCode string `json:"country_code"`
}

func try(name, px string) {
	tr := &http.Transport{}
	if px != "" {
		u, err := url.Parse(px)
		if err != nil {
			fmt.Println(name, "bad url", err)
			return
		}
		tr.Proxy = http.ProxyURL(u)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ip.sb/geoip", nil)
	t0 := time.Now()
	res, err := (&http.Client{Transport: tr, Timeout: 7 * time.Second}).Do(req)
	if err != nil {
		fmt.Printf("%-22s %.1fs ERR %v\n", name, time.Since(t0).Seconds(), err)
		return
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	var g geo
	json.Unmarshal(b, &g)
	fmt.Printf("%-22s %.1fs %d %s ip=%q cc=%q\n", name, time.Since(t0).Seconds(), res.StatusCode, first(string(b), 60), g.IP, g.CountryCode)
}

func first(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func main() {
	for _, a := range [][2]string{
		{"direct", ""},
		{"proxy 7890 http", "http://127.0.0.1:7890"},
		{"proxy 7893 mixed", "http://127.0.0.1:7893"},
		{"proxy 7892", "http://127.0.0.1:7892"},
	} {
		try(a[0], a[1])
	}
	os.Exit(0)
}
