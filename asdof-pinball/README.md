# SpaceCadetPinball – Web (WebAssembly)

3D Pinball · Space Cadet (역분석 엔진 [k4zmu2a/SpaceCadetPinball])를 Emscripten으로
WebAssembly 빌드하는 독립 프로젝트. 브라우저에서 실행된다.

## 폴더 구성

```
CMakeLists.txt                빌드 설정 (EMSCRIPTEN 분기 포함)
SpaceCadetPinball/            게임 소스 전체
Platform/Emscripten/
  shell.html                 브라우저 페이지 셸 (캔버스 + 로딩 + 온스크린 컨트롤)
web/board.html               (선택) 경쟁 모드 실시간 순위판 페이지
webdata/                     게임 데이터: CADET.DAT + SOUND/ (직접 준비, 저작권 데이터)
  fonts/korean.ttf           한글 렌더용 폰트 (scripts 로 생성, gitignore)
scripts/make-korean-font.sh  한글 폰트 서브셋 생성
build.sh / serve.sh          빌드 / 로컬서버 헬퍼
→ 빌드하면 build-web/(스크래치), bin/(배포물) 생성
```

## 사전 준비 (최초 1회)

```sh
brew install emscripten cmake
```

`webdata/` 에 게임 데이터가 있어야 한다 (Full Tilt 기준):
```
webdata/CADET.DAT
webdata/SOUND/*.WAV, *.MID
```
(Windows 기본판을 쓸 경우 `PINBALL.DAT` + 사운드도 인식됨)

한글 UI가 필요하면 폰트를 한 번 생성한다 (webdata 는 gitignore 대상):
```sh
./scripts/make-korean-font.sh   # webdata/fonts/korean.ttf 생성 (네오둥근모, OFL)
```

## 수정 → 빌드 → 확인 루프

```sh
./build.sh            # 최초 configure + 빌드, 이후 증분 빌드 (보통 10~20초)
./serve.sh            # http://localhost:8724/asdof-pinball.html
```

## 배포

`bin/` 안의 정적 파일을 정적 호스트에 올리면 끝.
```
asdof-pinball.html  (index.html 로 rename 가능)
asdof-pinball.js
asdof-pinball.wasm
asdof-pinball.data
board.html          (경쟁 모드 순위판, 선택)
```
- 정적 호스팅만 있으면 됨 (Netlify/Cloudflare Pages/GitHub Pages/S3/nginx…).
- `.wasm` 은 `application/wasm` MIME 필요 (요즘 호스트는 자동).
- 스레드(SharedArrayBuffer) 미사용 → **COOP/COEP 특수 헤더 불필요**.
- `.js` 가 `.wasm`/`.data` 를 파일명으로 fetch → **같은 폴더에, 이름 유지**.

## 웹 포팅에서 바꾼 것 (원본 대비)

- `winmain.cpp`: 블로킹 `while` 게임 루프를 `MainLoopTick()` 으로 분리 →
  브라우저에선 `emscripten_set_main_loop` (requestAnimationFrame) 로 구동, 프레임 sleep 생략.
- `CMakeLists.txt`: `if(EMSCRIPTEN)` 분기 — SDL2/SDL2_mixer 포트 플래그, 데이터 preload, HTML 셸.
- `shell.html`: 화면 하단 **온스크린 컨트롤 바** 상시 노출(데스크톱·모바일 공통 — 플리퍼 Z·/,
  발사 Space, 틸트 X·.·↑, 새 게임 F2). 버튼이 해당 키의 합성 `KeyboardEvent`를 `window`에 발생시켜 SDL로 전달.
- **한글 렌더**: 웹 빌드는 번들 폰트(네오둥근모)를 ImGui 폰트로 사용(메뉴 16px / 인게임 텍스트박스 32px).
  언어 전환은 페이지 리로드로 처리(웹에선 인게임 재초기화가 불가).
- 네이티브(Windows/Linux/macOS) 빌드 경로는 그대로 유지.

## 경쟁 모드 (선택)

게임 페이지가 점수를 WebSocket 으로 보내면, 같은 방(room)에 접속한 사람들의 점수가
실시간 순위판(`web/board.html`)에 모인다. `?room=<코드>` 로 세션을 나눌 수 있다.

- **앱 쪽 동작**: `winmain.cpp` 의 `extern "C"` 브릿지로 점수/게임오버를 노출(CMake `EXPORTED_FUNCTIONS`),
  `shell.html` 이 폴링해 `wss://<host>/_ws` 로 전송하고 순위판을 렌더한다.
- **서버 · 배포는 이 저장소 범위 밖**이다. 중계/영속화 서버, 프로토콜, nginx 등 운영 구성은
  별도 WebSocket 서버가 담당한다(운영 문서 참고).

## 알려진 제약

- **MIDI 음악**: 브라우저에서는 사운드폰트(패치셋) 없이 무음일 수 있음. 효과음(WAV)은 정상.
- **하이스코어/설정**: 기본 MEMFS라 새로고침 시 초기화 (영속화하려면 IDBFS 연동 필요).
- **경쟁 순위판 닉네임**: 익명이라 사칭 가능(캐주얼 경쟁용).
- **폰트 커버리지**: 번들 폰트는 라틴+한글 위주 → 일본어/중국어 등 다른 비라틴 언어는 글리프 없음.
