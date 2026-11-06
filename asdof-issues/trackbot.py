#!/usr/bin/env python3
"""노션 PMS_OP_QA 이슈 ↔ trackbot 대시보드 연결 도구. 모든 일의 기준은 노션이다 (2026-10-19 규칙).

정기(cron):
  python3 trackbot.py scan            노션 '진행 중' 이슈 중 할일(수정필요)로 올릴 후보 출력. 대시보드에 있는 건 상태 자동 동기화
                                      (노션이 '테스트 중'·'해결' 이 되면 서버가 수정완료로 옮겨 이력에 남긴다)
  python3 trackbot.py detail ID...    본문·댓글·첨부를 텍스트로 출력, 이미지는 IMG_DIR 에 내려받음
  python3 trackbot.py sync FILE.json  {"note","issues":[{id,stage:"fix",target,summary,analysis,evidence,actions,priority}]} 전송
  python3 trackbot.py ack ID...       할일 내용 갱신 없이 현재 노션 내용을 기준으로 삼음 (무의미한 변경)
  python3 trackbot.py mail-known      이미 올린 메일 id(internetMessageId)·메일#번호·제목 + 기준일 출력
  python3 trackbot.py mail-sync FILE  {"note","mails":[...]} 전송 (메일#번호는 서버가 매김)

사용자 요청 시 (노션 쓰기):
  python3 trackbot.py recent [DAYS]   변경 일시가 최근 DAYS(기본 30)일 안인 노션 이슈 목록 — 재요청 대조용
  python3 trackbot.py create FILE     확인필요 메일 → 노션 신규요청 등록. [{mail_no,title,menu,types,priority,due,source,path,content}]
  python3 trackbot.py rerequest FILE  연계·동일 이슈 → 상태 '재요청' + '이슈/요청 내용 ({date} 재요청)' 추가. [{no,mail_no,date,reason,content}]
  python3 trackbot.py set-status NO 상태   노션 상태 변경 (예: 테스트 확인 후 '테스트 중') + 대시보드 즉시 반영
  python3 trackbot.py solution FILE      [{no,date,lines,status?}] → 본문 해결안 섹션 끝에 반영 내용 추가 (+상태 변경)
  python3 trackbot.py mail-register MAIL_NO NO...   메일#번호에 등록한 노션 번호 붙이기 (create/rerequest 가 자동으로 함)
  python3 trackbot.py attach-sync     이슈 본문이 '출처'로 인용한 문서(pptx·xlsx 등)를 모아 첨부파일 리스트로 전송

메일은 Claude 가 Microsoft 365 MCP 로 Outlook "trackbot" 폴더만 읽는다 (이 스크립트는 Graph 를 호출하지 않음).
노션은 통합 토큰으로 PMS_OP_QA DB 만 다룬다. 쓰기(create·rerequest·set-status)는 사용자가 요청할 때만 한다.
"""
import hashlib, json, os, pathlib, re, sys, time, urllib.error, urllib.request

HERE = pathlib.Path(__file__).resolve().parent
SECRETS = HERE.parent / ".knowledge/issues.env"
CACHE = HERE.parent / ".knowledge/trackbot-cache.json"
IMG_DIR = pathlib.Path(os.environ.get("IMG_DIR", "/tmp/trackbot-img"))

BASE = "https://trackbot.asdof.xyz"
DB_ID = "6f50ae7c-bd8d-8286-a3f3-817578575218"  # PMS_OP_QA
CREATED_SINCE = "2026-09-01"  # 이 날짜 이후 생성된 이슈만 신규 판정 (요청자 무관)
MAIL_SINCE = "2026-09-28"  # 이 날짜 이후 수신 메일만 판정 (그 이전은 연동 전 이력)
RESOLVED = "해결"
PROGRESS = "진행 중"  # 사용자가 노션 내용을 확인하고 진행을 정한 상태 → 할일 '수정필요'
DONE = {RESOLVED, "테스트 중"}  # 개발이 끝난 상태 — 서버가 할일을 수정완료로 옮긴다
REQUESTER = "이승혜"  # 우리 측이 등록하는 노션 이슈의 요청자

EMPTY_BODY = hashlib.sha1(b"").hexdigest()[:12]  # 본문 블록이 하나도 없는 페이지의 body_hash
_users = {}  # notion user id → 이름
env = dict(l.split("=", 1) for l in SECRETS.read_text().split())


# ---------- HTTP ----------

def _req(url, method="GET", body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    for attempt in range(4):
        req = urllib.request.Request(url, method=method, data=data, headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503, 504) and attempt < 3:  # 노션 rate limit (평균 3 req/s)·일시 장애
                time.sleep(float(e.headers.get("Retry-After", 1 + attempt * 2)))
                continue
            sys.exit(f"HTTP {e.code} {url}: {e.read().decode()[:300]}")
        except (TimeoutError, urllib.error.URLError, ConnectionError) as e:
            if attempt < 3:  # 읽기 타임아웃·일시 네트워크 오류는 재시도
                time.sleep(1 + attempt * 2)
                continue
            sys.exit(f"네트워크 오류 {url}: {e}")


def notion(path, method="GET", body=None):
    return _req("https://api.notion.com/v1/" + path, method, body,
                {"Authorization": "Bearer " + env["NOTION_TOKEN"], "Notion-Version": "2022-06-28"})


def board(path, method="GET", body=None):
    return _req(BASE + path, method, body, {"X-Sync-Token": env["SYNC_TOKEN"]})


def paged(path, method="GET", body=None):
    out, cursor = [], None
    while True:
        if method == "POST":
            b = dict(body or {}, page_size=100, **({"start_cursor": cursor} if cursor else {}))
            r = notion(path, "POST", b)
        else:
            sep = "&" if "?" in path else "?"
            r = notion(f"{path}{sep}page_size=100" + (f"&start_cursor={cursor}" if cursor else ""))
        out += r["results"]
        if not r.get("has_more"):
            return out
        cursor = r["next_cursor"]


# ---------- 노션 파싱 ----------

def plain(rt):
    return "".join(x.get("plain_text", "") for x in rt or [])


def prop(p):
    t = p["type"]; v = p[t]
    if t in ("title", "rich_text"): return plain(v)
    if t in ("select", "status"): return v["name"] if v else ""
    if t == "multi_select": return ",".join(x["name"] for x in v)
    if t == "people":
        for x in v:  # 댓글 작성자 이름 해석용 (통합에 사용자 정보 권한이 없어도 이름을 알 수 있게)
            if x.get("name"): _users[x["id"]] = x["name"]
        return ",".join(x.get("name", "?") for x in v)
    if t == "unique_id": return f'{v.get("prefix") or ""}{v["number"]}'
    if t == "date": return v["start"] if v else ""
    if t == "files": return [f.get("name", "") for f in v]
    return v


def row_meta(r):
    p = r["properties"]
    return {
        "id": r["id"], "no": prop(p["ID"]), "title": prop(p["이슈"]).strip(), "status": prop(p["상태"]),
        "requester": prop(p["요청자"]), "assignee": prop(p["담당자"]), "types": prop(p["유형"]),
        "menu": prop(p.get("분류") or p.get("선택") or {"type": "select", "select": None}),  # 노션 '선택' → '분류' (2026-10-23)
        "priority": prop(p["우선순위"]), "created": r["created_time"],
        "last_edited": r["last_edited_time"], "url": r["url"], "source": prop(p["출처"]), "due": prop(p["마감일"]),
    }


def user_name(u):
    uid = u.get("id")
    if u.get("name"): return u["name"]
    if uid not in _users:
        try: _users[uid] = notion(f"users/{uid}").get("name", uid[:8])
        except SystemExit: _users[uid] = uid[:8]
    return _users[uid]


def render_blocks(block_id, depth=0, files=None):
    """본문 블록을 들여쓰기 텍스트로. 첨부(file/image)는 files 에 모은다."""
    lines = []
    for b in paged(f"blocks/{block_id}/children"):
        t = b["type"]; v = b.get(t, {}); pad = "  " * depth
        if t in ("image", "file", "pdf", "video"):
            src = v.get(v.get("type"), {}).get("url", "")
            name = v.get("name") or plain(v.get("caption")) or t
            if files is not None: files.append({"type": t, "name": name, "url": src})
            lines.append(f"{pad}[{t}: {name}]")
        elif t == "table_row":
            lines.append(pad + " | ".join(plain(c) for c in v["cells"]))
        elif t == "child_page":
            lines.append(f"{pad}[하위 페이지: {v.get('title')}]")
        elif t == "child_database":
            lines.append(f"{pad}[하위 DB: {v.get('title')}]")
        else:
            text = plain(v.get("rich_text")) if isinstance(v, dict) else ""
            prefix = {"heading_1": "# ", "heading_2": "## ", "heading_3": "### ", "bulleted_list_item": "- ",
                      "numbered_list_item": "1. ", "to_do": "[x] " if v.get("checked") else "[ ] ", "quote": "> "}.get(t, "")
            if t == "code": text = "```\n" + text + "\n```"
            if text or prefix.startswith("#"): lines.append(pad + prefix + text)
        if b.get("has_children") and t not in ("child_page", "child_database"):
            lines += render_blocks(b["id"], depth + (0 if t in ("table", "synced_block", "column_list", "column") else 1), files)
    return lines


def comments(page_id):
    """댓글 + 댓글 첨부(이미지·파일). 고객은 위치 표시 캡처를 댓글에 붙이는 경우가 많다."""
    out = []
    for c in paged(f"comments?block_id={page_id}"):
        files = [{"type": a.get("category", "file"), "name": a["file"]["url"].split("?")[0].rsplit("/", 1)[-1], "url": a["file"]["url"]}
                 for a in (c.get("attachments") or []) if a.get("file", {}).get("url")]
        out.append({"at": c["created_time"], "by": user_name(c["created_by"]), "text": plain(c["rich_text"]), "files": files})
    return out


# ---------- 캐시 (기준 revision) ----------

def load_cache():
    return json.loads(CACHE.read_text()) if CACHE.exists() else {"pages": {}}


def save_cache(c):
    CACHE.write_text(json.dumps(c, ensure_ascii=False, indent=1))
    CACHE.chmod(0o600)


def fetch_state(meta, cache_entry):
    """revision(재확인 기준 = 변경 일시 + 댓글 수)과 content_hash(재판정 기준 = 제목·본문·댓글)를 계산한다.
    본문은 변경 일시가 바뀐 경우에만 다시 읽는다. 댓글은 매번 확인 (댓글은 변경 일시를 안 바꿀 수 있음)."""
    cmts = comments(meta["id"])
    prev_cnt = int(str((cache_entry or {}).get("comment_count", 0)))
    if not cmts and prev_cnt > 0:  # 노션 API 가 일시적으로 빈 목록을 주는 경우가 있어 한 번 더 확인
        time.sleep(2)
        cmts = comments(meta["id"])
        if not cmts:
            time.sleep(4)
            cmts = comments(meta["id"])
        if not cmts:  # 세 번 모두 0건이면 댓글이 실제로 삭제된 것으로 본다
            print(f"댓글 {prev_cnt}건 → 0건 (삭제로 판단): {meta['id']}", file=sys.stderr)
    if cache_entry and cache_entry.get("last_edited") == meta["last_edited"] and cache_entry.get("body_hash"):
        body_hash = cache_entry["body_hash"]
    else:
        body_hash = hashlib.sha1("\n".join(render_blocks(meta["id"])).encode()).hexdigest()[:12]
    content = hashlib.sha1(json.dumps([meta["title"], body_hash, [c["text"] for c in cmts], [len(c["files"]) for c in cmts]], ensure_ascii=False).encode()).hexdigest()[:12]
    return f"{meta['last_edited']}|c{len(cmts)}", body_hash, content, cmts


def board_meta(m, rev):
    """대시보드에 보낼 노션 메타 (판정 페이로드에도 sync 가 자동으로 덮어쓴다)."""
    return {"id": m["id"], "no": m["no"], "title": m["title"], "url": m["url"], "author": m["requester"],
            "created_at": m["created"], "notion_status": m["status"], "notion_edited": m["last_edited"], "revision": rev,
            "notion_priority": m["priority"] or "", "due": m["due"] or ""}


# ---------- 명령 ----------

def all_rows():
    return sorted((row_meta(r) for r in paged(f"databases/{DB_ID}/query", "POST", {})), key=lambda r: r["created"])


def tracked_rows():
    """detail 의 이름 매핑용 (people 속성 수집)."""
    return all_rows()


def cmd_scan():
    cache = load_cache(); pages = cache.setdefault("pages", {})
    server = {i["id"]: i for i in board("/api/issues")["issues"]}
    cands, metas, pending = [], [], {}
    for m in all_rows():
        srv = server.get(m["id"])
        if not srv and m["status"] != PROGRESS:
            continue
        prev = pages.get(m["id"], {})
        rev, body_hash, content, cmts = fetch_state(m, prev)
        pages[m["id"]] = {**prev, "last_edited": m["last_edited"], "body_hash": body_hash, "comment_count": len(cmts)}
        pending[m["id"]] = {"meta": board_meta(m, rev), "content": content}

        reason = None
        if m["status"] == PROGRESS:
            if not srv or srv.get("stage") != "fix":
                reason = "progress"  # 노션 '진행 중' 으로 바뀜 → 할일 수정필요로 정리
            elif prev.get("content") and prev["content"] != content:
                reason = "changed"  # 진행 중 이슈의 제목·본문·댓글이 바뀜 → 할일 내용 갱신
            elif not prev.get("content"):
                pages[m["id"]]["content"] = content
        else:
            pages[m["id"]]["content"] = content  # 진행 중이 아니면 내용 변경은 할일과 무관 — 기준만 갱신

        if reason:
            cands.append({**m, "reason": reason, "on_server": bool(srv), "comments": len(cmts),
                          "last_comment": cmts[-1] if cmts else None})
        elif srv and (srv.get("revision") != rev or srv.get("no") != m["no"] or srv.get("notion_status") != m["status"]
                     or (srv.get("notion_priority") or "") != (m["priority"] or "") or (srv.get("due") or "") != (m["due"] or "")):
            metas.append({**board_meta(m, rev), "meta_only": True})  # 상태·속성만 반영 (단계 전환은 서버가 함)
    cache["pending"] = pending
    save_cache(cache)
    res = board("/api/sync", "POST", {"note": board("/api/issues")["last_note"], "issues": metas}) if metas else {}
    print(json.dumps({"scanned": len(pending), "meta_synced": len(metas), "stage_changed": {k: v for k, v in res.items() if k in ("done", "archived")},
                      "candidates": cands}, ensure_ascii=False, indent=1))


def cmd_detail(ids):
    tracked_rows()  # 요청자/담당자 people 에서 이름 매핑 수집
    for pid in ids:
        m = row_meta(notion(f"pages/{pid}"))
        files = []
        body = render_blocks(pid, files=files)
        cmts = comments(pid)
        print(f"===== #{m['no']} {m['title']}  ({m['status']} / 요청 {m['requester']} / 담당 {m['assignee']} / {m['types']} / {m['menu']})")
        print(f"id={pid} url={m['url']} 생성={m['created']} 수정={m['last_edited']}")
        print("\n".join(body))
        print("--- 댓글")
        for c in cmts:
            print(f"[{c['at'][:16]} {c['by']}] {c['text']}")
            for f in c["files"]:
                f["name"] = f"댓글 {c['at'][:16]} {c['by']} 첨부 · {f['name']}"
                files.append(f)
                print(f"    [댓글 첨부 {f['type']}: {f['name']}]")
        imgs = [f for f in files if f["url"]]
        if imgs:
            d = IMG_DIR / pid; d.mkdir(parents=True, exist_ok=True)
            print("--- 첨부 (내려받음)")
            for i, f in enumerate(imgs):
                ext = pathlib.Path(f["url"].split("?")[0]).suffix or ".bin"
                dst = d / f"{i:02d}{ext}"
                try:
                    urllib.request.urlretrieve(f["url"], dst); print(f"{f['type']} {f['name']} → {dst}")
                except Exception as e:
                    print(f"{f['type']} {f['name']} 다운로드 실패: {e}")
        print()


def cmd_sync(path):
    """판정 페이로드 전송. 노션 메타(no·title·상태·변경 일시·revision)는 scan 값으로 강제한다."""
    payload = json.loads(pathlib.Path(path).read_text())
    cache = load_cache(); pending = cache.get("pending", {})
    for it in payload["issues"]:
        if it["id"] in pending:
            it.update(pending[it["id"]]["meta"])
    res = board("/api/sync", "POST", payload)
    for it in payload["issues"]:
        if it["id"] in pending and not it.get("meta_only"):
            cache["pages"].setdefault(it["id"], {})["content"] = pending[it["id"]]["content"]
    save_cache(cache)
    print(json.dumps(res, ensure_ascii=False))


def cmd_ack(ids):
    """판정 없이 현재 내용을 기준으로 삼는다 (무의미한 변경). 체크 해제는 meta 로 반영된다."""
    cache = load_cache(); pending = cache["pending"]
    metas = [{**pending[i]["meta"], "meta_only": True} for i in ids]
    board("/api/sync", "POST", {"note": board("/api/issues")["last_note"], "issues": metas})
    for i in ids:
        cache["pages"][i]["content"] = pending[i]["content"]
    save_cache(cache)
    print("ack", ids)


def cmd_mail_known():
    mails = board("/api/issues").get("mails") or []
    print(json.dumps({"since": MAIL_SINCE, "known": sorted(m["id"] for m in mails),
                      "recent": [{"no": m.get("no"), "subject": m["subject"], "received_at": m["received_at"], "dup_of": m.get("dup_of") or None}
                                 for m in sorted(mails, key=lambda m: m["received_at"])[-15:]]}, ensure_ascii=False, indent=1))


def cmd_mail_sync(path):
    payload = json.loads(pathlib.Path(path).read_text())
    for m in payload.get("mails", []):  # 서브에이전트가 돌려준 형식 차이 보정 (번호 숫자·'#', 문구 문자열)
        m["issue_nos"] = [str(n).lstrip("#") for n in m.get("issue_nos") or []]
        m["attachments"] = [a if isinstance(a, str) else a.get("name", str(a)) for a in m.get("attachments") or []]
        acts = m.get("actions") or []
        if isinstance(acts, (str, dict)):
            acts = [acts]
        m["actions"] = [a if isinstance(a, dict) else {"kind": "person", "to": "", "label": "회신 문구", "text": a} for a in acts]
        m["dup_of"] = m.get("dup_of") or ""
    print(json.dumps(board("/api/mail-sync", "POST", payload), ensure_ascii=False))


def mail_register(mail_no, nos):
    if mail_no and nos:
        return board("/api/mail-register", "POST", {"items": [{"no": int(mail_no), "notion": [str(n) for n in nos]}]})


# ---------- 노션 쓰기 (사용자 요청 시에만) ----------

def rt(text):
    """긴 문자열을 노션 rich_text(2000자 제한) 조각으로."""
    text = str(text)
    return [{"type": "text", "text": {"content": text[i:i + 1900]}} for i in range(0, max(len(text), 1), 1900)]


def para(text):
    return {"object": "block", "type": "paragraph", "paragraph": {"rich_text": rt(text) if text else []}}


def h2(text):
    return {"object": "block", "type": "heading_2", "heading_2": {"rich_text": rt(text)}}


def find_row(no):
    no = str(no).lstrip("#")
    for m in all_rows():
        if m["no"] == no:
            return m
    sys.exit(f"노션 #{no} 없음")


def requester_id():
    for r in paged(f"databases/{DB_ID}/query", "POST", {}):
        for u in r["properties"]["요청자"]["people"]:
            if u.get("name") == REQUESTER:
                return u["id"]
    sys.exit(f"요청자 {REQUESTER} 의 노션 사용자 id 를 찾지 못함")


def cmd_recent(days="30"):
    since = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(time.time() - int(days) * 86400))
    rows = [m for m in all_rows() if m["last_edited"] >= since]
    for m in sorted(rows, key=lambda m: m["last_edited"], reverse=True):
        print(f'#{m["no"]:>4} [{m["status"]}] {m["last_edited"][:10]} {m["menu"]} | {m["title"]} | 출처: {m["source"]}  id={m["id"]}')


def cmd_create(path):
    """[{mail_no,title,menu,types:[..],priority,due,source,path,content:[줄...]}] → 노션 신규요청."""
    items = json.loads(pathlib.Path(path).read_text())
    uid = requester_id(); made = {}
    for it in items:
        props = {"이슈": {"title": rt(it["title"])}, "상태": {"status": {"name": "신규요청"}},
                 "요청자": {"people": [{"id": uid}]}, "출처": {"rich_text": rt(it.get("source") or f'메일#{it["mail_no"]}')}}
        if it.get("menu"): props["분류"] = {"select": {"name": it["menu"]}}
        if it.get("types"): props["유형"] = {"multi_select": [{"name": x} for x in it["types"]]}
        if it.get("priority"): props["우선순위"] = {"select": {"name": it["priority"]}}
        if it.get("due"): props["마감일"] = {"date": {"start": it["due"]}}
        children = [h2("화면 경로"), para(it.get("path", "")), h2("이슈/요청 내용"),
                    *[para(x) for x in it["content"]], h2("해결안"), para("")]
        page = notion("pages", "POST", {"parent": {"database_id": DB_ID}, "properties": props, "children": children})
        no = prop(page["properties"]["ID"])
        made.setdefault(it.get("mail_no"), []).append(no)
        print(f'#{no} 신규요청 {it["title"]}  {page["url"]}')
    for mail_no, nos in made.items():
        mail_register(mail_no, nos)


def cmd_rerequest(path):
    """[{no,mail_no,date,reason,content:[줄...]}] → 상태 '재요청' + '이슈/요청 내용' 위에 재요청 단락 추가."""
    items = json.loads(pathlib.Path(path).read_text())
    for it in items:
        m = find_row(it["no"])
        blocks = paged(f'blocks/{m["id"]}/children')
        idx = next((i for i, b in enumerate(blocks) if b["type"].startswith("heading")
                    and plain(b[b["type"]].get("rich_text")).strip().startswith("이슈/요청 내용")), None)
        if idx is None:
            sys.exit(f'#{m["no"]}: \'이슈/요청 내용\' 제목을 찾지 못함 — 본문 구조 확인 필요')
        if idx == 0:
            sys.exit(f'#{m["no"]}: \'이슈/요청 내용\' 이 첫 블록이라 그 위에 끼울 수 없음 (노션 API 는 after 만 지원)')
        new = [h2(f'이슈/요청 내용 ({it["date"]} 재요청)'), para("재요청 사유: " + it["reason"]), *[para(x) for x in it["content"]]]
        if it.get("mail_no"):
            new.append(para(f'출처: 메일#{it["mail_no"]}'))
        notion(f'blocks/{m["id"]}/children', "PATCH", {"children": new, "after": blocks[idx - 1]["id"]})
        notion(f'pages/{m["id"]}', "PATCH", {"properties": {"상태": {"status": {"name": "재요청"}}}})
        print(f'#{m["no"]} 재요청 처리 ({m["status"]} → 재요청) {m["title"]}')
        mail_register(it.get("mail_no"), [m["no"]])


def cmd_solution(path):
    """[{no, date, lines:[줄...], status?}] → 본문 '해결안' 섹션 끝에 '<date> 기준 반영 내용' 단락 추가 (+ 상태 변경)."""
    for it in json.loads(pathlib.Path(path).read_text()):
        m = find_row(it["no"])
        blocks = paged(f'blocks/{m["id"]}/children')
        head = lambda b: b["type"] in ("heading_1", "heading_2")
        idx = next((i for i, b in enumerate(blocks) if head(b)
                    and plain(b[b["type"]].get("rich_text")).strip().startswith("해결안")), None)
        if idx is None:
            sys.exit(f'#{m["no"]}: \'해결안\' 제목을 찾지 못함 — 본문 구조 확인 필요')
        end = next((i for i in range(idx + 1, len(blocks)) if head(blocks[i])), len(blocks))
        new = [para(f'{it["date"]} 기준 반영 내용'), *[para("- " + x) for x in it["lines"]]]
        notion(f'blocks/{m["id"]}/children', "PATCH", {"children": new, "after": blocks[end - 1]["id"]})
        print(f'#{m["no"]} 해결안 {len(it["lines"])}줄 추가 {m["title"]}')
        if it.get("status"):
            cmd_set_status(it["no"], it["status"])


def cmd_set_status(no, status):
    m = find_row(no)
    notion(f'pages/{m["id"]}', "PATCH", {"properties": {"상태": {"status": {"name": status}}}})
    m = row_meta(notion(f'pages/{m["id"]}'))
    cache = load_cache(); prev = cache.setdefault("pages", {}).get(m["id"], {})
    rev, body_hash, content, cmts = fetch_state(m, prev)
    cache["pages"][m["id"]] = {**prev, "last_edited": m["last_edited"], "body_hash": body_hash, "comment_count": len(cmts), "content": content}
    save_cache(cache)
    res = board("/api/sync", "POST", {"note": board("/api/issues")["last_note"], "issues": [{**board_meta(m, rev), "meta_only": True}]})
    print(f'#{m["no"]} 상태 → {m["status"]}  대시보드: {json.dumps(res, ensure_ascii=False)}')


ATTACH_EXT = r"(?:pptx?|xlsx?|docx?|pdf|zip|hwp)"
ATTACH_REQ = re.compile(r"(Node-B\s*Edge|pms)\s*개선\s*요청사항\s*(\d{6})(?:\.pptx?)?", re.I)  # 요청사항 PPT (확장자 생략 인용 포함)
ATTACH_SRC = re.compile(r"출처:\s*([^\s·(][^·\n]*?\.%s)" % ATTACH_EXT, re.I)  # '출처: 파일명.ext'
ATTACH_IMG = re.compile(r"\[image:\s*([^\]\n]*?\.%s)(?:\s+\d+페이지)?\]" % ATTACH_EXT, re.I)  # 이미지 캡션에 박힌 문서명


def attach_key(name):
    return re.sub(r"\s+", "", re.sub(r"\.[a-z0-9]+$", "", name.lower()))


def attach_kind(name):
    e = (re.search(r"\.([a-z0-9]+)$", name.lower()) or [None, ""])[1]
    return ("ppt" if e in ("ppt", "pptx") else "xlsx" if e in ("xls", "xlsx") else "doc" if e in ("doc", "docx", "hwp")
            else "pdf" if e == "pdf" else "zip" if e == "zip" else "other")


def cmd_attach_sync():
    """이슈 본문에서 인용된 요청 문서를 찾아 서버 첨부파일 리스트로 올린다 (이슈 번호를 refs 로 연결)."""
    server = {i["id"]: i for i in board("/api/issues")["issues"]}
    found = {}  # key -> {name, refs:set}
    for m in all_rows():
        if m["id"] not in server and not (m["created"][:10] >= CREATED_SINCE and m["status"] != RESOLVED):
            continue
        body = "\n".join(render_blocks(m["id"], files=[]))
        names = []
        for mt in ATTACH_REQ.finditer(body):
            prefix = "Node-B Edge" if mt.group(1).lower().startswith("node") else "pms"
            names.append(f"{prefix}개선 요청사항{mt.group(2)}.pptx")
        for rx in (ATTACH_SRC, ATTACH_IMG):
            for mt in rx.finditer(body):
                n = mt.group(1).strip()
                if ATTACH_REQ.search(n):
                    continue  # 요청사항 PPT 는 위에서 정규화해 처리
                names.append(n)
        for n in names:
            e = found.setdefault(attach_key(n), {"name": n, "refs": set()})
            if "." in n and "." not in e["name"]:
                e["name"] = n
            e["refs"].add(m["no"])
    items = [{"id": k, "name": v["name"], "kind": attach_kind(v["name"]),
              "refs": [{"kind": "issue", "id": no} for no in sorted(v["refs"], key=int)]} for k, v in sorted(found.items())]
    print(json.dumps(board("/api/attachment-sync", "POST", {"attachments": items}), ensure_ascii=False))
    for it in items:
        print(f'{it["kind"]:5} {it["name"]}  ← ' + ",".join("#" + r["id"] for r in it["refs"]))


if __name__ == "__main__":
    a = sys.argv[1:]
    if not a: sys.exit(__doc__)
    {"scan": cmd_scan, "detail": lambda: cmd_detail(a[1:]),
     "sync": lambda: cmd_sync(a[1]), "ack": lambda: cmd_ack(a[1:]),
     "mail-known": cmd_mail_known, "mail-sync": lambda: cmd_mail_sync(a[1]),
     "recent": lambda: cmd_recent(*a[1:2]), "create": lambda: cmd_create(a[1]), "rerequest": lambda: cmd_rerequest(a[1]),
     "set-status": lambda: cmd_set_status(a[1], " ".join(a[2:])),
     "solution": lambda: cmd_solution(a[1]),
     "mail-register": lambda: print(json.dumps(mail_register(a[1], a[2:]), ensure_ascii=False)),
     "attach-sync": cmd_attach_sync}[a[0]]()
