package main

// 截图模式（-mask）：把这一帧里所有 IP 和 MAC 换成 x.x.x.x / xx:xx:xx:xx:xx:xx 再画。
//
// 为什么在数据层做，而不是画完之后往图上涂方块：地址散在顶栏、出口卡、fnOS 抬头、
// 设备列表、控制接口 URL 好几处，靠肉眼挑坐标迟早漏一个，而漏掉的那个正好是本人最不想
// 公开的东西。它们全都是字符串，反射走一遍快照就一个都不剩。
//
// 只动字符串里的地址，数字、百分比、时长一律不碰，所以版式和真实截图是一致的——
// 唯一变短的是 "198.51.100.10" 变成 "x.x.x.x"，只会更宽松不会挤。

import (
	"fmt"
	"reflect"
	"regexp"
)

var (
	ipRe  = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}`)
	macRe = regexp.MustCompile(`(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}`)
	urlRe = regexp.MustCompile(`(?i)(https?://)([^/\s:"']*)(:[0-9]+)?`)
)

// maskStr 打码一个字符串里的地址。主机名也一起打：有人把 NAS 的地址写成了
// 自定义域名，那种字符串里没有点分四段，光靠 ipRe 抓不到。
func maskStr(s string) string {
	if s == "" {
		return s
	}
	// 顺序有讲究：URL 先走，因为自定义域名里没有点分四段，只靠 ipRe 抓不到；
	// 端口留着（:9090 这种是服务约定，不是个人信息），只换主机名
	s = urlRe.ReplaceAllString(s, "${1}x.x.x.x${3}")
	s = ipRe.ReplaceAllString(s, "x.x.x.x")
	s = macRe.ReplaceAllString(s, "xx:xx:xx:xx:xx:xx")
	return s
}

// MaskSnap 就地打码一份快照。反射遍历，所以以后 Snap 加字段不用再记得回来补。
func MaskSnap(s *Snap) {
	if s == nil {
		return
	}
	maskValue(reflect.ValueOf(s).Elem())
	// 下面这些都不是地址，反射抓不到，但都是各人自己起的名字。
	// 截图发出去最容易被忽略的就是这一类：主机名是顶栏大标题，设备名是别人
	// 的电脑/手机名，往往直接就是本人 ID。
	if s.Hostname != "" {
		s.Hostname = "hostname"
	}
	for i := range s.Devs {
		s.Devs[i].Name = fmt.Sprintf("设备 %d", i+1)
	}
}

func maskValue(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(maskStr(v.String()))
		}
	case reflect.Struct:
		// time.Time 之类的内部字段是私有的，逐个看能不能设，不设就跳过
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath != "" {
				continue
			}
			maskValue(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			maskValue(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if v.MapIndex(k).Kind() == reflect.String {
				v.SetMapIndex(k, reflect.ValueOf(maskStr(v.MapIndex(k).String())))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			maskValue(v.Elem())
		}
	}
}
