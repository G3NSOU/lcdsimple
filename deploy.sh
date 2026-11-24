#!/bin/bash
# 推二进制 + 字体 + 服务脚本到路由器。
# 默认：如果服务已启用就重启它，否则只更新文件（-png 调试回路不受影响）。
# 注意：iStoreOS 的 busybox 没有 pkill、sleep 只认整数，别在这儿踩坑。
set -eu
cd "$(dirname "$0")"
export PATH=/opt/homebrew/bin:$PATH GOFLAGS=-mod=mod GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
# 目标机器和 SSH 私钥从环境或同目录的 .deploy.conf 读，仓库里不预置任何人的地址。
# .deploy.conf 是 gitignore 的，本地写一行 PANEL_HOST=root@192.0.2.1 就能用。
CONF="$(dirname "$0")/.deploy.conf"
[ -f "$CONF" ] && . "$CONF"
: "${PANEL_HOST:?没有路由器地址：export PANEL_HOST=root@<地址>，或写进 .deploy.conf}"
: "${PANEL_KEY:?没有 SSH 私钥：export PANEL_KEY=$HOME/.ssh/id_ed25519，或写进 .deploy.conf}"
KEY="$PANEL_KEY"; HOST="$PANEL_HOST"
SSH="ssh -i $KEY -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=8 $HOST"
SCP="scp -q -i $KEY -o IdentitiesOnly=yes -o BatchMode=yes"

if [ "${1:-}" = "build" ]; then
  gofmt -w *.go tools/crop/*.go
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/routerpanel .
  echo "built: $(ls -l dist/routerpanel | awk '{print $5}') bytes"
fi

# 服务在跑就先停：一是避免 ETXTBSY，二是让旧进程走 defer 把 fbcon 挂回控制台
$SSH '/etc/init.d/routerpanel stop 2>/dev/null; true'

$SSH 'mkdir -p /etc/routerpanel /etc/config'

# 先落 /tmp 再原子改名：直接覆盖正在运行的二进制会被 ETXTBSY 拒绝
$SCP dist/routerpanel "$HOST:/tmp/routerpanel.new"
for f in /tmp/fonthout/SHS-Light.ttf /tmp/fonthout/SHS-Normal.ttf /tmp/fonthout/SHS-Bold.ttf; do
  [ -f "$f" ] && $SCP "$f" "$HOST:/etc/routerpanel/$(basename "$f")" || true
done
$SCP files/init.d-routerpanel "$HOST:/tmp/init.d-routerpanel"
$SCP files/routerpanel.uci "$HOST:/tmp/routerpanel.uci"
$SCP files/luci/controller/routerpanel.lua "$HOST:/tmp/luci-controller.lua"
$SCP files/luci/model/cbi/routerpanel.lua "$HOST:/tmp/luci-cbi.lua"
$SCP files/luci/view/routerpanel/status.htm "$HOST:/tmp/luci-status.htm"

$SSH 'mv -f /tmp/routerpanel.new /etc/routerpanel/routerpanel
chmod +x /etc/routerpanel/routerpanel
mv -f /tmp/init.d-routerpanel /etc/init.d/routerpanel
chmod +x /etc/init.d/routerpanel
# uci 配置只在第一次铺，别把用户后来改的 enabled/interval 覆盖掉
[ -f /etc/config/routerpanel ] || { mv -f /tmp/routerpanel.uci /etc/config/routerpanel; }
rm -f /tmp/routerpanel.uci
# 新加的两个开关：只在没有的时候补默认值，不覆盖他选过的状态
[ -n "$(uci -q get routerpanel.main.screen)" ] || uci -q set routerpanel.main.screen=1
[ -n "$(uci -q get routerpanel.main.status_bar)" ] || uci -q set routerpanel.main.status_bar=black
uci -q commit routerpanel

# iStoreOS「服务」里的那个页面：Lua 控制器 + CBI 模型 + 状态模板，
# 和 fastnet/openclash 一个路子。改完要清 LuCI 的编译缓存，否则菜单还是旧的
mkdir -p /usr/lib/lua/luci/view/routerpanel
cp /tmp/luci-controller.lua /usr/lib/lua/luci/controller/routerpanel.lua
cp /tmp/luci-cbi.lua /usr/lib/lua/luci/model/cbi/routerpanel.lua
cp /tmp/luci-status.htm /usr/lib/lua/luci/view/routerpanel/status.htm
rm -f /tmp/luci-controller.lua /tmp/luci-cbi.lua /tmp/luci-status.htm
rm -f /tmp/luci-indexcache* 2>/dev/null
rm -rf /tmp/luci-modulecache 2>/dev/null
if [ "$(uci -q get routerpanel.main.enabled)" = "1" ]; then
  /etc/init.d/routerpanel start; sleep 1
fi
/etc/init.d/routerpanel status 2>/dev/null || true
ls -l /etc/routerpanel'
