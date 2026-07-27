#!/usr/bin/env python3
# asdof-emulator — 로컬 개발 서버
#
# mGBA wasm 코어는 스레드(SharedArrayBuffer)를 쓰므로 cross-origin isolation 이
# 필요하다. 이 서버는 COOP/COEP 헤더 + 올바른 wasm MIME 를 붙여 준다.
#   (`python3 -m http.server` 로는 코어가 안 뜬다)
#
# 서버 세이브는 별도 서비스(asdof-saves)를 CORS 로 직접 호출하므로 여기서 프록시하지
# 않는다. 로컬 테스트 시엔 asdof-saves 를 따로 띄우고(예: :8760), 앱 설정의 "서버 주소"에
# http://localhost:8760 을 넣으면 된다.
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
        self.send_header("Cache-Control", "no-store")
        super().end_headers()


if __name__ == "__main__":
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("", PORT), Handler) as httpd:
        print(f"열기:  http://localhost:{PORT}/   (종료: Ctrl+C)")
        print("cross-origin isolation: COOP/COEP 적용됨 ✅")
        httpd.serve_forever()
