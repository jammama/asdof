# 노션 이슈 트리아지 절차 (30분 주기 Claude 작업)

대시보드: https://trackbot.asdof.xyz/ (비밀번호: `../.knowledge/issues.env` 의 ADMIN_PASSWORD)

## 대상

- 노션: "외부공유" 페이지(https://app.notion.com/p/3300ae7cbd8d8015b507d620c963f063) 안의 **PMS_OP_QA** DB
- 접근: 노션 내부 통합 토큰(읽기 전용, `.knowledge/issues.env` 의 NOTION_TOKEN). MCP 사용 안 함
- 판정 대상: **생성 일시 2026-09-01 이후 & 노션 상태 ≠ '해결'** (요청자 무관) + 이미 대시보드에 있는 건
- 노션 상태: 신규요청/내용미비/백로그/미해결/이슈/진행 중/검토 중/테스트 중/요청 취소/해결
  - '테스트 중' 도 완료가 아니다 — 요청자가 이견을 달았을 수 있으니 댓글까지 본다.

## 재확인(체크 해제) 기준 = 노션 변경 일시

- revision = `<변경 일시>|c<댓글 수>`. 대시보드에 체크된 이슈라도 revision 이 바뀌면 자동으로 미체크 + "다시 확인".
- 상태·속성만 바뀐 경우: scan 이 판정 없이 meta 만 동기화 (체크 해제, 판정 유지) — Claude 할 일 없음.
- 제목·본문·댓글이 바뀐 경우: `reason: changed` 후보 → 재판정.

## 매 회차 절차

1. `python3 trackbot.py scan` → 판정 대상(`candidates`) JSON.
   - `reason`: `new` 신규 / `changed` 내용 변경 / `unresolved` 노션에서 '해결' 이 풀림(재발 의심).
   - 후보가 없으면 이번 회차는 끝.
2. `IMG_DIR=<scratchpad>/img python3 trackbot.py detail <id>...` 로 본문·댓글·첨부를 읽는다. 첨부 이미지는 Read 로 직접 본다.
   - **댓글 첨부 이미지가 핵심인 경우가 많다** (요청자가 위치·현상을 캡처에 빨간 표시로 지정). `[댓글 첨부 …]` 로 표시된 이미지는 판정 전에 반드시 Read 로 연다.
     텍스트에 '요청드린 위치', '첨부', '위와 같이' 등이 있으면 해당 댓글·직전 댓글의 첨부를 확인하기 전엔 내용 미비로 판정하지 않는다.
3. changed/unresolved 인 건은 무엇이 바뀌었는지 `reopen_reason` 에 한 줄로 적는다
   (예: "작성자 댓글: 오늘 오전에도 동일 증상 재발"). 새 이슈가 기존 이슈와 **같은 증상**이면
   기존 이슈에도 `reopen: true, reopen_reason: "#<새 번호>에서 재발 보고"` 를 보낸다.
   의미 없는 변경(오탈자 등)은 `python3 trackbot.py ack <id>` 로 재판정 없이 넘긴다.
4. 판정 — 코드를 직접 확인한다 (추측 금지, 근거는 `evidence` 에 `파일:라인` 또는 커밋 해시).
   - 백엔드 `/Users/shlee02911/WebstormProjects/pms-back`, 프론트 `/Users/shlee02911/WebstormProjects/PMS_front`
   - 완료 여부: 관련 커밋이 있고 **원격에 푸시됐는지** (`git fetch -q; git branch -r --contains <hash>`), 커밋 일자가 요청/마지막 요청 댓글 이후인지.
   - 노션 댓글의 내부 담당자(이승혜·변성용 등) 답변 맥락을 반영한다 (이미 답한 내용 반복 금지).
   - **읽기 전용**: 코드 수정·커밋·DB 쓰기 금지. 판정만 한다.

   | verdict | 기준 |
   |---|---|
   | `fix` | 백엔드/웹 프론트 코드 수정이 필요 |
   | `app` | 모바일 앱 쪽 원인/수정 필요 → 앱 개발자에게 전달 |
   | `insufficient` | 재현 조건·대상(충전소/충전기 번호, 계정, 시각, 화면) 부족 |
   | `invalid` | 정상 동작, 사용법 오해, 운영 데이터/설정 문제, 요구사항 외, 요청 취소 |
   | `testing` | **코드상 완료 + 푸시됨** — 노션이 '해결' 이 아니면 전부 여기 |
   | `review` | 확인필요 — 파악 비용 초과로 판정 보류 (아래 예산 규칙) |
   | (`resolved`) | 보내지 않는다. 노션 상태가 '해결' 이 되면 서버가 자동으로 해결됨으로 옮긴다 |

   - `notify_needed: true` — `testing` 인데 노션 댓글에 "수정 완료/반영했습니다/올려두었습니다/확인 부탁" 류
     **우리 측 안내 댓글이 (완료 커밋 이후에) 없을 때**. 대시보드 카드 왼쪽에 "전달 필요" 로 표시된다.
   - priority: `high`(현장 업무 차단·데이터 오류·보안), `mid`, `low`(문구·UI 사소).

5. **예산 규칙 (토큰 과다 방지)** — 이슈 1건 파악에 **약 2분 / 도구 호출 12회**를 넘길 것 같으면 즉시 멈추고
   `verdict: review` 로 올린다. analysis 에 "여기까지 파악한 것 / 막힌 지점 / 재확인 시 볼 곳" 을 적는다.
   사용자가 재확인을 요청하면 그때 예산 없이 깊게 본다. 서브에이전트에 맡길 때도 이 규칙을 프롬프트에 넣는다.
6. `actions` — 내가 **그대로 복사해서 쓸 문구** (verdict 별 최소 1개, review 는 생략 가능).
   - `fix` → `kind: prompt`, `to: "Claude Code (pms-back)"` 등. 원인 파일·라인, 재현 조건, 기대 동작, 수정 범위, "단위 테스트 작성 + ./gradlew test" 까지 들어간 자급자족 프롬프트. 권한 정책이 걸리면 "먼저 사용자에게 질문" 지시.
   - `app` → `kind: person`, `to: "앱 개발자"`. 현상·재현·서버 응답 근거(API/필드)·요청사항.
   - `insufficient` → `kind: person`, `to: "<요청자>"`. 정중한 한국어, 필요한 정보를 번호 목록으로.
   - `invalid` → `kind: person`, `to: "<요청자>"`. 정상 동작인 이유와 올바른 사용법. 방어적이지 않게.
   - `testing` → `kind: person`, `to: "<요청자>"`. 노션 댓글용 반영 완료 안내 ("@요청자 …반영했습니다. 확인 후 해결 처리 부탁드립니다").
7. scratchpad 에 `{"note": "<이번 회차 한 줄 요약>", "issues": [...]}` 를 쓰고 `python3 trackbot.py sync <파일>` 로 전송.
   노션 메타(no·title·url·author·상태·변경 일시·revision)는 sync 가 scan 값으로 자동으로 채운다.
8. 새로 `high` 가 생기거나 재발(unresolved/reopen)이 있으면 짧게 알린다.

## sync 페이로드 필드

```json
{
  "id": "<노션 page id>",
  "verdict": "fix|app|insufficient|invalid|testing|review", "priority": "high|mid|low",
  "notify_needed": false,
  "summary": "한 줄", "analysis": "판단 근거 (몇 문단)", "evidence": ["src/...:123 — 설명"],
  "actions": [{"kind": "prompt|person", "to": "", "label": "", "text": ""}],
  "reopen": false, "reopen_reason": ""
}
```

확인 체크 상태는 사람만 바꾼다.
