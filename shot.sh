#!/bin/bash
# 路由器上渲染一帧 -> 拉回本地看图。全程不碰 /dev/fb0。
set -e
# 目标机器和 SSH 私钥从环境或同目录的 .deploy.conf 读，仓库里不预置任何人的地址。
# .deploy.conf 是 gitignore 的，本地写一行 PANEL_HOST=root@192.0.2.1 就能用。
CONF="$(dirname "$0")/.deploy.conf"
[ -f "$CONF" ] && . "$CONF"
: "${PANEL_HOST:?没有路由器地址：export PANEL_HOST=root@<地址>，或写进 .deploy.conf}"
: "${PANEL_KEY:?没有 SSH 私钥：export PANEL_KEY=$HOME/.ssh/id_ed25519，或写进 .deploy.conf}"
KEY="$PANEL_KEY"; HOST="$PANEL_HOST"
SSH="ssh -i $KEY -o IdentitiesOnly=yes -o BatchMode=yes"
$SSH "$HOST" "/etc/routerpanel/routerpanel -png /tmp/panel.png $*" 2>&1 | tail -3
scp -q -i "$KEY" -o IdentitiesOnly=yes "$HOST:/tmp/panel.png" ./shot/panel-$(date +%H%M%S).png 2>/dev/null || true
ls -t shot/*.png | head -1
