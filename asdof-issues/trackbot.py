#!/usr/bin/env python3
"""노션 PMS_OP_QA 이슈 ↔ trackbot 대시보드 연결 도구 (Claude 30분 주기 작업용).

  python3 trackbot.py scan            판정이 필요한 이슈 목록(JSON) 출력. 상태·속성만 바뀐 건 자동 meta 동기화
                                      대상: 2026-09-01 이후 생성 & 노션 상태 '해결' 아님 (+ 이미 대시보드에 있는 건)
  python3 trackbot.py detail ID...    본문·댓글·첨부를 텍스트로 출력, 이미지는 IMG_DIR 에 내려받음
  python3 trackbot.py sync FILE.json  {"note","issues":[...]} 전송 → 성공 시 기준 revision 갱신
  python3 trackbot.py ack ID...       판정 없이 현재 내용을 기준으로 삼음 (무의미한 변경 — 체크 해제는 됨)

노션은 통합 토큰(읽기 전용)으로 PMS_OP_QA DB 만 조회한다. 같은 페이지의 다른 블록은 읽지 않는다.
"""
import hashlib, json, os, pathlib, sys, time, urllib.error, urllib.request

HERE = pathlib.Path(__file__).resolve().parent
SECRETS = HERE.parent / ".knowledge/issues.env"
CACHE = HERE.parent / ".knowledge/trackbot-cache.json"
IMG_DIR = pathlib.Path(os.environ.get("IMG_DIR", "/tmp/trackbot-img"))

BASE = "https://trackbot.asdof.xyz"
DB_ID = "6f50ae7c-bd8d-8286-a3f3-817578575218"  # PMS_OP_QA
CREATED_SINCE = "2026-09-01"  # 이 날짜 이후 생성된 이슈만 신규 판정 (요청자 무관)
RESOLVED = "해결"  # 노션 상태가 이것인 이슈는 신규 판정 제외 (대시보드엔 해결됨으로만 반영)

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
            if e.code in (429, 502, 503, 504) and attempt < 3:  # 노션 rate limit (평균 3 req/s)·일시 장애
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
        "menu": prop(p["선택"]), "priority": prop(p["우선순위"]), "created": r["created_time"],
        "last_edited": r["last_edited_time"], "url": r["url"],
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
    if cache_entry and cache_entry.get("last_edited") == meta["last_edited"] and cache_entry.get("body_hash"):
        body_hash = cache_entry["body_hash"]
    else:
        body_hash = hashlib.sha1("\n".join(render_blocks(meta["id"])).encode()).hexdigest()[:12]
    content = hashlib.sha1(json.dumps([meta["title"], body_hash, [c["text"] for c in cmts], [len(c["files"]) for c in cmts]], ensure_ascii=False).encode()).hexdigest()[:12]
    return f"{meta['last_edited']}|c{len(cmts)}", body_hash, content, cmts


def board_meta(m, rev):
    """대시보드에 보낼 노션 메타 (판정 페이로드에도 sync 가 자동으로 덮어쓴다)."""
    return {"id": m["id"], "no": m["no"], "title": m["title"], "url": m["url"], "author": m["requester"],
            "created_at": m["created"], "notion_status": m["status"], "notion_edited": m["last_edited"], "revision": rev}


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
        on_server = m["id"] in server
        in_scope = m["created"][:10] >= CREATED_SINCE and m["status"] != RESOLVED
        if not on_server and not in_scope:
            continue
        prev = pages.get(m["id"], {})
        rev, body_hash, content, cmts = fetch_state(m, prev)
        pages[m["id"]] = {**prev, "last_edited": m["last_edited"], "body_hash": body_hash}
        pending[m["id"]] = {"meta": board_meta(m, rev), "content": content}

        reason = None
        if not on_server:
            reason = "new"
        elif prev.get("content") and prev["content"] != content:
            reason = "changed"  # 제목·본문·댓글이 바뀜 → 재판정
        elif server[m["id"]]["verdict"] == "resolved" and m["status"] != RESOLVED:
            reason = "unresolved"  # 노션에서 '해결' 이 풀림 → 재판정
        elif not prev.get("content"):
            pages[m["id"]]["content"] = content  # 기준 없음(도구 전환 직후): 현재 내용을 기준으로

        if reason:
            last = cmts[-1] if cmts else None
            cands.append({**m, "reason": reason, "on_server": on_server, "comments": len(cmts), "last_comment": last,
                          "prev_verdict": server.get(m["id"], {}).get("verdict")})
        elif server[m["id"]].get("revision") != rev or server[m["id"]].get("no") != m["no"]:
            metas.append({**board_meta(m, rev), "meta_only": True})  # 상태·속성만 바뀜: 판정 유지, 체크만 해제
    cache["pending"] = pending
    save_cache(cache)
    if metas:
        board("/api/sync", "POST", {"note": board("/api/issues")["last_note"], "issues": metas})
    print(json.dumps({"scanned": len(pending), "meta_synced": len(metas), "candidates": cands}, ensure_ascii=False, indent=1))


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


if __name__ == "__main__":
    a = sys.argv[1:]
    if not a: sys.exit(__doc__)
    {"scan": cmd_scan, "detail": lambda: cmd_detail(a[1:]),
     "sync": lambda: cmd_sync(a[1]), "ack": lambda: cmd_ack(a[1:])}[a[0]]()
