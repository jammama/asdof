#!/usr/bin/env python3
# asdof-emulator — 로컬 개발 서버
#
# mGBA wasm 코어는 스레드(SharedArrayBuffer)를 쓰므로 cross-origin isolation 이
# 필요하다. 이 서버는 COOP/COEP 헤더 + 올바른 wasm MIME 를 붙여 준다.
#   (`python3 -m http.server` 로는 코어가 안 뜬다)
#
# 또한 /api/ 요청은 로컬 save-server(기본 127.0.0.1:8760)로 프록시한다.
# → 배포(nginx /api/ 프록시)와 동일하게 "같은 오리진"에서 세이브 API 를 테스트 가능.
#   save-server 미기동 시 /api/ 는 502 를 돌려준다(정적 서빙엔 영향 없음).
#
# 사용법:  ./serve.py [포트]      (기본 8090)
import http.server
import os
import socketserver
import sys
import urllib.error
import urllib.request

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8090
API_TARGET = os.environ.get("SAVE_SERVER", "http://127.0.0.1:8760")


class Handler(http.server.SimpleHTTPRequestHandler):
    extensions_map = {
        **http.server.SimpleHTTPRequestHandler.extensions_map,
        ".js": "text/javascript",
        ".mjs": "text/javascript",
        ".wasm": "application/wasm",
        ".json": "application/json",
    }

    def _iso_headers(self):
        self.send_header("Cross-Origin-Opener-Policy", "same-origin")
        self.send_header("Cross-Origin-Embedder-Policy", "require-corp")
        self.send_header("Cross-Origin-Resource-Policy", "same-origin")

    # 정적 응답에만 isolation + no-store 를 붙인다 (/api/ 는 _proxy 가 직접 처리)
    def end_headers(self):
        if not self.path.startswith("/api/"):
            self._iso_headers()
            self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def _proxy(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        body = self.rfile.read(length) if length else None
        req = urllib.request.Request(API_TARGET + self.path, data=body, method=self.command)
        for h in ("X-Token", "X-Origin-Name", "Content-Type"):
            v = self.headers.get(h)
            if v:
                req.add_header(h, v)
        try:
            with urllib.request.urlopen(req) as resp:
                data = resp.read()
                self.send_response(resp.status)
                for h in ("Content-Type", "X-Origin-Name"):
                    v = resp.headers.get(h)
                    if v:
                        self.send_header(h, v)
                self._iso_headers()
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
        except urllib.error.HTTPError as e:
            data = e.read()
            self.send_response(e.code)
            self._iso_headers()
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except urllib.error.URLError:
            msg = b"save-server not running (SAVE_SERVER=%b)" % API_TARGET.encode()
            self.send_response(502)
            self._iso_headers()
            self.send_header("Content-Length", str(len(msg)))
            self.end_headers()
            self.wfile.write(msg)

    def do_GET(self):
        if self.path.startswith("/api/"):
            return self._proxy()
        return super().do_GET()

    def do_POST(self):
        if self.path.startswith("/api/"):
            return self._proxy()
        self.send_error(405)

    def do_PUT(self):
        if self.path.startswith("/api/"):
            return self._proxy()
        self.send_error(405)

    def do_DELETE(self):
        if self.path.startswith("/api/"):
            return self._proxy()
        self.send_error(405)


if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        print(f"열기:  http://localhost:{PORT}/   (종료: Ctrl+C)")
        print("cross-origin isolation: COOP/COEP 적용됨 ✅")
        print(f"/api/ → {API_TARGET} 프록시 (save-server 별도 기동 필요)")
        httpd.serve_forever()
