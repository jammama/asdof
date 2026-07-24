#!/usr/bin/env python3
# asdof-emulator — 로컬 개발 서버
#
# mGBA wasm 코어는 스레드(SharedArrayBuffer)를 쓰므로 cross-origin isolation 이
# 필요하다. `python3 -m http.server` 는 COOP/COEP 헤더를 안 줘서 코어가 안 뜬다.
# 이 서버는 그 헤더 + 올바른 wasm MIME 타입을 붙여 준다.
#
# 사용법:  ./serve.py [포트]      (기본 8090)
import http.server
import socketserver
import sys

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8090


class Handler(http.server.SimpleHTTPRequestHandler):
    extensions_map = {
        **http.server.SimpleHTTPRequestHandler.extensions_map,
        ".js": "text/javascript",
        ".mjs": "text/javascript",
        ".wasm": "application/wasm",
        ".json": "application/json",
    }

    def end_headers(self):
        # cross-origin isolation (SharedArrayBuffer 활성화 조건)
        self.send_header("Cross-Origin-Opener-Policy", "same-origin")
        self.send_header("Cross-Origin-Embedder-Policy", "require-corp")
        self.send_header("Cross-Origin-Resource-Policy", "same-origin")
        # 개발 중 캐시 방지
        self.send_header("Cache-Control", "no-store")
        super().end_headers()


if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        print(f"열기:  http://localhost:{PORT}/   (종료: Ctrl+C)")
        print("cross-origin isolation: COOP/COEP 적용됨 ✅")
        httpd.serve_forever()
