# trackbot 재시작 가이드 ("챗봇 크롤링 재시작")

새 Claude 세션에서 사용자가 **"챗봇 크롤링 재시작"** 이라고 하면 이 문서대로 복구한다.
절차·규칙의 정본은 `TRIAGE.md` (2026-10-19 규칙 개편 — 모든 일의 기준은 노션).

## 1. 복구 절차

1. 상태 확인: `cd /Users/shlee02911/WebstormProjects/oci/asdof-issues && python3 trackbot.py scan`
   - 기준 revision 캐시는 `../.knowledge/trackbot-cache.json`, 할일·메일 데이터는 서버(trackbot.asdof.xyz)에 있으므로 세션이 바뀌어도 그대로 이어진다.
2. 30분 cron 등록 (CronCreate, `7,37 * * * *`, recurring) — 프롬프트는 아래 **cron 프롬프트** 그대로. 세션 한정·7일 만료.
   - 모델: cron 전용 세션에서 `/model sonnet` 으로 메인을 낮춘다 (정리는 Opus 서브에이전트가 담당). 같은 세션에서 다른 작업을 하면 그 작업도 Sonnet 으로 돌기 때문에 세션 분리 권장.
3. scan 후보·새 메일이 있으면 TRIAGE.md cron 절차대로 처리.

## 2. cron 프롬프트

```
[trackbot 정기 작업] /Users/shlee02911/WebstormProjects/oci/asdof-issues/TRIAGE.md 의 'cron (30분) — ① 메일, ④ 할일' 절차대로 한다. 모든 일의 기준은 노션이고 cron 은 노션을 읽기만 한다(노션 쓰기 금지). **모델 분리: 메인은 스크립트 실행(mail-known·메일 목록 대조·scan)과 sync 만 하고, 메일·이슈 정리는 1건당(후보가 많으면 2~3건 묶음) Agent(model: "opus") 서브에이전트로 위임한다 — 프롬프트에 TRIAGE.md 경로·해당 절·대상(메일 uri/internetMessageId 또는 노션 #ID·page id)·이미지 Read 필수·읽기 전용·예산 규칙을 넣고 JSON 항목만 돌려받아 메인이 모아서 sync 한다.** (1) 메일: `cd /Users/shlee02911/WebstormProjects/oci/asdof-issues && python3 trackbot.py mail-known` 과 Microsoft 365 MCP `outlook_email_search(folderName="trackbot", limit=25, offset=0)` 를 대조해 since 이후 미등록 메일만 `read_resource` 로 읽고(첨부 pptx·docx·pdf·xlsx 도 attachments uri 로 확인) 모든 메일을 1통=1카드로 요약(verdict request|reply|info, 같은 메일 재전달이면 dup_of)해 `python3 trackbot.py mail-sync <파일>`. (2) 할일: `python3 trackbot.py scan` 의 candidates(노션 '진행 중' 신규 progress / 내용 변경 changed)를 detail 로 읽고(댓글 첨부 이미지 반드시 Read) 4개 저장소(pms-back, PMS_front, pms-interface, pms-android) 코드와 대조해 stage=fix 할일로 정리 — 웹·서버는 에이전트 프롬프트, 앱은 앱 개발자용 시스템 변경사항(노션에 없는 API·필드·코드값)만 — `python3 trackbot.py sync <파일>`. 오탈자 등 무의미한 변경은 ack. 읽기 전용(코드 수정·커밋 금지), trackbot 폴더 외 메일 검색·답장·초안·전달 금지, 원문 미게시. **예산 규칙: 1건 약 2분/도구 12회 초과 예상 시 멈추고 analysis 에 막힌 지점을 적어 올린다(초과 시 TaskStop).** 셸 heredoc 은 'EOF' 따옴표. 변경이 없으면 "변경 없음" 한 줄, 있으면 새 메일(메일#번호 제목)·새 수정필요(#번호)·수정완료로 옮겨진 건(scan 의 stage_changed)을 한두 줄로 보고한다.
```

## 2-1. 사용자 요청 시 작업 (cron 아님)

- **"확인필요 노션에 올려줘"** → TRIAGE.md ② (메일 나누기 → `recent` 대조 → `create` 신규요청 / `rerequest` 재요청, 등록 전 목록 확인받기)
- **"진행 중 테스트해줘"** → TRIAGE.md ⑤ (푸시된 코드만, 4개 저장소, DB 항목은 사용자 확인, 코드 이상 검증은 동의 후 → `set-status <번호> 테스트 중`)
- **'대응 내용 정리 만들어줘'** → TRIAGE.md '일일 정리 문서' (collect → content.json 다듬기 → build → upload). 정기 cron 없음.

## 3. 구성 요약

| 항목 | 위치 |
|---|---|
| 대시보드 | https://trackbot.asdof.xyz (비번 asdof1234) — [할일(수정필요·이력) | 메일(확인필요·전체·이력) | 첨부파일 리스트 | 대응 내용 정리] |
| 서버 코드 | `main.go`(할일 단계), `mail.go`(메일#번호·노션 등록), `static/index.html`, 배포 `./deploy.sh` (OCI 161.118.133.112, systemd asdof-issues, :8800) |
| 노션 조회·쓰기 | `trackbot.py` (scan / detail / sync / ack / mail-* / recent / create / rerequest / set-status) |
| 절차 | `TRIAGE.md` |
| 비밀값 | `../.knowledge/issues.env` (ADMIN_PASSWORD, SYNC_TOKEN, NOTION_TOKEN) |
| 노션 대상 | "외부공유" 페이지 안 PMS_OP_QA DB (6f50ae7c-bd8d-8286-a3f3-817578575218) |
| 데이터 백업 | 서버 `/var/lib/asdof-issues/data.json.bak-20261019-v1` (규칙 개편 직전) |

## 4. 사용자 규칙 (2026-10-19 개편, 확정)

- 모든 일의 기준은 **노션**. cron 은 노션을 읽기만, 노션 쓰기는 사용자 요청 시에만.
- 메일 탭: **모든 메일**을 1통 = 1카드로 요약, 메일#번호. 신규요청(`request`)은 확인필요. 같은 메일 재전달은 다시 올리지 않음(`dup_of`).
- 확인필요 → 노션: 항목별로 나눠 신규요청, 최근 한 달(변경 일시) 이슈와 연계·동일하면 상태 '재요청' + '이슈/요청 내용 ({날짜} 재요청)' 추가.
- 노션 '진행 중' → 할일 수정필요 (웹·서버 = 에이전트 프롬프트, 앱 = 앱 개발자용 시스템 변경사항만 — 앱 개발자도 노션을 봄).
- 테스트 요청 시: 푸시된 코드(android·interface·front·back)만으로 판단 → '테스트 중' → 서버가 할일을 수정완료로 이력에. DB 항목은 사용자에게 확인, 코드 이상 검증은 설명·동의 후.
- 첨부파일 리스트·대응 내용 정리는 유지(대응 내용 정리에 접기 추가).
- 개편 전 데이터: 이슈 판정 → 할일 '이력'(보관), 메일 → 메일 '이력', 옛 할일 목록은 삭제.
- 확인 체크(`PUT /api/issues/{id}/check`)는 관리자 비밀번호 전용 — 사용자가 명시적으로 허락하기 전엔 비밀번호를 쓰지 않는다.
- '이승혜' = 우리 측(사용자 본인), hoseong.hwang = 고객사 요청자, 상훈 이 = 앱 개발자, 변성용 = 우리 측 동료.

## 5. 지금까지 겪은 함정

- 댓글 첨부 이미지를 안 보면 오판 (#120). detail 은 댓글 첨부까지 내려받으니 반드시 Read.
- 노션 API 가 가끔 500/타임아웃/댓글 0건을 돌려줌 → 재시도 로직 있음. 댓글이 세 번 연속 0건이면 실제 삭제로 본다(2026-10-19 #237).
- heredoc 에 백틱이 있으면 셸이 실행함 → `<<'EOF'` 사용.
- 첨부 다운로드가 많은 이슈(#168)는 detail 이 2~3분 걸림 → timeout 넉넉히.
- 노션 API 는 블록을 `after` 로만 끼울 수 있다 — `rerequest` 는 '이슈/요청 내용' 제목 바로 앞 블록 뒤에 넣는다 (제목이 첫 블록이면 실패).

## 6. 메일 연동

- Outlook 규칙으로 고객사 메일을 "trackbot" 폴더에 복사 → claude.ai Microsoft 365 MCP(`/mcp` 에서 연결, 관리자 동의 완료)로 그 폴더만 읽는다.
- 기준일 `trackbot.py` 의 `MAIL_SINCE = 2026-09-28`.
- MCP 가 세션에 안 잡히면 `/mcp` 에서 claude.ai Microsoft 365 재연결. 그래도 안 되면 메일 단계만 건너뛰고 할일은 계속.
