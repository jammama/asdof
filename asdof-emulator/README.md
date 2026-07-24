# asdof-emulator

브라우저에서 도는 **GB / GBC / GBA 에뮬레이터**. 롬을 올리면 바로 실행되고,
세이브는 브라우저(IndexedDB)에 남는다. 폰·데스크톱 모두 지원.

- **코어**: [`@thenick775/mgba-wasm`](https://www.npmjs.com/package/@thenick775/mgba-wasm) — mGBA를 WebAssembly로 컴파일한 것. GB/GBC/GBA를 한 코어가 모두 처리한다. (롬은 변환 없이 데이터로 그대로 먹인다.)
- **프론트**: 순수 HTML/CSS/JS (빌드 스텝 없음). 코어만 wasm.
- **저장**: 코어 내장 IDBFS(IndexedDX 파일시스템)에 롬·세이브·상태저장이 영속화됨.

## 로컬 실행

```bash
./serve.py          # http://localhost:8090
```

> ⚠️ `python3 -m http.server` 로는 **안 된다.** 이 코어는 스레드(SharedArrayBuffer)를
> 써서 **cross-origin isolation**(COOP/COEP 헤더)이 필요한데, `serve.py` 가 그 헤더를
> 붙여 준다. 헤더가 없으면 로딩 화면에서 "실행할 수 없어요" 가 뜬다.

## 롬 넣는 법

두 가지 방법이 있고, 둘 다 결국 브라우저 라이브러리(IndexedDB)에 들어간다.

1. **직접 업로드** — 앱 화면의 "＋ 롬 파일 올리기" 로 `.gba/.gbc/.gb/.zip` 선택.
2. **서버 선반** — 내 서버에 롬을 올려두고 어느 기기서든 불러오기. (개인용)
   - `roms/` 폴더에 롬 파일을 넣고,
   - `roms/roms.json` 에 항목을 추가:
     ```json
     {
       "roms": [
         { "name": "포켓몬스터 골드.gbc", "file": "pkm-gold.gbc", "system": "GBC" },
         { "name": "harvest-moon-fomt.gba", "file": "hm-fomt.gba", "system": "GBA" }
       ]
     }
     ```
   - `name` = 화면 표시용(및 로컬 저장 파일명), `file` = `roms/` 안 실제 파일명.
   - 앱의 "서버 선반" 에 "가져오기" 버튼이 뜨고, 누르면 로컬 라이브러리로 복사된다.

## 조작

- **폰**: 화면 위 가상 패드 (방향 / A · B / L · R / START · SELECT).
- **데스크톱**: 방향키 · `X`=A · `Z`=B · `A`=L · `S`=R · `Enter`=Start · `Backspace`=Select.
- HUD: 상태저장/불러오기(슬롯1) · 빨리감기(≫) · 전체화면(⛶) · 목록으로.
- 배터리 세이브(게임 내 "저장/리포트")는 자동으로 IndexedDB에 영속화된다
  (플레이 중 주기적 + 뒤로가기/탭 숨김 시). 마지막 지점 자동 이어하기도 지원.

## 배포 (emulator.asdof.xyz)

```bash
./deploy.sh              # core/js/css/index.html + roms/ 전체 배포
./deploy.sh --no-roms    # 앱 코드만 (롬 제외)
./deploy.sh --dry-run    # 올릴 목록만 확인
```

- 서버 nginx vhost 는 `nginx-emulator.conf` 참고 (COOP/COEP 헤더 필수).
  최초 1회만 배치하면 된다:
  ```bash
  sudo cp nginx-emulator.conf /etc/nginx/sites-available/emulator.conf
  sudo ln -s /etc/nginx/sites-available/emulator.conf /etc/nginx/sites-enabled/
  sudo nginx -t && sudo systemctl reload nginx
  sudo certbot --nginx -d emulator.asdof.xyz
  ```

## 저작권

에뮬레이터 자체는 합법이다. 롬은 **본인이 합법적으로 소유한 것을 본인 개인용으로만**
사용할 것. 서버 선반(`roms/`)은 **비공개(인증 뒤)** 로 두고 남이 받아가지 못하게 해야 한다
— 공개 배포는 저작권 침해다. (`.gitignore` 가 롬 바이너리를 커밋에서 제외한다.)

## 구조

```
index.html            앱 셸 (화면 + HUD + 가상패드 + 라이브러리)
css/style.css         스타일 (반응형 + 터치 패드 레이아웃)
js/
  engine.js           코어 로드/초기화, FSSync 영속화
  library.js          롬 목록/업로드/삭제 + 서버 선반
  player.js           실행/상태저장/빨리감기/일시정지
  touch.js            가상 게임패드 (멀티터치)
  app.js              화면 전환 + UI 연결
core/
  mgba.js, mgba.wasm  mGBA wasm 코어 (vendored)
roms/roms.json        서버 선반 매니페스트 (롬 파일은 커밋 안 함)
serve.py              로컬 dev 서버 (COOP/COEP 헤더)
deploy.sh             scp 기반 배포
nginx-emulator.conf   서버 vhost 예시
```
