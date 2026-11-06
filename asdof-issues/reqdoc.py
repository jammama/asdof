#!/usr/bin/env python3
"""고객사 요청사항 PPT → 노션 '요청사항 문서 정의' DB 업로드.

  python3 reqdoc.py upload REQ.json SLIDE_DIR   요구사항 목록을 DB 에 행으로 올린다 (이미 올린 file+page 는 건너뜀)

REQ.json: [{file, page, kind, received, no, priority, title, content}]  — 슬라이드 1장 = 1행
SLIDE_DIR: "<파일명(확장자 제외, NFC)>_pNN.jpg" 슬라이드 이미지 (PowerPoint PDF 내보내기 → 렌더)
행마다 스크린샷 속성과 본문에 슬라이드 이미지를 붙인다 (노션 File Upload API).
"""
import json, pathlib, sys, unicodedata, urllib.request, uuid

import trackbot as t

DB_ID = "3f20ae7c-bd8d-80d7-a6f8-f6fa7dd6ecb0"  # 요청사항 문서 정의
CACHE = t.HERE.parent / ".knowledge/reqdoc-cache.json"


def nfc(s):
    return unicodedata.normalize("NFC", s)


def upload_image(path):
    """노션 File Upload API (single part) → file_upload id."""
    up = t.notion("file_uploads", "POST", {"filename": path.name, "content_type": "image/jpeg"})
    boundary = uuid.uuid4().hex
    body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{path.name}\"\r\n"
            f"Content-Type: image/jpeg\r\n\r\n").encode() + path.read_bytes() + f"\r\n--{boundary}--\r\n".encode()
    req = urllib.request.Request(f"https://api.notion.com/v1/file_uploads/{up['id']}/send", method="POST", data=body, headers={
        "Authorization": "Bearer " + t.env["NOTION_TOKEN"], "Notion-Version": "2022-06-28",
        "Content-Type": f"multipart/form-data; boundary={boundary}"})
    with urllib.request.urlopen(req, timeout=120) as r:
        res = json.load(r)
    if res.get("status") != "uploaded":
        sys.exit(f"업로드 실패 {path.name}: {res}")
    return up["id"]


def text(s):
    # rich_text 한 덩어리는 2000자 제한
    return [{"type": "text", "text": {"content": s[i:i + 2000]}} for i in range(0, len(s), 2000)] or []


def create_row(it, img_id):
    name = f"{pathlib.Path(it['file']).stem}_p{it['page']:02d}.jpg"
    props = {
        "이름": {"title": text(it["title"])},
        "문서명": {"rich_text": text(it["file"])},
        "요구사항 내용": {"rich_text": text(it["content"])},
        "PPT 페이지": {"number": it["page"]},
        "구분": {"select": {"name": it["kind"]}},
        "스크린샷": {"files": [{"type": "file_upload", "file_upload": {"id": img_id}, "name": name}]},
    }
    if it.get("received"):
        props["수신일"] = {"date": {"start": it["received"]}}
    head = " · ".join(x for x in [it.get("no"), f"우선순위 {it['priority']}" if it.get("priority") else ""] if x)
    children = []
    if head:
        children.append({"type": "callout", "callout": {"rich_text": text(head), "icon": {"type": "emoji", "emoji": "📌"}}})
    children.append({"type": "image", "image": {"type": "file_upload", "file_upload": {"id": img_id}}})
    return t.notion("pages", "POST", {"parent": {"database_id": DB_ID}, "properties": props, "children": children})


def cmd_upload(req_path, slide_dir):
    items = json.loads(pathlib.Path(req_path).read_text())
    cache = json.loads(CACHE.read_text()) if CACHE.exists() else {}
    slides = {nfc(p.name): p for p in pathlib.Path(slide_dir).glob("*.jpg")}
    done = 0
    for it in items:
        it["file"] = nfc(it["file"])
        key = f"{it['file']}#{it['page']}"
        if key in cache:
            continue
        img = slides.get(f"{pathlib.Path(it['file']).stem}_p{int(it['page']):02d}.jpg")
        if not img:
            print("슬라이드 이미지 없음:", key); continue
        page = create_row(it, upload_image(img))
        cache[key] = page["id"]
        CACHE.write_text(json.dumps(cache, ensure_ascii=False, indent=1))
        done += 1
        print(f"[{done}] {key} → {it['title']}")
    print(f"완료: 신규 {done}건, 전체 {len(cache)}건")


if __name__ == "__main__":
    a = sys.argv[1:]
    if len(a) != 3 or a[0] != "upload":
        sys.exit(__doc__)
    cmd_upload(a[1], a[2])
