#!/usr/bin/env python3
"""排版量尺：把线上页面搬到本地，注入一段量高脚本，再用 headless Chrome 按精确视口读回来。

为什么要有这个：改一行 CSS 就要 build+deploy 到路由器（约 40 秒），而"到底哪一排把页脚挤出屏幕"
是需要连试七八次的量。这里只做只读代理，页面数据仍然来自路由器，视口由 Chrome 决定，
量出来的 offsetHeight 和真机同一档。

用法：python3 tools/layout-harness/server.py [端口=8899]
     然后 curl http://127.0.0.1:8899/  或  chrome --window-size=1024,660 --dump-dom

已知限制：页面在 headless Chrome 里通过本代理取数会卡住（同样的请求 curl 和真浏览器都正常，
代理侧日志显示请求到了、回包也写出去了，但页面的 XHR 回调始终不来）。所以量排版优先用
页面自带的 ?m=1 直连路由器；本文件只在需要"改完 CSS 不部署就看"时用真浏览器（browser-use）。
"""
import http.server
import os
import socketserver
import sys
import urllib.request

UP = os.environ.get("PANEL_URL", "http://192.168.1.1:8788")  # 不预置任何人的地址
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
# 页面本体读本地文件：改完 CSS 直接刷新就行，不用 build+deploy 到路由器再等 40 秒
PAGE = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                     "../../web/index.html"))

# 关掉 EventSource：页面自己会退回 XHR 轮询。代理下 SSE 反复重连时 headless Chrome
# 里的轮询会跟着一起卡住，量排版不需要长连接，走轮询反而稳
PRE = "<script>window.EventSource = void 0;</script>"

# 注入量高脚本：把每一排、每一张卡的内容高度写进 #dbg，dump-dom 直接读
HOOK = """
<div id="dbg" style="position:fixed;left:0;bottom:0;font:10px monospace;color:#000;
 background:#fff;z-index:9999;max-width:100%;overflow:hidden"></div>
<script>
(function(){
 function h(e){ return e ? e.offsetHeight : -1; }
 function sh(e){ return e ? e.scrollHeight : -1; }
 function cards(sel){
   var out=[], n=document.querySelectorAll(sel);
   for (var i=0;i<n.length;i++){ out.push(sh(n[i].querySelector('.card'))); }
   return out.join('/');
 }
 function rows(){
   var g=document.querySelectorAll('.grid'), out=[];
   for (var i=0;i<g.length;i++){ out.push((g[i].className.replace('grid ',''))+':'+h(g[i])); }
   return out.join(' ');
 }
 function probe(){
   // 代理到底有没有把数据送到浏览器，先自己试一次，别靠猜
   try {
     var x = new XMLHttpRequest();
     x.open('GET', '/api/state?_=' + new Date().getTime(), true);
     x.onload = function(){ window.__probe = 'XHR ' + x.status + '/' + x.responseText.length; };
     x.onerror = function(){ window.__probe = 'XHR error'; };
     x.send();
   } catch (e) { window.__probe = 'XHR throw ' + e; }
 }
 function tick(){
   probe();
   var app=document.getElementById('app');
   if (!app) { return; }
   var r2=document.querySelector('.row2');
   var keep=r2 ? r2.style.flex : '';
   if (r2) { r2.style.webkitFlex='none'; r2.style.flex='none'; }
   var min=app.scrollHeight;
   if (r2) { r2.style.webkitFlex=''; r2.style.flex=''; if (keep) r2.style.flex=keep; }
   document.getElementById('dbg').textContent =
     'V=' + innerWidth + 'x' + innerHeight +
     ' BODY=' + document.body.scrollHeight + ' MINAPP=' + min +
     ' | ' + rows() +
     ' | r1=' + cards('.row1') + ' r2=' + cards('.row2') + ' r3=' + cards('.row3') +
     ' | ' + (window.__probe || 'probe-') +
     ' FIT=' + ((document.getElementById('fit') || {}).textContent || '-');
 }
 setInterval(tick, 600); tick();
})();
</script>
"""


class Handler(http.server.BaseHTTPRequestHandler):
    # HTTP/1.1：无长度的 SSE 必须自己写 chunked 分帧。之前用 HTTP/1.0 时 curl 能读到，
    # 但 headless Chrome 里页面的 XHR 会一直挂着——量排版量到的是空壳，比不量更糟
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def _page(self):
        with open(PAGE, "rb") as f:
            html = f.read().decode("utf-8", "replace")
        body = html.replace("<head>", "<head>" + PRE, 1).replace(
            "</body>", HOOK + "</body>").encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True

    def do_GET(self):
        print("GET %s" % self.path, flush=True)
        if self.path.split("?")[0] in ("/", "/index.html"):
            self._page()
            return
        stream = self.path.startswith("/api/stream")
        try:
            r = urllib.request.urlopen(UP + self.path, timeout=30)
        except Exception as e:
            self.send_response(502)
            self.send_header("Content-Length", "0")
            self.end_headers()
            print("upstream %s -> %s" % (self.path, e), flush=True)
            return
        self.send_response(r.status)
        for k in ("Content-Type", "Cache-Control"):
            if r.headers.get(k):
                self.send_header(k, r.headers[k])
        if not stream:
            body = r.read()
            r.close()
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            # 每个响应都断链：headless Chrome 复用 keep-alive 连接时，
            # 这个单线程 accept 的服务器会让页面的 XHR 永远收不到回包
            self.close_connection = True
            return
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        try:
            while True:
                chunk = r.read(1024)
                if not chunk:
                    break
                self.wfile.write(b"%x\r\n" % len(chunk) + chunk + b"\r\n")
                self.wfile.flush()
        except Exception:
            pass
        try:
            self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
        except Exception:
            pass
        r.close()
        self.close_connection = True


socketserver.TCPServer.allow_reuse_address = True
socketserver.ThreadingTCPServer.daemon_threads = True
# 线程化：SSE 会长期占住一条连接，单线程的话页面再请求 /api/meta 就死锁在这里
with socketserver.ThreadingTCPServer(("127.0.0.1", PORT), Handler) as httpd:
    print("layout harness on http://127.0.0.1:%d (upstream %s)" % (PORT, UP), flush=True)
    httpd.serve_forever()
