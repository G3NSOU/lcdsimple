#!/usr/bin/env python3
# 用一个本地假数据服务把"出口 IP 已暴露"这一帧喂给真浏览器看。
# 为什么需要它：这条告警只有在出口真的回到大陆境内才会出现，我没法为了截图
# 去动他的代理分流，所以把线上那一帧取回来、只改出口字段，其余保持真实。
import json, os, sys, time, urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

BASE = os.environ.get("PANEL_URL", "http://192.168.1.1:8788")  # 不预置任何人的地址
HTML = urllib.request.urlopen(BASE + "/", timeout=8).read().decode("utf-8")
STATE = json.loads(urllib.request.urlopen(BASE + "/api/state", timeout=8).read().decode("utf-8"))

eg = STATE["Clash"]["Egress"]
eg["IP"] = "117.136.38.21"
eg["CC"] = "CN"
eg["City"] = "Chongqing"
eg["ISP"] = "China Mobile / AS56040 China Mobile Communications"
eg["CN"] = True
eg["Exposure"] = 205
eg["Via"] = "本机直连（代理口未通）"

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _hdr(self, ctype):
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Cache-Control", "no-store")
        self.end_headers()

    def do_GET(self):
        p = self.path.split("?")[0]
        if p == "/api/state":
            self._hdr("application/json; charset=utf-8")
            self.wfile.write(json.dumps(STATE).encode())
        elif p == "/api/stream":
            self._hdr("text/event-stream; charset=utf-8")
            for _ in range(120):
                self.wfile.write(("data: %s\n\n" % json.dumps(STATE)).encode())
                self.wfile.flush()
                time.sleep(0.3)
        elif p == "/api/meta":
            self._hdr("application/json; charset=utf-8")
            self.wfile.write(b'{"control":false,"groups":[],"mode":"rule"}')
        elif p == "/api/ping":
            self.send_response(204)
            self.end_headers()
        else:
            self._hdr("text/html; charset=utf-8")
            self.wfile.write(HTML.encode())

if __name__ == "__main__":
    HTTPServer(("127.0.0.1", int(sys.argv[1] if len(sys.argv) > 1 else 8899)), H).serve_forever()
