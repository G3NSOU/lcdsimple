#!/bin/sh
# 在 iPad（iOS 9.3.5，越狱）上以 root 跑的一次性配置脚本。
# 用法（从 Mac 走 USB 或局域网）：
#	sh ipad/install.sh "<Mac 的 id_ed25519.pub 内容>" "http://<路由器地址>:8788/"
#
# 现状：这台设备的 sshd 现在连 banner 都发不出来（OpenSSH 客户端和 paramiko 两种实现
# 都在 kex 之前被 reset，说明是设备侧 sshd 起不来，不是算法协商问题）。
# 所以这个脚本要等 Cydia 里把 OpenSSH 重装一次之后才跑得动——重装会补上
# /private/etc/ssh/ssh_host_* 这些 host key，sshd 才肯起来。
# 脚本本身对"已经修好"和"还没修好"都安全：缺什么补什么，全部幂等。

set -e
PUBKEY="$1"
PANEL="$2"   # 面板地址必须自己给：这里不留默认值，因为每家的地址都不一样
SSHDIR=/private/etc/ssh
LA=/Library/LaunchDaemons

say() { echo "[routerpanel] $*"; }

# 1) host key：sshd 没有一把可用密钥就直接退出，表现就是"连接被 reset"
if [ ! -f "$SSHDIR/ssh_host_rsa_key" ]; then
  say "补 ssh_host_rsa_key"
  /usr/bin/ssh-keygen -q -t rsa -b 2048 -f "$SSHDIR/ssh_host_rsa_key" -N '' -C routerpanel
else
  say "ssh_host_rsa_key 已存在"
fi
chmod 600 "$SSHDIR"/ssh_host_*_key 2>/dev/null || true

# 2) 免密：只授权 Mac 那一把公钥，不放开密码登录
if [ -n "$PUBKEY" ]; then
  mkdir -p /var/root/.ssh
  chmod 700 /var/root/.ssh
  if ! grep -qF "$PUBKEY" /var/root/.ssh/authorized_keys 2>/dev/null; then
    echo "$PUBKEY" >>/var/root/.ssh/authorized_keys
  fi
  chmod 600 /var/root/.ssh/authorized_keys
  say "已写入 authorized_keys"
fi

# 3) 面板地址落到配置文件，看门狗脚本里不留任何人的 IP
if [ -n "$PANEL" ]; then
  echo "URL=\"$PANEL\"" > /etc/panel-watch.conf
  say "面板地址已写入 /etc/panel-watch.conf"
fi

# 4) 看门狗：Safari 被杀掉就重新打开面板页
install -m 755 panel-watch.sh /usr/local/bin/panel-watch.sh 2>/dev/null || {
  mkdir -p /usr/local/bin; cp panel-watch.sh /usr/local/bin/panel-watch.sh; chmod 755 /usr/local/bin/panel-watch.sh;
}
cp com.routerpanel.panelwatch.plist "$LA/com.routerpanel.panelwatch.plist" 2>/dev/null || true
chmod 644 "$LA/com.routerpanel.panelwatch.plist"
launchctl unload "$LA/com.routerpanel.panelwatch.plist" 2>/dev/null || true
launchctl load "$LA/com.routerpanel.panelwatch.plist"
say "看门狗已加载（每 30 秒检查一次，日志在 /tmp/panel-watch.log）"

# 5) 让 sshd 用新密钥重开
if [ -f "$LA/com.openssh.sshd.plist" ]; then
  launchctl unload "$LA/com.openssh.sshd.plist" 2>/dev/null || true
  launchctl load "$LA/com.openssh.sshd.plist"
  say "sshd 已重载"
fi

say "面板地址：${PANEL:-未填（看门狗会等着 /etc/panel-watch.conf）}"
say "完成"
