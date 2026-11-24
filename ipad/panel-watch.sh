#!/bin/sh
# iPad mini 1 面板看门狗（iOS 9.3.5，越狱后由 root 跑）
#
# 只做一件事：Safari 不在了就把它拉回面板页。
# 为什么需要：iOS 9 没有 Service Worker、也不能被外部唤醒，网页自己"死不了"的前提是
# MobileSafari 进程还活着；被 jetsam 杀掉或崩了就只会回到桌面，屏幕一片黑等着人。
#
# 这里刻意不写死启动工具名：iOS 9 上能"从命令行开 URL"的是 Cydia 包 openurl /
# com.conradkramer.open / sbutils 之类，装的是哪一个不一定，所以按顺序探测，
# 全都没有就只记一行日志，绝不假装自己成功了。
# 面板地址从配置文件读，脚本里不预置任何人的内网 IP：
# /etc/panel-watch.conf 一行一条 URL=...，由 install.sh 写。
CONF=/etc/panel-watch.conf
LOG=/tmp/panel-watch.log
URL=""
if [ -f "$CONF" ]; then
  . "$CONF"
fi
if [ -z "$URL" ]; then
  echo "$(date '%H:%M:%S') $CONF 里没有 URL=，什么都不做" >>"$LOG"
  exit 0
fi

front() {
  pgrep -x MobileSafari >/dev/null 2>&1
}

open_url() {
  for t in /usr/bin/openurl /usr/bin/open /usr/bin/sbutils /usr/bin/uiopen; do
    [ -x "$t" ] || continue
    case "$t" in
      */sbutils) "$t" openurl "$URL" ;;
      *) "$t" "$URL" ;;
    esac
    return 0
  done
  return 1
}

if front; then
  exit 0
fi

if open_url; then
  echo "$(date '+%H:%M:%S') Safari 不在，已重新打开面板" >>"$LOG"
else
  echo "$(date '+%H:%M:%S') Safari 不在，但设备上没有 openurl/open/sbutils，无法自动拉起" >>"$LOG"
fi
exit 0
