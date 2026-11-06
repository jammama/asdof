# trackbot 절차 (2026-10-19 규칙 — 모든 일의 기준은 노션)

대시보드: https://trackbot.asdof.xyz/ (비밀번호: `../.knowledge/issues.env` 의 ADMIN_PASSWORD)
화면 상단: **[할일 | 메일 | 첨부파일 리스트 | 대응 내용 정리]**

## 흐름 한눈에

| 단계 | 누가 | 무엇을 | 결과 |
|---|---|---|---|
| ① 메일 수신 | cron | trackbot 폴더 새 메일을 요약해 메일 탭에 올림 (메일#번호) | 신규요청 메일 = **확인필요** |
| ② 노션 등록 | 사용자 요청 시 | 확인필요 메일을 나눠 노션 **신규요청** 으로 등록, 최근 한 달 이슈와 겹치면 **재요청** | 메일 카드에 '노션 등록 #…' |
| ③ 진행 결정 | 사용자 | 노션 내용 확인 후 상태를 **진행 중** 으로 | — |
| ④ 할일 정리 | cron | 진행 중 이슈를 할일 탭 **수정필요** 로 (웹·서버=에이전트 프롬프트, 앱=앱 개발자 요약) | 할일 카드 |
| ⑤ 테스트 확인 | 사용자 요청 시 | 푸시된 코드(android·interface·front·back)로 구현 여부 판단 → 노션 **테스트 중** | 할일이 **수정완료** 로 이력 탭 |

- 노션: "외부공유" 페이지(https://app.notion.com/p/3300ae7cbd8d8015b507d620c963f063) 안의 **PMS_OP_QA** DB, 통합 토큰(`.knowledge/issues.env` NOTION_TOKEN). MCP 사용 안 함.
- 노션 상태: 재요청/신규요청/요청확인/내용미비/백로그/미해결/이슈/진행 중/검토 중/테스트 중/요청 취소/해결
- **노션 쓰기(등록·재요청·상태 변경)는 사용자가 요청할 때만.** cron 은 노션을 읽기만 한다.
- 코드 확인은 읽기 전용 — 수정·커밋 금지.

## cron (30분) — ① 메일, ④ 할일

### ① 메일 (Outlook "trackbot" 폴더)

- 접근: Microsoft 365 MCP (`outlook_email_search` folderName="trackbot", `read_resource`). **읽기 전용** — 답장·초안·전달·라벨·삭제 도구 금지, 다른 폴더 검색 금지. 원문은 올리지 않는다(요약만).
1. `python3 trackbot.py mail-known` → 올린 메일 id 목록·기준일·최근 메일(메일#번호·제목).
2. `outlook_email_search(folderName="trackbot", limit=25, offset=0)` → `internetMessageId` 가 known 에 없고 수신일 ≥ since 인 것만 신규. 없으면 끝.
3. 신규는 `read_resource` 로 본문, 첨부 pptx/docx/pdf/xlsx 도 `attachments[].uri` 로 읽어 **내용을 확인**한다. 캡처 이미지는 텍스트로 안 나오니 이미지 의존 항목은 '이미지 미확인' 으로 적는다.
4. **모든 메일을 올린다.** 메일 1통 = 카드 1개 (요청 항목이 여러 개여도 한 카드에 요약).
   | verdict | 기준 |
   |---|---|
   | `request` | 신규요청 — 개발·수정·자료 요청이 담김 → 화면 **확인필요** |
   | `reply` | 회신·답변 — 우리 문의·보고에 대한 고객 답변, 또는 우리가 답해야 할 질문 |
   | `info` | 참고 — 공유·안내·감사·동료 전달 메모 |
   - **같은 메일의 재전달**(FW 로 같은 본문·첨부를 다시 보냄)이면 새 내용이 없으므로 `dup_of: "<원 메일 internetMessageId>"` 로 올린다 → 화면에 따로 뜨지 않고 원 메일에 '재전달 n건' 표시. 전달자가 **새 의견·질문을 붙였으면** 재전달이 아니라 `reply`/`info` 로 올린다.
   - `summary`: 한두 줄. `analysis`: 요청 항목을 번호 목록으로 (출처 슬라이드·페이지, 기한, 우선순위). 노션 등록 때 이걸 나눈다.
   - `issue_nos`: 메일이 언급하는 기존 노션 번호 (분명할 때만).
   - `actions`: 회신이 필요하면 그대로 복사할 회신 문구 (person). 없으면 비움.
5. scratchpad 에 `{"note": "...", "mails": [{id, subject, from, received_at, web_link, attachments, issue_nos, verdict, priority, summary, analysis, actions, dup_of}]}` → `python3 trackbot.py mail-sync <파일>`. 메일#번호는 서버가 매긴다.

### ④ 할일 (노션 '진행 중' → 수정필요)

1. `python3 trackbot.py scan` → `candidates`. `reason`: `progress` 새로 진행 중이 됨 / `changed` 진행 중 이슈의 본문·댓글이 바뀜. 대시보드에 있는 건 상태가 자동 반영되고, 노션이 '테스트 중'·'해결' 이 되면 서버가 **수정완료**(이력), 그 밖의 상태(재요청·요청 취소 등)가 되면 **보관**(이력)으로 옮긴다 (`stage_changed`).
2. 후보마다 `IMG_DIR=<scratchpad>/img python3 trackbot.py detail <id>` 로 본문·댓글·첨부를 읽는다. **댓글 첨부 이미지는 반드시 Read 로 연다.** 의미 없는 변경(오탈자 등)은 `python3 trackbot.py ack <id>`.
3. 코드를 확인해 무엇을 어디서 고쳐야 하는지 정리한다 (추측 금지, `evidence` 에 `파일:라인`/커밋).
   - 백 `pms-back`, 프론트 `PMS_front`, 인터페이스 `pms-interface`, 앱 `pms-android` (모두 `/Users/shlee02911/WebstormProjects/` 아래)
   - `target`: `web`(PMS_front) | `server`(pms-back·pms-interface) | `app`(pms-android) | `mixed`(여럿)
4. `actions` — 그대로 복사해 쓰는 완성본
   - 웹·서버 → `kind: prompt`, `to: "Claude Code (pms-back)"` 등 **에이전트에게 전달할 프롬프트**: 노션 #번호·요청 요약, 원인 파일·라인, 현재/기대 동작, 수정 범위, 백엔드면 "단위 테스트 작성 + ./gradlew test", 권한 정책이 걸리면 "먼저 사용자에게 질문", "커밋 금지".
   - 앱 → `kind: person`, `to: "앱 개발자 (상훈 팀장)"` **변경사항 요약**. 앱 개발자도 노션을 보므로 **노션에 없는 시스템 내용만** — 사용할 API·요청/응답 필드·코드값·서버 변경 여부·호출 순서. 업무용 개조식(첫 줄 `#번호 요약`, 아래 `- `, 명사형 종결 '~ 요청', '~ 확인 필요', '필요 시 요청 바람'). 노션 내용 반복·인사 금지.
   - mixed 면 둘 다.
5. **예산 규칙** — 1건 약 2분 / 도구 12회를 넘길 것 같으면 멈추고 analysis 에 "파악한 것 / 막힌 지점 / 볼 곳" 을 적어 올린다 (actions 는 파악한 범위까지).
6. scratchpad 에 `{"note": "...", "issues": [{"id", "stage": "fix", "target", "priority": "high|mid|low", "summary": "한 줄", "analysis": "무엇을 고칠지 정리", "evidence": [...], "actions": [...]}]}` → `python3 trackbot.py sync <파일>`. 노션 메타는 sync 가 scan 값으로 채운다.

cron 보고: 변경이 없으면 "변경 없음" 한 줄. 있으면 새 메일(메일#번호 제목)·새 수정필요(#번호)·수정완료로 옮겨진 건을 한두 줄로.

## 사용자 요청 시

### ② "확인필요 노션에 올려줘" — 메일 → 노션 신규요청/재요청

1. 대상: 메일 탭 확인필요(`request` 이고 노션 미등록) 메일. 사용자가 메일#번호를 지정하면 그것만.
2. 메일 본문·첨부를 다시 읽어 **요청 항목 단위로 나눈다** (슬라이드 1장 = 보통 1건).
3. `python3 trackbot.py recent` (변경 일시 최근 30일 이슈) 로 **연계되거나 동일한 이슈**가 있는지 대조한다. 애매하면 `detail` 로 본문 확인.
4. 겹치지 않으면 → `create` 로 신규요청: `[{mail_no, title, menu(선택 속성), types(유형), priority(높음|보통|낮음), due, source, path(화면 경로), content:[이슈/요청 내용 줄...]}]`. 요청자=이승혜, 본문 '화면 경로 / 이슈/요청 내용 / 해결안', 출처에 `메일#번호` 포함.
5. 연계·동일하면 → `rerequest` 로 재요청: `[{no, mail_no, date:"MM/DD", reason:"재등록 연유", content:[새 요청 내용 줄...]}]`. 상태가 '재요청' 이 되고 '이슈/요청 내용' 위에 `이슈/요청 내용 ({날짜} 재요청)` 제목 + 사유 + 내용이 들어간다.
6. 두 명령 모두 메일 카드에 등록한 노션 번호를 자동으로 붙인다(확인필요에서 빠짐). 등록 전 나눈 목록(신규/재요청 구분, 대상 #번호)을 사용자에게 보여 주고 확인받는다.

### ⑤ "진행 중 테스트해줘" — 구현 여부 확인 → 테스트 중

1. 대상: 할일 수정필요(노션 진행 중) 이슈. 사용자가 #번호를 지정하면 그것만.
2. **푸시된 코드만** 본다: 4개 저장소(`pms-android`, `pms-interface`, `PMS_front`, `pms-back`)에서 `git fetch -q` 후 원격 브랜치(`origin/develop` 등)에 들어간 커밋·코드로 요청 항목별 구현 여부를 판단한다. 로컬 미푸시 변경은 구현으로 치지 않는다.
3. **DB 데이터(양식·코드값 등)가 걸린 항목**은 마이그레이션 파일만으로 판단하지 않는다 — 운영 반영 여부는 사용자가 직접 추출해 주는 경우가 있으니 **반드시 사용자에게 확인**한다.
4. 코드 이상의 검증(실행·API 호출·DB 조회 등)이 필요하면 무엇을 왜 하는지 설명하고 **동의를 받은 뒤** 진행한다.
5. 구현 완료로 판단되면 근거(저장소·커밋·파일)를 보여 주고 `python3 trackbot.py set-status <번호> 테스트 중` → 서버가 할일을 **수정완료** 로 이력에 남긴다. 미완료 항목은 무엇이 남았는지 보고만 한다.

## 첨부파일 리스트 (화면 상단 [첨부파일 리스트] 탭)

- 이슈 본문이 `출처: ○○.pptx` 등으로 인용한 요청 문서 + 메일 첨부(서버 mails)를 파일명으로 합쳐 보여 준다. 원본 위치(메일/드라이브/링크)를 못 찾은 문서는 '위치 미확인'(빨간 표시).
- 갱신: `python3 trackbot.py attach-sync` (노션 본문을 훑어 인용 문서와 이슈 번호를 서버에 올림. 신규 요청 PPT 이슈가 늘었을 때 실행).
- 직접 등록(드라이브 링크 등): `POST /api/attachment-sync` (`{"attachments":[{id,name,kind,refs,where,link,note}]}`, X-Sync-Token). 같은 id 면 갱신, `remove:true` 면 삭제. 자동 집계가 지우지 않는다.
- 탭 분류: **요청사항 / 기획검토 / 내용정리 / 기타**. 화면이 파일명 규칙(`fileCategory`)으로 나눈다 — 요청사항(요청사항·회의자료·기능 변경) → 기획검토(설계·검토·기획·워크플로우·보고서·재검증) → 내용정리(정리·가이드·기준) → 나머지 기타. 규칙과 다르게 둘 파일은 attachment-sync 항목에 `category: request|plan|summary|etc` 로 직접 지정한다 (자동 집계가 지우지 않음).
- 서버 코드: `attach.go`, 화면: `static/index.html` renderFiles. 배포는 `./deploy.sh`.

## 일일 정리 문서 ('{날짜} pms요구사항 해결 문의 내용 정리.docx')

- **사용자가 요청할 때만** 만든다 (예: '대응 내용 정리 만들어줘', '오늘 문서 정리'). 정기 cron 없음. 날짜당 1개, 같은 날 다시 만들면 덮어쓴다.
- 대상: 노션 상태 **'테스트 중' → [개발완료]**, **'요청확인' → [문의]**. 한 페이지에 이슈 하나, 각각 1부터 번호.
- 페이지 구성: 제목 `[개발완료] N. {이슈}` / 표(노션참조번호·구분·출처·요청일) / 1. 기존 요청 내용(+스크린샷) / 2. 작업·대응 내용 (문의는 2. 문의 내용 및 상세 설명 + 우리 측 제안 캡처).
- 요약 페이지 '참고 — [개발확인중] 항목'(노션 '진행 중'·'신규요청'·'재요청', content.json `progress`+`new`)과 '참고 — [검토중] 항목': 노션 '검토 중' 이슈를 사진 없이 한 페이지에 `[검토중] N. {이슈} (노션참조번호)` + 1. 요청사항(출처) / 2. 보류 사유(출처) 한 줄씩. content.json `hold`.
- 절차
  1. `python3 report.py collect` → `report/out/<날짜>/collected.json` + `content.json`(없을 때만 우리 측 최근 댓글로 초안)
  2. Claude 가 `content.json` 을 다듬는다 — 근거는 노션 댓글·대시보드 판정·원격 커밋. **고객사에 보내는 문서라 SQL·커밋·코드 위치·내부 담당자 판단·'테스트중/요청확인' 같은 상태명은 쓰지 않는다.** 보고 문서라 '~했습니다' 체로 사실만 쓰고, '확인 후 해결 처리 부탁'·'회신 부탁'·'검토 부탁' 같은 요청 문구는 쓰지 않는다 (문의는 무엇을 문의했는지만). 근거가 없는 작업은 지어내지 말고 사용자에게 확인.
  3. `python3 report.py build` → docx + 메일 제목·본문(`.mail.json`, 복붙용 — 보고 말투, [개발완료]·[문의] 제목 목록). 바꿀 땐 content.json 에 `"mail": {subject, intro}`
  4. `python3 report.py upload` → 서버 `/var/lib/asdof-issues/files/` 에 올리고 다운로드 링크 출력 (`https://trackbot.asdof.xyz/files/<파일명>?pw=<ADMIN_PASSWORD>` — 비밀번호 없으면 401)
  5. 대시보드 상단 **'대응 내용 정리'** 탭에 목록이 뜬다 — 미리보기(새 탭, 브라우저에서 docx 렌더) / 다운로드 / 메일 제목·본문 복사 (메일 작성·첨부는 사용자가 직접)
- 확장 필드 (2026-10-20~, content.json): `cover_list`(표지 목차 줄), `reqtable`({title, note, rows: [요청일자, 대상, 목록, 요청내용, 요청기한, 완료여부, 노션, 실제 구분]} — 메일로 받은 요구사항 표를 그대로 옮기고 완료여부·웹/앱/서버 정정 표시, '※'로 시작하면 구분 정정 강조), `explain`([{no, title, detail, table{head,rows}, after}] → [내용정리] 페이지), `schedule`([[no, 구분, 상태, 예정일]] + `schedule_note` → '남은 항목 개발 일정' 표, 있으면 [개발확인중]/[검토중] 요약 대신 사용), done 항목 `title`(노션 제목에 내부 표기가 있을 때 고객사용 제목).
- 내부용 정리 문서 (2026-10-30~, 예: 운영 배포 내용 정리): `report/out/<날짜>/*.json` 을 `{title, subtitle, blocks:[{h}|{p}|{li,lv}|{table}]}` 로 쓰고 `node report/note.js <json> "report/out/<날짜>/<날짜> <제목>.docx"` → `python3 report.py upload <날짜>` 가 그 폴더의 docx 를 모두 올린다.
- 운영 반영 내역 (2026-10-23~, 요청 시): 완료(해결) 항목만 고객사가 '무엇이 바뀌었나' 보는 문서. `report/out/<날짜>/release.json`(구역별 summary·before·after) 작성 → `python3 report.py release <날짜>` → `{날짜} 운영 반영 내역.docx` (요약표 + 구역별 변경 전/후). 근거 없는 '변경 전'은 쓰지 않는다. `upload` 가 함께 올린다.
- 회의 문서 (2026-10-16~, 요청 시): `python3 report.py meeting` → `{회의일} 회의 내 문의 및 안건 정리.docx`. [문의](ask)·[안건](hold) 을 상세 페이지(요청 원문+본문 사진 / 문의 내용·현황 및 회의 확인 사항)로만 묶는다. content.json 에 `"meeting": {"date": "YYYY-MM-DD"}`, hold 항목에 `detail` 배열. `upload` 가 정리 문서와 함께 올린다 (메일 본문 없음).
- 의존성: `report/` 에서 `npm install` (docx), 이미지 크기는 macOS `sips` 사용.
