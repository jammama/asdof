# planbot 설계 (v0.1 초안)

> 상태: 설계 검토용 초안 (2026-10-07). 구현 전 — 아래 "결정 필요" 항목 확정 후 착수.
> 원형: `../asdof-issues` (trackbot.asdof.xyz, PMS 노션·메일 트리아지). 그 경험을 다른 사람이 자기 프로젝트에 쓸 수 있게 일반화한다.

## 1. 한 줄 정의

각자 자기 Claude Code 로 업무 소스(노션·메일·이슈 트래커 등)를 읽어 정리하면, 그 결과를 **planbot.asdof.xyz** 에 올려 웹에서 확인·체크하는 서비스.
서버는 **기록과 화면만** 담당한다. Claude 실행·소스 연결·비용은 사용자 몫.

## 2. 목표 / 비목표

**목표**
- 정리 결과를 4가지 출력으로 분류해 보여준다
  | 타입 | 화면 이름 | 의미 | 예 |
  |---|---|---|---|
  | `client` | 고객 전달 | 고객사·요청자에게 보낼 문구 | "반영했습니다, 확인 후 해결 처리 부탁드립니다" |
  | `me` | 내 할일 | 내가 직접 해야 하는 일 (짧은 한 줄) | "장애 사진 장수 규칙 수정" |
  | `agent` | 에이전트 지시 | 그대로 붙여넣을 AI 프롬프트 | "Claude Code(pms-back): …단위 테스트 작성 + 전체 테스트" |
  | `share` | 공유 | 내부 동료·다른 담당자에게 알릴 내용 | "앱 개발자: 10/6 앱 요청 16건" |
- 모든 출력은 **ref** 로 출처(원천 항목 카드 → 원문 링크)를 가리킨다
- 사용자는 md 프롬프트 + 단일 파일 CLI 만 받으면 시작 가능. 연결할 소스는 **Claude 가 첫 실행 때 사용자에게 묻는다**
- 사용자 20명 고정 (관리자가 발급한 고정 ID)

**비목표 (v1)**
- 서버에서 Claude/LLM 실행, 서버가 소스(노션·메일)에 직접 접속
- 회원가입·결제·팀 공동편집·실시간 알림
- 원문 저장 (요약·판정·문구만 저장 — trackbot 과 동일 원칙)

## 3. 전체 구조

```
사용자 PC                                          planbot.asdof.xyz (OCI)
┌──────────────────────────────────────┐          ┌─────────────────────────────┐
│ Claude Code (사용자 계정·비용)          │          │ nginx (TLS) → planbot :8810  │
│  ├ planbot/SETUP.md   첫 실행 인터뷰    │          │  ├ /api/*   통신 규격 (§6)    │
│  ├ planbot/TRIAGE.md  회차 절차(생성됨) │  HTTPS   │  ├ /        웹 화면 (§7)      │
│  ├ planbot/config.json 연결 설정(생성됨)│ ───────▶ │  └ data/    사용자별 저장소    │
│  └ planbot.py  push/pull CLI          │  토큰     │      u01.json … u20.json     │
│ 소스 접근: MCP 커넥터·API 토큰 (사용자)  │          └─────────────────────────────┘
└──────────────────────────────────────┘
```

- 서버: Go 단일 바이너리 + 정적 HTML (trackbot 과 동일 스택, systemd + nginx + certbot)
- 저장: **사용자별 JSON 파일 1개** (`data/<userId>.json`, 임시파일+rename 원자 교체). 20명 규모라 DB 불필요. 사용자 간 격리가 파일 단위로 보장됨
- 클라이언트: `planbot.py` — Python 표준 라이브러리만 (설치 없음). Claude 가 호출

## 4. 사용자·인증

- 관리자가 `u01`~`u20` 고정 ID 를 발급 (`admin.json` 또는 CLI `planbot-admin issue u03 --name 홍길동`)
- ID 마다 두 비밀값:
  | 비밀값 | 용도 | 보관 |
  |---|---|---|
  | 화면 비밀번호 | 웹 로그인 (`id + password`) | 서버엔 bcrypt 해시만 |
  | 동기화 토큰 | Claude(CLI) → API 쓰기 | 서버엔 SHA-256 해시만, 사용자 PC `~/.planbot/credentials` (0600) |
- 사용자당 **프로젝트 여러 개** (상한 5) — PMS, 다른 고객사 등 분리. 프로젝트 = 화면의 상단 선택기
- 토큰은 그 사용자 데이터에만 쓰기 가능. 관리자도 사용자 데이터 열람 화면 없음 (서버 파일 접근은 운영자 권한 — 고지 필요, §9)
- 웹 세션: 로그인 시 HttpOnly·Secure 쿠키 (trackbot 의 localStorage 비밀번호 방식 대신)

## 5. 데이터 모델

```jsonc
// data/u03.json
{
  "user": {"id": "u03", "name": "홍길동"},
  "projects": [{
    "id": "pms",                       // 사용자가 정한 slug
    "name": "PMS 유지보수",
    "people": {"client": ["황호성"], "internal": ["변성용"], "agent_targets": ["Claude Code (pms-back)"]},
    "sources": [                       // SETUP 이 기록 (화면 표시·설명용, 비밀값 없음)
      {"key": "notion-qa", "kind": "notion", "label": "PMS_OP_QA DB"},
      {"key": "mail", "kind": "outlook", "label": "trackbot 폴더"}
    ],
    "last_sync": "…", "last_note": "이번 회차 한 줄 요약",

    "items": [{                        // 원천 항목 = 소스 1건 (이슈·메일·티켓·PPT 슬라이드…)
      "id": "notion-qa:3f20…",         // <source key>:<원천 id> — 안정 키
      "source": "notion-qa",
      "ref_no": "#168", "title": "web 요청사항 1006 일괄", "url": "https://…",
      "author": "황호성", "created_at": "…", "status": "진행 중",
      "revision": "…",                 // 바뀌면 확인 체크 해제 (trackbot 규칙)
      "verdict": "fix", "priority": "high",
      "summary": "한 줄", "analysis": "판단 근거", "evidence": ["src/…:123 — …"],
      "checked": false, "reopened": false, "reopen_reason": "", "history": [{"at": "…", "text": "…"}]
    }],

    "outputs": [{                      // 4분류 결과 — 화면의 주 탭
      "id": "168-photo-count",         // Claude 가 정하는 안정 키 (같은 일 = 같은 id)
      "type": "me",                    // client | me | agent | share
      "to": "",                        // client/share/agent 의 대상 (예: "황호성", "앱 개발자")
      "text": "장애 사진 장수 규칙 수정",   // me 는 한 줄, 나머지는 복사할 본문
      "refs": ["notion-qa:3f20…", "mail:<msgid>"],
      "created_at": "…", "done": false, "done_at": ""
    }]
  }]
}
```

- `verdict` 값은 프로젝트가 정한다 (기본: `fix` `external` `insufficient` `invalid` `testing` `review` `resolved`). 화면은 값 목록을 데이터에서 읽어 칩을 만든다
- `done`/`checked` 는 **사람만** 바꾼다. Claude 는 같은 id 로 문구만 갱신하거나 `remove` 한다

## 6. 통신 규격 (API)

모든 응답 JSON. 쓰기는 `Authorization: Bearer <동기화 토큰>`, 화면은 세션 쿠키.

| 메서드 | 경로 | 인증 | 설명 |
|---|---|---|---|
| POST | `/api/login` | - | `{id, password}` → 세션 쿠키 |
| GET | `/api/projects` | 세션·토큰 | 내 프로젝트 목록 |
| PUT | `/api/projects/{pid}` | 토큰 | 프로젝트 생성·설정 갱신 (`name, people, sources, verdicts`) — SETUP 이 호출 |
| GET | `/api/projects/{pid}` | 세션·토큰 | 전체 상태 (items + outputs). CLI 의 `pull` |
| POST | `/api/projects/{pid}/sync` | 토큰 | 회차 결과 일괄 반영 (아래) |
| PUT | `/api/projects/{pid}/check` | 세션 | `{kind: item|output, id, value}` 확인·완료 체크 |
| GET | `/api/projects/{pid}/export.md` | 세션·토큰 | 마크다운 보관본 |

`POST /sync` 본문:
```jsonc
{
  "note": "이번 회차 한 줄 요약",
  "items":   [{ /* §5 item, 판정 포함 */ }, {"id": "…", "meta_only": true, "status": "…", "revision": "…"}],
  "outputs": [{ /* §5 output */ }, {"id": "…", "remove": true}],
  "reopen":  [{"item": "…", "reason": "#170에서 재발 보고"}]
}
```
- 서버 규칙 (trackbot 에서 검증된 것 그대로): revision 이 바뀌면 체크 해제 + "다시 확인", `meta_only` 는 판정 유지, 새 item 이 기존 item 을 ref 로 가리키면 그 item 도 다시 확인
- 제한: 요청 5MB, 문자열 길이 상한(제목 300, 본문 8000), 프로젝트당 item 2,000·output 1,000 (초과 시 오래된 완료 항목부터 보관 처리)
- `planbot.py` 명령: `login` (토큰 저장) / `pull` / `sync FILE` / `outputs` (현재 출력 목록) / `check-known SOURCE` (이미 올린 원천 id 목록 — 메일처럼 revision 없는 소스용)

## 7. 웹 화면

- 상단: 프로젝트 선택 · 마지막 동기화 · 회차 요약(note)
- 주 탭 (출력 4분류 + 원천):
  | 탭 | 카드 내용 | 동작 |
  |---|---|---|
  | 내 할일 | 체크박스 + 한 줄 + ref 칩 | 완료 체크 |
  | 고객 전달 | 대상 · 문구 + 복사 버튼 + ref 칩 | 복사, 보냄 체크 |
  | 에이전트 지시 | 대상 에이전트 · 프롬프트 + 복사 + ref 칩 | 복사, 실행함 체크 |
  | 공유 | 대상 · 문구 + 복사 + ref 칩 | 복사, 공유함 체크 |
  | 원천 | 소스별 필터 칩 · 판정 배지 · 판단 근거 · 원문 링크 · 관련 출력 | 확인 체크 |
- ref 칩 → 원천 카드로 이동·펼침 (trackbot 할일 탭과 동일)
- 하위 탭: 남은 것 / 완료 / 전체. 모바일 폭 대응. 1분 주기 조용한 새로고침
- 화면 문구·배지 색은 trackbot 디자인 재사용

## 8. 사용자 패키지 (md 프롬프트)

배포물: `planbot-kit.zip` = `SETUP.md`, `TRIAGE.template.md`, `planbot.py`, `README.md`.
사용자는 압축을 프로젝트 폴더에 풀고 Claude Code 에 **"planbot SETUP.md 대로 설정해줘"** 라고 말한다.

### SETUP.md — Claude 가 사용자에게 묻는 것 (AskUserQuestion)
1. planbot ID·동기화 토큰 → `planbot.py login`
2. 프로젝트 이름·slug (기존 프로젝트면 선택)
3. **연결할 소스** (복수 선택) — 각 소스마다 접근 방법을 확인·시험 후 등록
   | 소스 | 접근 | 시험 |
   |---|---|---|
   | 노션 DB | 노션 통합 토큰(해당 DB만 공유) 또는 노션 MCP | DB 1행 조회 |
   | Outlook / Gmail 폴더 | claude.ai Microsoft 365 / Gmail 커넥터 (`/mcp`) | 폴더 최신 1건 |
   | Jira / Linear / GitHub Issues | MCP 또는 API 토큰 | 이슈 1건 |
   | Slack 채널 | MCP | 메시지 1건 |
   | 로컬 폴더 (PPT·문서 드롭) | 파일 시스템 | 파일 목록 |
   - 범위는 **최소 권한**으로: 특정 DB·폴더·채널만. 사용자가 원하지 않는 전체 권한은 요구하지 않는다
4. 사람: 고객사 요청자 / 내부 담당자 / 에이전트 대상(코드 저장소 경로 포함)
5. 판정 기준: 기본 verdict 세트 사용 여부, 코드 대조 여부(저장소 경로·원격 브랜치)
6. 주기: 30분/1시간/수동 — 주기 실행 등록 (Claude Code 세션 cron 은 세션 한정·7일 만료임을 안내)
7. 결과: `config.json` + 소스별 절차가 채워진 `TRIAGE.md` 생성, `PUT /projects/{pid}` 로 설정 등록, 첫 회차 실행

### TRIAGE.template.md — 회차 절차 (소스 무관 공통부)
1. 소스별 변경 후보 수집 (revision 비교 / `check-known` 대조)
2. 후보마다 원문·첨부(이미지·PPT 포함)를 읽고 판정 — **읽기 전용**
3. 출력 4분류 생성 규칙
   - `me`: 한 줄 20자 안팎, 동사로 끝, 설명 금지 (근거는 원천 카드에)
   - `client`: 정중한 문구, 요청자 지정, 이미 답한 내용 반복 금지
   - `agent`: 자급자족 프롬프트 (원인 위치·재현·기대 동작·범위·테스트), 정책 결정이 걸리면 "먼저 사용자에게 질문" 포함
   - `share`: 대상 + 알릴 사실만
   - 모든 출력 refs 필수
4. 예산 규칙: 원천 1건 약 2분/도구 12회 초과 시 `review` 로 올리고 중단
5. `planbot.py sync` 로 전송, 변경 없으면 "변경 없음" 한 줄

## 9. 보안·운영

- 저장 데이터는 **요약·판정·문구** (원문·첨부 미저장). 그래도 고객사 정보가 포함되므로 사용자에게 고지 + 동의 문구를 SETUP 첫 단계에 표시
- 전송 HTTPS 만, `X-Robots-Tag: noindex`, 로그인 실패 rate limit (ID당 분당 5회), 토큰 재발급(`planbot-admin rotate u03`)
- 백업: `data/` 일 1회 tar (7일 보관)
- 사용자 삭제: `planbot-admin remove u03` → 파일 삭제
- 배포: trackbot 과 같은 OCI 서버, systemd `asdof-planbot`, 포트 8810, nginx vhost `planbot.asdof.xyz` + certbot (DNS A 레코드 추가 필요)

## 10. trackbot 에서 가져올 교훈 (TRIAGE 템플릿에 기본 포함)

- 댓글·첨부 **이미지를 반드시 열어 본다** (안 보면 오판)
- 소스 API 일시 오류(500·타임아웃·빈 목록)는 재시도·재확인 후 판단
- 재확인 기준 = 원천 변경 일시. 상태만 바뀐 건 판정 없이 meta 동기화
- "해결" 은 원천 상태로만, 코드 완료는 `testing` + 안내 필요 표시
- macOS 한글 파일명 NFD → 비교 전 NFC 정규화
- 셸 heredoc 은 `<<'EOF'` (백틱 실행 방지)

## 11. 단계 계획

| 단계 | 내용 | 산출물 |
|---|---|---|
| M1 | 서버 API + 사용자 파일 저장 + 관리자 CLI + 단위 테스트 | `main.go` 외, `planbot-admin` |
| M2 | 웹 화면 (로그인·프로젝트·5탭·ref 이동) | `static/` |
| M3 | 키트: `planbot.py`, `SETUP.md`, `TRIAGE.template.md` | `kit/` |
| M4 | 내 PMS 를 첫 프로젝트로 시범 (trackbot 과 병행 1주) | 비교 메모 |
| M5 | 사용자 2~3명 베타 → 20명 | 피드백 반영 |

## 12. 결정 필요

1. 프로젝트 상한(사용자당 5)과 항목 상한이 적절한지
2. 관리자(나)의 사용자 데이터 열람 화면 — 없음(현재 안) vs 지원용 읽기 전용
3. 출력 4분류 외 추가 타입 필요 여부 (예: `decision` 결정 필요 사항)
4. 원천 카드에 원문 링크 외 **원문 일부 저장** 허용 여부 (현재 안: 미저장)
5. 키트 배포 방식 — zip 다운로드 vs GitHub 공개 저장소
