#!/usr/bin/env python3
"""일일 '{날짜} pms요구사항 해결 문의 내용 정리' 문서(docx) — 수집 → 문구 작성 → 생성 → 서버 업로드.

  python3 report.py collect [날짜]   노션 '테스트 중'·'요청확인' 이슈 원문·스크린샷 수집 → report/out/<날짜>/
                                      content.json 이 없으면 우리 측(이승혜·변성용) 최근 댓글로 초안을 만든다
  (Claude) report/out/<날짜>/content.json 을 고객사용 문구로 다듬는다 — 형식은 아래
  python3 report.py build [날짜]     docx + 메일 제목·본문(.mail.json, 복붙용) 생성 → report/out/<날짜>/
  python3 report.py meeting [날짜]   회의용 '{회의일} 회의 내 문의 및 안건 정리.docx' — [문의](ask)·[안건](hold) 상세 페이지만
                                      content.json 의 "meeting": {"date": "YYYY-MM-DD"} 필요, hold 항목에 "detail": [...] 를 쓴다
  python3 report.py release [날짜]   '{날짜} 운영 반영 내역.docx' — 완료(해결) 항목만, 고객사가 바뀐 점을 보는 문서
                                      report/out/<날짜>/release.json: {"intro", "sections":[{"title","items":[{"no","before","after":[..]}]}]}
                                      노션 원문(제목·출처·요청일)은 해당 번호를 직접 읽어 release_issues.json 으로 저장해 쓴다
  python3 report.py upload [날짜]    OCI 서버 /var/lib/asdof-issues/files 에 올리고 다운로드 링크 출력 (같은 날짜는 덮어씀)
                                      대시보드 '대응 내용 정리' 탭에 목록·미리보기·다운로드·메일 제목/본문 복사가 생긴다

날짜 기본값은 오늘(KST), 형식 YYYY-MM-DD.

content.json
  {"done": [{"no": "167", "work": ["문장", ...]}],                        ← 노션 '테스트 중' → [개발완료]
   "ask":  [{"no": "205", "question": "문의 문장", "detail": ["문장", ...]}],  ← 노션 '요청확인' → [문의]
   "progress": [{"no": "..", "request": "요청 요약 (출처)", "reason": "진행 상황 (출처)"}],  ← '진행 중' → 요약 페이지 [개발확인중]
   "new":      [{"no": "..", "request": "요청 요약 (출처)", "reason": "파악 내용"}],          ← '신규요청'·'재요청' → [개발확인중] (진행 중 뒤에 이어서)
   "hold": [{"no": "194", "request": "요청 요약 (출처: …)", "reason": "홀딩 사유 (출처: …)"}]}  ← '검토 중' → 요약 페이지 [검토중]
  done·ask 항목에 "sent": "YYYY-MM-DD" 를 넣으면 이전 메일로 전달한 항목 — 문서 하단 '기 전달 항목'으로 분리, 메일 목록에서 제외.
  요약 페이지는 사진 없이 한 줄씩. 본문이 비어 있는 우리 측 메모성 이슈는 빼도 된다.
  순서대로 1부터 번호가 붙는다. 고객사에 보내는 문서라 SQL·커밋·코드 위치 같은 내부 내용은 쓰지 않는다.
  보고 문서다 — '확인 후 해결 처리 부탁', '회신 부탁', '검토 부탁' 같은 상대에게 요청하는 문구는 쓰지 않는다 (문의는 무엇을 문의했는지만 서술).
"""
import datetime, json, pathlib, re, subprocess, sys, urllib.parse, urllib.request

import trackbot as t

HERE = pathlib.Path(__file__).resolve().parent
OUT = HERE / "report" / "out"
STATUS = {"테스트 중": "done", "요청확인": "ask", "진행 중": "progress", "신규요청": "new", "재요청": "new", "검토 중": "hold", "미해결": "new"}
SUMMARY = ("progress", "new", "hold")  # 사진 없이 한 줄 요약 페이지
OURS = ("이승혜", "변성용")
IMG_EXT = (".png", ".jpg", ".jpeg")

SSH_HOST = "ubuntu@161.118.133.112"
SSH_KEY = HERE.parent / ".knowledge/ssh-key-2026-07-21.key"
FILES_DIR = "/var/lib/asdof-issues/files"


def today():
    return datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=9))).strftime("%Y-%m-%d")


def doc_name(date):
    return f"{date} pms요구사항 해결 문의 내용 정리.docx"


def sections(body):
    out, cur = {}, "_"
    for line in body:
        if line.startswith("## "):
            cur = line[3:].strip(); out[cur] = []
        else:
            out.setdefault(cur, []).append(line)
    return out


def parse_request(body):
    """본문에서 화면 경로·요청 문장·출처(파일명)만 뽑는다. 내부 메모(앱 반영은 #… 등)는 뺀다."""
    s = sections(body)
    screen = " ".join(l for l in s.get("화면 경로", []) if not l.startswith("[")).strip()
    request, sources = [], []
    for l in (x for line in s.get("이슈/요청 내용", []) for x in line.splitlines()):
        l = l.strip()
        if not l or l.startswith("["):
            continue
        if l.startswith("출처:"):
            sources.append(l[3:].split(" · ")[0].strip()); continue
        if sources and re.match(r"^- .*(요청사항|확인결과)", l):
            sources.append(l[2:].strip()); continue
        if re.match(r"^앱 (반영|공통사진)", l):
            continue
        request.append(l)
    return screen, request, sources


def fetch(url, dst):
    try:
        urllib.request.urlretrieve(url, dst); return True
    except Exception as e:
        print(f"  이미지 다운로드 실패 {dst.name}: {e}", file=sys.stderr); return False


def our_comments(cmts):
    return [re.sub(r"@\S+\s*", "", c["text"]).strip() for c in cmts if c["by"] in OURS and c["text"].strip()]


def cmd_collect(date):
    d = OUT / date; img = d / "img"; img.mkdir(parents=True, exist_ok=True)
    t.tracked_rows()  # 사람 이름 매핑
    issues, draft = [], {"done": [], "ask": [], "progress": [], "new": [], "hold": []}
    for r in t.all_rows():
        kind = STATUS.get(r["status"])
        if not kind:
            continue
        files = []
        body = t.render_blocks(r["id"], files=files)
        cmts = t.comments(r["id"])
        images = []
        if kind in ("progress", "new"):
            files, cmts_for_img = [], []  # [개발확인중] 참고 페이지는 사진 없이 한 줄 요약만
        elif kind == "hold":
            cmts_for_img = []  # [검토중]은 정리 문서에선 한 줄 요약, 회의 문서([안건])에선 본문 사진까지 싣는다
        else:
            cmts_for_img = cmts
        for i, f in enumerate(f for f in files if f["type"] == "image" and f["url"]):
            ext = pathlib.Path(f["url"].split("?")[0]).suffix.lower() or ".png"
            name = f"{r['no']}_b{i}{ext}"
            if ext in IMG_EXT and fetch(f["url"], img / name):
                images.append({"file": f"img/{name}", "src": "body"})
        for ci, c in enumerate(cmts_for_img):
            if c["by"] not in OURS:
                continue  # [문의] 페이지에는 우리 측 제안 캡처만 싣는다
            for fi, f in enumerate(c["files"]):
                ext = pathlib.Path(f["url"].split("?")[0]).suffix.lower()
                name = f"{r['no']}_c{ci}_{fi}{ext}"
                if ext in IMG_EXT and fetch(f["url"], img / name):
                    images.append({"file": f"img/{name}", "src": "comment"})
        screen, request, sources = parse_request(body)
        issues.append({"no": r["no"], "title": r["title"], "status": r["status"], "menu": r["menu"],
                       "created": r["created"], "due": r.get("due", ""), "screen": screen, "request": request, "sources": sources,
                       "images": images, "comments": [{"at": c["at"], "by": c["by"], "text": c["text"]} for c in cmts]})
        mine = our_comments(cmts)
        if kind == "done":
            draft["done"].append({"no": r["no"], "work": mine[-1].splitlines() if mine else ["(작업 내용 작성 필요)"]})
        elif kind in SUMMARY:
            draft[kind].append({"no": r["no"], "request": " ".join(request)[:200], "reason": mine[-1] if mine else "(작성 필요)"})
        else:
            draft["ask"].append({"no": r["no"], "question": mine[-1] if mine else "(문의 내용 작성 필요)", "detail": []})
    (d / "collected.json").write_text(json.dumps({"date": date, "issues": issues}, ensure_ascii=False, indent=1))
    content = d / "content.json"
    existed = content.exists()
    if not existed:
        content.write_text(json.dumps(draft, ensure_ascii=False, indent=1))
    print(json.dumps({"dir": str(d), "done": [i["no"] for i in draft["done"]], "ask": [i["no"] for i in draft["ask"]], **{k: [i["no"] for i in draft[k]] for k in SUMMARY},
                      "content": "기존 유지" if existed else "초안 생성 — 고객사용 문구로 다듬을 것"}, ensure_ascii=False))


def dims(d):
    out = {}
    for f in (d / "img").iterdir():
        o = subprocess.run(["sips", "-g", "pixelWidth", "-g", "pixelHeight", str(f)], capture_output=True, text=True).stdout.split()
        if "pixelWidth:" in o:
            out[f.name] = {"w": int(o[o.index("pixelWidth:") + 1]), "h": int(o[o.index("pixelHeight:") + 1])}
    (d / "dims.json").write_text(json.dumps(out))


def mail_name(date):
    return doc_name(date).removesuffix(".docx") + ".mail.json"


def build_mail(date, content, collected):
    """메일에 복붙할 제목·본문 — 보고 말투, 요청 문구 없음. content.json 의 "mail": {subject, intro} 로 바꿀 수 있다."""
    titles = {i["no"]: i["title"] for i in collected["issues"]}
    over = content.get("mail", {})
    # "sent" 가 있는 항목은 이전 메일로 전달한 것 — 목록에서 빼고 하단 정리 안내만 붙인다
    sent = [c for c in content.get("done", []) + content.get("ask", []) if c.get("sent")]
    done = [c for c in content.get("done", []) if not c.get("sent")]
    ask = [c for c in content.get("ask", []) if not c.get("sent")]
    lines = ["안녕하세요, 호성님.", "앰버로드 이승혜입니다.", "",
             over.get("intro") or f"{date} 기준 PMS 요구사항 해결 및 문의 내용을 정리하여 첨부드립니다.", ""]
    if done:
        lines += [f"[개발완료] {len(done)}건"] + [f"  {n}. {c.get('title') or titles.get(c['no'], '')}" for n, c in enumerate(done, 1)] + [""]
    explain = content.get("explain", [])
    if explain:
        lines += [f"[내용정리] {len(explain)}건"] + [f"  {n}. {c.get('title') or titles.get(c['no'], '')}" for n, c in enumerate(explain, 1)] + [""]
    if content.get("schedule"):
        lines += [f"남은 항목 {len(content['schedule'])}건의 개발 일정을 문서 마지막에 정리했습니다.", ""]
    if ask:
        lines += [f"[문의] {len(ask)}건"] + [f"  {n}. {titles.get(c['no'], '')}" for n, c in enumerate(ask, 1)] + [""]
    if sent:
        dates = ", ".join(sorted({f"{int(c['sent'][5:7])}/{int(c['sent'][8:10])}" for c in sent}))
        lines += [f"{dates} 메일로 전달드린 내용({len(sent)}건)은 문서 하단에 '기 전달 항목'으로 함께 정리했습니다.", ""]
    lines += ["상세 내용은 첨부 문서에 정리했습니다.", "", "감사합니다.", "이승혜 드림"]
    return {"subject": over.get("subject") or f"[Node-B PMS] {date} 요구사항 해결 및 문의 내용 정리",
            "body": "\n".join(lines), "done": len(done), "ask": len(ask)}


def cmd_build(date):
    d = OUT / date
    dims(d)
    subprocess.run(["node", str(HERE / "report" / "build.js"), str(d), str(d / doc_name(date))], check=True)
    mail = build_mail(date, json.loads((d / "content.json").read_text()), json.loads((d / "collected.json").read_text()))
    (d / mail_name(date)).write_text(json.dumps(mail, ensure_ascii=False, indent=1))
    print(f"메일 제목·본문 → {d / mail_name(date)}")


def meeting_name(content):
    return f"{content['meeting']['date']} 회의 내 문의 및 안건 정리.docx"


def cmd_meeting(date):
    d = OUT / date
    content = json.loads((d / "content.json").read_text())
    if not content.get("meeting", {}).get("date"):
        sys.exit('content.json 에 "meeting": {"date": "YYYY-MM-DD"} 필요')
    dims(d)
    subprocess.run(["node", str(HERE / "report" / "build.js"), str(d), str(d / meeting_name(content)), "meeting"], check=True)


def release_name(date):
    return f"{date} 운영 반영 내역.docx"


def cmd_release(date):
    d = OUT / date
    rel = json.loads((d / "release.json").read_text())
    nos = [it["no"] for sec in rel["sections"] for it in sec["items"] if it.get("no")]  # 노션 번호 없는 항목(커밋 기준 변경)도 허용
    issues = []
    for no in nos:
        m = find_row_meta(no)
        _, _, sources = parse_request(t.render_blocks(m["id"]))
        issues.append({"no": m["no"], "title": m["title"], "menu": m["menu"], "created": m["created"], "sources": sources})
    (d / "release_issues.json").write_text(json.dumps({"date": date, "issues": issues}, ensure_ascii=False, indent=1))
    subprocess.run(["node", str(HERE / "report" / "release.js"), str(d), str(d / release_name(date))], check=True)


def find_row_meta(no):
    for m in t.all_rows():
        if str(m["no"]) == str(no):
            return m
    sys.exit(f"노션 #{no} 없음")


def ssh(*args):
    base = ["-i", str(SSH_KEY), "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15"]
    return base + list(args)


def cmd_upload(date):
    src = OUT / date / doc_name(date)
    extra = [p for p in sorted((OUT / date).glob("*.docx")) if p.name not in (doc_name(date), release_name(date))
             and "회의 내 문의" not in p.name]  # note 등 그 밖의 문서 (회의 문서는 아래에서 따로)
    if not src.exists() and not (OUT / date / release_name(date)).exists() and not extra:
        sys.exit(f"문서 없음: {src} — 먼저 build (또는 release·note)")
    mail = OUT / date / mail_name(date)
    inst = lambda a, b: f"sudo install -m 0640 -o www-data -g www-data {a} '{FILES_DIR}/{b}' && rm -f {a}"
    cmd = f"sudo install -d -o www-data -g www-data {FILES_DIR}"
    if src.exists():
        tmp = f"/tmp/asdof-report-{date}.docx"
        subprocess.run(["scp", "-q", *ssh(str(src), f"{SSH_HOST}:{tmp}")], check=True)
        cmd += f" && {inst(tmp, doc_name(date))}"
    if src.exists() and mail.exists():
        tmp_mail = f"/tmp/asdof-report-{date}.mail.json"
        subprocess.run(["scp", "-q", *ssh(str(mail), f"{SSH_HOST}:{tmp_mail}")], check=True)
        cmd += f" && {inst(tmp_mail, mail_name(date))}"
    cpath = OUT / date / "content.json"
    content = json.loads(cpath.read_text()) if cpath.exists() else {}
    names = [doc_name(date)] if src.exists() else []
    if (OUT / date / release_name(date)).exists():
        tmp_rel = f"/tmp/asdof-release-{date}.docx"
        subprocess.run(["scp", "-q", *ssh(str(OUT / date / release_name(date)), f"{SSH_HOST}:{tmp_rel}")], check=True)
        cmd += f" && {inst(tmp_rel, release_name(date))}"
        names.append(release_name(date))
    for i, p in enumerate(extra):
        tmp_x = f"/tmp/asdof-note-{date}-{i}.docx"
        subprocess.run(["scp", "-q", *ssh(str(p), f"{SSH_HOST}:{tmp_x}")], check=True)
        cmd += f" && {inst(tmp_x, p.name)}"
        names.append(p.name)
    if content.get("meeting", {}).get("date") and (OUT / date / meeting_name(content)).exists():
        # 회의 문서도 같은 폴더에 올려 '대응 내용 정리' 탭에 함께 뜨게 한다 (메일 본문 없음)
        tmp_meet = f"/tmp/asdof-meeting-{date}.docx"
        subprocess.run(["scp", "-q", *ssh(str(OUT / date / meeting_name(content)), f"{SSH_HOST}:{tmp_meet}")], check=True)
        cmd += f" && {inst(tmp_meet, meeting_name(content))}"
        names.append(meeting_name(content))
    subprocess.run(["ssh", *ssh(SSH_HOST, cmd)], check=True)
    for n in names:
        print(f"{t.BASE}/files/{urllib.parse.quote(n)}?pw={urllib.parse.quote(t.env['ADMIN_PASSWORD'])}")


if __name__ == "__main__":
    a = sys.argv[1:]
    if not a or a[0] not in ("collect", "build", "meeting", "release", "upload"):
        sys.exit(__doc__)
    date = a[1] if len(a) > 1 else today()
    {"collect": cmd_collect, "build": cmd_build, "meeting": cmd_meeting, "release": cmd_release, "upload": cmd_upload}[a[0]](date)
