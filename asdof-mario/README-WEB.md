# asdof-mario — Super Mario 64 웹(WASM) 포트

Super Mario 64 디컴파일(`sm64-port`) 소스를 **Emscripten**으로 빌드해
브라우저(WebGL2 + Web Audio)에서 실행되는 WebAssembly 로 만드는 프로젝트입니다.

**워크플로우: 게임 소스 수정 → 빌드 → 웹 배포** 에 필요한 파일만 담겨 있습니다.

---

## 1. 필수 툴체인 (한 번만 설치)

macOS 기준:

```bash
brew install emscripten            # emcc — WASM 컴파일러
brew install make                  # GNU Make 4.x (gmake) — 기본 3.81은 '!=' 미지원
brew install x86_64-elf-binutils   # 사운드 시퀀스(.s) 어셈블용 GNU as / objcopy
```

> 이 세 가지는 **파일이 아니라 시스템 도구**라 폴더에 포함하지 않습니다. 위 명령으로 설치하세요.

## 2. 빌드

```bash
./build-web.sh
```

내부적으로 실행되는 명령:

```bash
gmake TARGET_WEB=1 AS=x86_64-elf-as OBJCOPY=x86_64-elf-objcopy -j<코어수>
```

결과물:

```
build/us_web/sm64.us.html   # 로더 페이지
build/us_web/sm64.us.js     # glue 코드
build/us_web/sm64.us.wasm   # 게임 + 전체 에셋 (약 13MB, 별도 데이터 파일 없음)
```

## 3. 로컬 실행 / 배포 미리보기

```bash
./serve-web.sh          # 기본 8080 포트
# 브라우저에서:  http://localhost:8080/sm64.us.html
```

WASM 은 `file://` 로 열 수 없으므로 반드시 HTTP 서버가 필요합니다.

## 4. 웹 배포

`build/us_web/` 안의 세 파일(`.html`, `.js`, `.wasm`)을 **정적 호스팅**에 그대로 올리면 됩니다
(GitHub Pages, Netlify, S3, nginx 등). 서버 설정 팁:

- `.wasm` 은 `application/wasm` MIME 로 서빙 (대부분 자동)
- 캐시가 크므로 gzip/brotli 압축 권장 (13MB → 수 MB)

---

## 게임 내용 수정 위치

| 수정 대상 | 경로 |
|---|---|
| 게임 로직 (마리오 동작, 물리, 카메라 등) | `src/game/` |
| 엔진 (충돌, 지오/레벨 스크립트, 수학) | `src/engine/` |
| 레벨 데이터 / 지오메트리 / 스크립트 | `levels/<레벨>/` |
| 액터(오브젝트) 모델·동작 | `actors/`, `src/game/behaviors/` |
| 오디오 | `src/audio/`, `sound/` |
| 텍스트/대사 | `text/`, `charmap.txt` |
| 웹/PC 호환 레이어 (렌더러·입력·오디오 백엔드) | `src/pc/` |

수정 후 `./build-web.sh` 만 다시 실행하면 됩니다. (변경분만 증분 빌드)

### 기본 키보드 조작 (sm64-port 기본값)
- 이동: 방향키 / WASD 계열 (config 파일에서 변경 가능)
- A(점프): 스페이스 등, B: 다른 키 — 첫 실행 시 생성되는 `build/us_web/sm64config.txt` 에서 매핑 확인·수정
- 게임패드(Gamepad API)도 지원

---

## 이 포트에서 표준 sm64-port 대비 변경된 점 (macOS + 최신 Emscripten 대응)

`Makefile` 에 다음 수정이 적용되어 있습니다:

1. **`-march=native` 제거 (웹 타겟)** — wasm 컴파일에서 무효한 플래그
2. **웹 링커 플래그 현대화** — `TOTAL_MEMORY=20MB` → `INITIAL_MEMORY=64MB` + `ALLOW_MEMORY_GROWTH`,
   `EXTRA_EXPORTED_RUNTIME_METHODS` → `EXPORTED_RUNTIME_METHODS`, deprecated `-g4`/`--source-map-base` 제거
3. **`CXX := em++` 설정 (웹 타겟)** — C++ 파일(그래픽 백엔드 등)이 네이티브 clang++ 대신 em++ 로 컴파일/링크되도록
4. **`AS`/`OBJCOPY` 를 GNU binutils 로** — macOS 기본 LLVM `as` 는 사운드 데이터(`.s`)의 GNU `--defsym`/디렉티브 미지원

> ⚠️ **저작권**: `baserom.us.z64` 는 에셋 추출용 원본 롬입니다. 사용자가 합법적으로 소유한 사본이며
> 재배포 대상이 아닙니다. 빌드 결과물(`.wasm`)에도 닌텐도의 저작권 에셋이 포함되므로 **공개 배포에 주의하세요.**
