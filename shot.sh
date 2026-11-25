#!/bin/bash
# 路由器上渲染一帧 -> 拉回本地看图。全程不碰 /dev/fb0（强制 -no-fb）。
#
# 为什么要从 uci 把参数拼回来：服务的启动参数是 init.d 从 uci 读的，
# 手动跑一次只带 -png 的话，fnOS/出口这些开关全是空的，出图会退化成
# "未配置"那张卡——看着像 bug，其实是取数条件没给全。
# 出口探测这里不跟着 uci 关：出图要给 README 和视频用，需要真实出口那一格。
set -e
# 目标机器和 SSH 私钥从环境或同目录的 .deploy.conf 读，仓库里不预置任何人的地址。
# .deploy.conf 是 gitignore 的，本地写一行 PANEL_HOST=root@192.0.2.1 就能用。
CONF="$(dirname "$0")/.deploy.conf"
[ -f "$CONF" ] && . "$CONF"
: "${PANEL_HOST:?没有路由器地址：export PANEL_HOST=root@<地址>，或写进 .deploy.conf}"
: "${PANEL_KEY:?没有 SSH 私钥：export PANEL_KEY=$HOME/.ssh/id_ed25519，或写进 .deploy.conf}"
KEY="$PANEL_KEY"; HOST="$PANEL_HOST"

# 远端脚本从 stdin 进来，$* 作为参数跟在 -- 后面，避免引号在两层 shell 里碎掉
ssh -i "$KEY" -o IdentitiesOnly=yes -o BatchMode=yes "$HOST" \
  "sh -s -- $*" <<'REMOTE' | tail -4
set -e
F="-no-fb"
for k in title interval pixfmt font_light font_normal font_bold \
         fnos_host fnos_user fnos_key fnos_mon groups_file proxy \
         ipad_host ipad_user ipad_key status_bar; do
  v=$(uci -q get "routerpanel.@panel[0].$k" 2>/dev/null || true)
  [ -n "$v" ] || continue
  o=$(printf '%s' "$k" | tr '_' '-')
  F="$F -$o '$v'"
done
echo "flags: $F" | sed -E "s/(-fnos-user '[^']*')/-fnos-user <已填>/"
eval "/etc/routerpanel/routerpanel -png /tmp/panel.png $F" '"$@"'
REMOTE

out="./shot/panel-$(date +%H%M%S).png"
scp -q -i "$KEY" -o IdentitiesOnly=yes "$HOST:/tmp/panel.png" "$out"
echo "$out"
