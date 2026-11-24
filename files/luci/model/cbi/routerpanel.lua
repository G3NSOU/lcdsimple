-- 软路由屏幕状态面板的设置页。
-- 状态一律现读现算，不缓存：他关心的是"现在这块屏/这个页面到底在不在跑"，
-- 上一秒的值在这里就是错的。
local sys = require "luci.sys"
local uci = require "luci.model.uci".cursor()

local function trim(s)
	return (s or ""):gsub("^%s+", ""):gsub("%s+$", "")
end

local function readfile(path)
	local f = io.open(path, "r")
	if not f then return "" end
	local s = f:read("*a")
	f:close()
	return s or ""
end

-- 进程在不在，只认 pidfile + kill -0：procd 的 "running" 会算上正在重启的间隙，
-- 那种时候页面显示"运行中"就成了谎话
local function running()
	local pid = trim(readfile("/var/run/routerpanel.pid"))
	if pid == "" or not pid:match("^%d+$") then return false end
	return sys.call("kill -0 " .. pid .. " 2>/dev/null") == 0
end

local function getfirst(opt, dflt)
	local v = trim(uci:get_first("routerpanel", "panel", opt) or "")
	if v == "" then return dflt end
	return v
end

-- 网页地址从"进程实际监听的 socket"里读，不拿 uci 的 http_bind 猜：
-- 留空时程序自己挑 br-lan 的地址，写死一个 LAN IP 反而可能指到已经改掉的口上。
-- 也绝不返回 0.0.0.0 或 WAN 地址——这台机器同时是拨号路由器。
local function weburl(port)
	if not port or port == "" or port == "0" then return nil end
	local f = io.popen("netstat -ltn 2>/dev/null", "r")
	if not f then return nil end
	for line in f:lines() do
		local ip, p = line:match("(%d+%.%d+%.%d+%.%d+):(%d+)")
		if ip and p == port and ip:sub(1, 4) ~= "127." and ip ~= "0.0.0.0" then
			f:close()
			return "http://" .. ip .. ":" .. port .. "/"
		end
	end
	f:close()
	return nil
end

-- 面板自己的 JSON 接口是现成的数据源，比在 Lua 里重算一遍 CPU/出口/模式靠谱，
-- 也不会出现"服务页说连上了、iPad 页说没连上"
local function state(url)
	local ok, jsonc = pcall(require, "luci.jsonc")
	if not ok or not url then return nil end
	local cmd = "wget -q -T 2 -O /tmp/routerpanel.luci.json '" .. url .. "api/state' 2>/dev/null"
	if sys.call(cmd) ~= 0 then return nil end
	local raw = readfile("/tmp/routerpanel.luci.json")
	if raw == "" then return nil end
	local good, obj = pcall(jsonc.parse, raw)
	if good and type(obj) == "table" then return obj end
	return nil
end

local m = Map("routerpanel", translate("Regisseur 面板"))
m.description = translate("软路由屏幕面板 + iPad 局域网网页，两块屏共用同一份采集结果。")

-- 这些设置全是启动参数，改完必须让进程重起一次才生效（init.d 里的
-- reload_service 就是干这个的）。默认情况下 LuCI 把 apply 推给前端的异步请求去做，
-- 页面关了、或者从命令行保存，改动就只落在配置里、进程一动不动——
-- 于是出现"我明明关了屏幕输出，屏上还是那一帧"。这里让服务端当场 commit + apply。
m.apply_on_parse = false

-- apply_on_parse 只管到 commit + apply；这台机器上没有 /lib/ucitrigger（查过），
-- uci apply 不会去碰 /etc/init.d/routerpanel，进程照旧。所以自己补一刀。
-- 后台跑：重启要两三秒，挂在保存请求上页面会像卡死。
function m.on_after_apply(map)
	sys.call("/etc/init.d/routerpanel restart > /dev/null 2>&1 &")
end

local enabled = getfirst("enabled", "0")
local screen = getfirst("screen", "1")
local port = getfirst("http_port", "")
local url = weburl(port)
local st = state(url)

local sec = m:section(SimpleSection)
sec.template = "routerpanel/status"
sec.running = running()
sec.enabled = enabled
sec.screen = screen
sec.url = url or ""
sec.port = port
sec.st = st or {}
sec.ipad = (st and st.IPad) or {}
sec.clash = (st and st.Clash) or {}
sec.egress = (st and st.Clash and st.Clash.Egress) or {}

local s = m:section(TypedSection, "panel", translate("设置"))
s.anonymous = true

local en = s:option(Flag, "enabled", translate("启用"))
en.description = translate("关掉就完全不启进程：屏幕交还系统控制台，iPad 网页也一起停。")
en.rmempty = true
en.default = "0"

local scr = s:option(Flag, "screen", translate("屏幕输出"))
scr.description = translate("关掉后不再写 /dev/fb0，也不占用控制台，只留 iPad 网页——不想接显示器就关这个。接口是 DVI 还是 HDMI 都一样，有输出就能拉起画面。")
scr.rmempty = true
scr.default = "1"

local hp = s:option(Value, "http_port", translate("iPad 网页端口"))
hp.description = translate("留空或 0 = 不起网页。只监听局域网地址，不会监听拨号出口那侧。")
hp.datatype = "port"
hp.rmempty = true
hp.default = "8788"

local ct = s:option(Flag, "control", translate("允许网页操作"))
ct.description = translate("开了才能在 iPad 上切代理模式、开关 OpenClash；操作会写进底部日志栏。")
ct.rmempty = true
ct.default = "0"

local tk = s:option(Value, "control_token", translate("访问 Token"))
tk.description = translate("页面第一次操作时输入一次。留空且启用了上面的开关，进程会自动生成一个写回这里。")
tk.password = true
tk.rmempty = true

local bar = s:option(ListValue, "status_bar", translate("iOS 状态栏样式"))
bar.description = translate("只在从桌面图标打开（全屏网页）时有效，改完把图标划掉重开一次再看效果。")
bar:value("black", translate("不透明黑"))
bar:value("translucent", translate("透明黑（页面自己垫黑那 20 像素）"))
bar.rmempty = true
bar.default = "black"

local ti = s:option(Value, "title", translate("面板名字"))
ti.description = translate("屏幕顶栏的大字、网页标题、iPad 桌面图标都用它。")
ti.rmempty = true
ti.default = "Regisseur"

local iv = s:option(Value, "interval", translate("刷新间隔（秒）"))
iv.datatype = "and(uinteger, min(1), max(60))"
iv.rmempty = true
iv.default = "3"

local eg = s:option(Flag, "egress", translate("探测出口 IP"))
eg.description = translate("关掉就不测出口，也就不会有「IP 回到大陆」的橙色告警。")
eg.rmempty = true
eg.default = "1"

local ih = s:option(Value, "ipad_host", translate("iPad 面板机地址"))
ih.description = translate("留空就不探这台设备。用 SSH 只读取系统信息，密钥是 /etc/routerpanel/id_ipad。")
ih.datatype = "host"
ih.rmempty = true

local adv = m:section(TypedSection, "panel", translate("高级"))
adv.anonymous = true
adv.addremove = false

local fn = adv:option(Value, "fnos_host", translate("fnOS 地址"))
fn.description = translate("飞牛虚拟机的内网地址。留空 = 不探 fnOS，卡片上写「未配置」——" ..
	"这里没有默认值，因为每家的网段都不一样，预置一个等于把别人的环境发给所有人。")
fn.datatype = "host"
fn.rmempty = true

local fu = adv:option(Value, "fnos_user", translate("fnOS 用户"))
fu.description = translate("SSH 只读取系统信息用的用户名，留空同样不探。密钥在 /etc/routerpanel/id_panel。")
fu.rmempty = true

local fm = adv:option(Value, "fnos_mon", translate("qemu monitor socket"))
fm.description = translate("留空就用宿主机上的 qemu 进程判断虚拟机在不在跑；填了则直接问 qemu，准确一点。")
fm.rmempty = true

local px = adv:option(ListValue, "pixfmt", translate("帧缓冲字节序"))
px:value("bgra", "bgra")
px:value("rgba", "rgba")
px.description = translate("屏幕颜色红蓝反了才需要改成 rgba。")
px.rmempty = true

return m
