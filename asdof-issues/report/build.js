// 일일 정리 문서(docx) 생성 — report.py build 가 호출한다.
//   node build.js <작업 디렉토리> <출력 파일 경로>
// 작업 디렉토리: collected.json(노션 원문) · content.json(작업/문의 문구) · dims.json(이미지 크기) · img/
const fs = require('fs');
const path = require('path');
const {
  Document, Packer, Paragraph, TextRun, ImageRun, HeadingLevel, AlignmentType,
  PageBreak, Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
} = require('./node_modules/docx');

const [dir, outFile, mode] = process.argv.slice(2);
const MEETING = mode === 'meeting'; // 회의 문서: [문의](ask)·[안건](hold) 상세 페이지만
const read = (f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));
const collected = read('collected.json');
const content = read('content.json');
const dims = read('dims.json');
const byNo = Object.fromEntries(collected.issues.map((i) => [i.no, i]));

const FONT = 'Malgun Gothic';
// 고객사 문서에는 사람 이름·호칭을 넣지 않는다 (예: '10/19 황호성 메일' → '10/19 메일', '변성용 이사가 별도 전달' → '별도 전달')
const PEOPLE = /(황호성|변성용|이승혜|정찬우|이상훈|상훈 이|주대표)\s*(이사|팀장|부장|매니저|대표|님)?(이|가|께서|에게)?\s*/g;
const scrub = (t) => String(t ?? '').replace(PEOPLE, '').replace(/\(\s*\)/g, '').replace(/ {2,}/g, ' ').trim();
const CONTENT_W = 9026; // A4 폭 11906 - 좌우 여백 1440*2

const p = (text, run = {}) => new Paragraph({
  spacing: { after: 80, line: 300 },
  children: [new TextRun({ text: scrub(text), font: FONT, size: 21, ...run })],
});

const label = (text) => new Paragraph({
  spacing: { before: 200, after: 80 },
  border: { bottom: { style: BorderStyle.SINGLE, size: 6, color: '9AA5B1', space: 2 } },
  children: [new TextRun({ text, font: FONT, size: 23, bold: true, color: '1F3A5F' })],
});

function image(file, maxW, maxH) {
  const d = dims[path.basename(file)];
  if (!d) return null;
  const r = Math.min(maxW / d.w, maxH / d.h, 1);
  return new Paragraph({
    alignment: AlignmentType.CENTER,
    spacing: { before: 80, after: 80 },
    children: [new ImageRun({
      type: file.toLowerCase().endsWith('.png') ? 'png' : 'jpg',
      data: fs.readFileSync(path.join(dir, file)),
      transformation: { width: Math.round(d.w * r), height: Math.round(d.h * r) },
    })],
  });
}

function metaTable(issue) {
  const cols = [1500, 3013, 1500, 3013];
  const cell = (text, head, i) => new TableCell({
    width: { size: cols[i], type: WidthType.DXA },
    shading: head ? { type: ShadingType.CLEAR, fill: 'E8EEF5', color: 'auto' } : undefined,
    margins: { top: 60, bottom: 60, left: 100, right: 100 },
    children: [new Paragraph({ children: [new TextRun({ text: scrub(text), font: FONT, size: 19, bold: head })] })],
  });
  const kind = issue.menu === 'Android' ? '앱' : '웹';
  return new Table({
    width: { size: CONTENT_W, type: WidthType.DXA },
    columnWidths: cols,
    rows: [
      new TableRow({ children: [cell('노션참조번호', true, 0), cell(`#${issue.no}`, false, 1), cell('구분', true, 2), cell(`${kind} (${issue.menu})`, false, 3)] }),
      new TableRow({ children: [cell('출처', true, 0), cell(issue.sources.join(' / ') || '노션 직접 등록', false, 1), cell('요청일', true, 2), cell(issue.created.slice(0, 10), false, 3)] }),
    ],
  });
}

const heading = (text) => new Paragraph({
  heading: HeadingLevel.HEADING_1,
  spacing: { after: 160 },
  children: [new TextRun({ text: scrub(text), font: FONT, size: 30, bold: true, color: '1F3A5F' })],
});

function requestBlock(issue, imgMaxH) {
  const out = [label('1. 기존 요청 내용')];
  if (issue.screen) out.push(p(`화면 경로: ${issue.screen}`, { color: '555555' }));
  issue.request.forEach((t) => out.push(p(t)));
  const imgs = issue.images.filter((i) => i.src === 'body');
  const h = imgs.length > 1 ? Math.min(imgMaxH, 230) : imgMaxH;
  imgs.forEach((i) => { const im = image(i.file, 540, h); if (im) out.push(im); });
  return out;
}

const done = content.done || [];
const ask = content.ask || [];
const children = [];

const meetingDate = (content.meeting || {}).date || '';
const coverTitle = MEETING
  ? `${Number(meetingDate.slice(5, 7))}/${Number(meetingDate.slice(8, 10))} 회의 내 문의 및 안건 정리`
  : `${collected.date} PMS 요구사항 해결·문의 내용 정리`;
children.push(new Paragraph({ spacing: { before: 2400, after: 200 }, alignment: AlignmentType.CENTER,
  children: [new TextRun({ text: coverTitle, font: FONT, size: 40, bold: true, color: '1F3A5F' })] }));
children.push(new Paragraph({ alignment: AlignmentType.CENTER, spacing: { after: 600 },
  children: [new TextRun({ text: 'Node-B PMS', font: FONT, size: 24, color: '555555' })] }));
// 항목에 "sent": "YYYY-MM-DD" 가 있으면 이전에 전달한 항목 — 미전달 항목 뒤에 구분 페이지를 두고 따로 번호를 매긴다
const fresh = (list) => list.filter((c) => !c.sent);
const sent = (list) => list.filter((c) => c.sent);
const sentDates = [...new Set([...done, ...ask].filter((c) => c.sent).map((c) => c.sent))];
const mmdd = (d) => `${Number(d.slice(5, 7))}/${Number(d.slice(8, 10))}`;
const hold = content.hold || [];
if (MEETING) {
  children.push(p(`[문의] ${ask.length}건: 진행 전 확인을 위해 문의드린 내용입니다.`));
  children.push(p(`[안건] ${hold.length}건: 진행 방향을 회의에서 함께 정할 내용입니다.`));
} else if (sentDates.length) {
  children.push(p(`[개발완료] ${fresh(done).length}건 / [문의] ${fresh(ask).length}건: 이번에 새로 정리한 내용입니다.`));
  children.push(p(`기 전달 — [개발완료] ${sent(done).length}건 / [문의] ${sent(ask).length}건: ${sentDates.map(mmdd).join(', ')} 메일로 전달한 내용으로, 문서 하단에 함께 정리했습니다.`, { color: '555555' }));
} else if (content.cover_list) {
  children.push(p(`[개발완료] ${done.length}건 / [내용정리] ${(content.explain || []).length}건 / 남은 항목 일정 ${(content.schedule || []).length}건`, { bold: true }));
  content.cover_list.forEach((t) => children.push(p(t)));
} else {
  children.push(p(`[개발완료] ${done.length}건: 작업을 완료하여 운영에 반영한 내용입니다.`));
  children.push(p(`[문의] ${ask.length}건: 진행 전 확인을 위해 문의드린 내용입니다.`));
}

// 일반 표 — head: 열 제목, rows: 문자열 배열, widths: DXA 합계 CONTENT_W
function gridTable(head, rows, widths, size = 16) {
  const cell = (text, isHead, i, fill) => new TableCell({
    width: { size: widths[i], type: WidthType.DXA },
    shading: isHead ? { type: ShadingType.CLEAR, fill: 'E8EEF5', color: 'auto' } : fill ? { type: ShadingType.CLEAR, fill, color: 'auto' } : undefined,
    margins: { top: 40, bottom: 40, left: 70, right: 70 },
    children: [new Paragraph({ children: [new TextRun({ text: scrub(text), font: FONT, size, bold: isHead })] })],
  });
  return new Table({
    width: { size: CONTENT_W, type: WidthType.DXA },
    columnWidths: widths,
    rows: [new TableRow({ tableHeader: true, children: head.map((h, i) => cell(h, true, i)) }),
      ...rows.map((r) => new TableRow({ children: r.map((v, i) => cell(v, false, i, r._fill && r._fill[i])) }))],
  });
}

// 맨 앞 — 메일로 받은 요구사항 정리표에 완료여부·노션 번호·실제 구분(웹/앱/서버 정정)을 채운다
function reqTablePage(t) {
  children.push(new Paragraph({ children: [new PageBreak()] }));
  children.push(heading(t.title));
  if (t.note) children.push(p(t.note, { color: '555555', size: 18 }));
  const widths = [700, 650, 900, 2826, 700, 1050, 900, 1300];
  const rows = t.rows.map((r) => {
    const row = [...r];
    const fill = [];
    if (/개발완료/.test(r[5])) fill[5] = 'DDF3E4';
    if (String(r[7]).startsWith('※')) fill[7] = 'FFF1D6';
    row._fill = fill;
    return row;
  });
  children.push(gridTable(['요청일자', '대상', '목록', '요청내용', '요청기한', '완료여부', '노션', '실제 구분'], rows, widths, 15));
}

// [내용정리] — 현재 동작 설명 (문의 전 정리)
function explainPages(list) {
  list.forEach((c, idx) => {
    const issue = byNo[c.no] || { no: c.no, title: c.title, menu: '', sources: [], created: '', images: [], request: [], screen: '' };
    children.push(new Paragraph({ children: [new PageBreak()] }));
    children.push(heading(`[내용정리] ${idx + 1}. ${c.title || issue.title}`));
    if (byNo[c.no]) { children.push(metaTable(issue)); children.push(...requestBlock(issue, 230)); }
    children.push(label('2. 현재 동작 정리'));
    (c.detail || []).forEach((t) => children.push(p(t)));
    if (c.table) children.push(gridTable(c.table.head, c.table.rows, [2200, 3413, 3413], 17));
    (c.after || []).forEach((t) => children.push(p(t)));
  });
}

// 개발 일정 — [no, 구분, 상태, 예정일]
function schedulePage(list, note) {
  children.push(new Paragraph({ children: [new PageBreak()] }));
  children.push(heading('남은 항목 개발 일정'));
  if (note) children.push(p(note, { color: '555555', size: 18 }));
  const rows = list.map(([no, kind, status, due], i) => [String(i + 1), (byNo[no] || {}).title || '', `#${no}`, kind, status, due]);
  children.push(gridTable(['No', '항목', '노션', '구분', '상태', '예정일'], rows, [450, 4176, 700, 1300, 1400, 1000], 16));
}

function donePages(list, suffix) {
  list.forEach((c, idx) => {
    const issue = byNo[c.no];
    if (!issue) return;
    children.push(new Paragraph({ children: [new PageBreak()] }));
    children.push(heading(`[개발완료] ${idx + 1}. ${c.title || issue.title}${suffix(c)}`), metaTable(issue)); // c.title: 고객사용 제목(노션 제목에 내부 표기가 있을 때)
    children.push(...requestBlock(issue, issue.images.filter((i) => i.src === 'body').length > 1 ? 230 : 330));
    children.push(label('2. 작업 / 대응 내용'));
    (c.work || []).forEach((t) => children.push(p(t)));
  });
}

function askPages(list, suffix) {
  list.forEach((c, idx) => {
    const issue = byNo[c.no];
    if (!issue) return;
    const cimgs = issue.images.filter((i) => i.src === 'comment');
    children.push(new Paragraph({ children: [new PageBreak()] }));
    children.push(heading(`[문의] ${idx + 1}. ${issue.title}${suffix(c)}`), metaTable(issue));
    children.push(...requestBlock(issue, cimgs.length ? 170 : 300));
    children.push(label('2. 문의 내용 및 상세 설명'));
    if (c.question) children.push(p(c.question, { bold: true }));
    (c.detail || []).forEach((t) => children.push(p(t)));
    cimgs.forEach((i, k) => { const im = image(i.file, k === 0 ? 420 : 300, k === 0 ? 190 : 70); if (im) children.push(im); });
  });
}

const small = (label, text) => new Paragraph({
  spacing: { after: 40, line: 260 }, indent: { left: 280 },
  children: [new TextRun({ text: `${label} `, font: FONT, size: 18, bold: true, color: '555555' }), new TextRun({ text: scrub(text), font: FONT, size: 18 })],
});

// 회의 문서의 [안건] 페이지 — 요청 내용(+본문 사진) / 현황 및 회의 확인 사항
function agendaPages(list) {
  list.forEach((c, idx) => {
    const issue = byNo[c.no];
    if (!issue) return;
    children.push(new Paragraph({ children: [new PageBreak()] }));
    children.push(heading(`[안건] ${idx + 1}. ${issue.title}`), metaTable(issue));
    children.push(...requestBlock(issue, 260));
    children.push(label('2. 현황 및 회의 확인 사항'));
    if (c.question) children.push(p(c.question, { bold: true }));
    (c.detail || [c.reason]).forEach((t) => children.push(p(t)));
  });
}

if (MEETING) {
  askPages(ask, () => '');
  agendaPages(hold.filter((h) => !h.brief));
  // "brief": true 안건은 사진·원문 없이 한 페이지에 한 줄씩 (예: 회의에서 논의만 할 항목)
  const brief = hold.filter((h) => h.brief);
  if (brief.length) {
    children.push(new Paragraph({ children: [new PageBreak()] }));
    children.push(heading('[안건] 회의 내 논의 예정 항목'));
    brief.forEach((h, idx) => {
      const issue = byNo[h.no] || { title: '' };
      children.push(new Paragraph({ spacing: { before: 140, after: 40 }, keepNext: true,
        children: [new TextRun({ text: `${hold.length - brief.length + idx + 1}. ${issue.title}`, font: FONT, size: 20, bold: true, color: '1F3A5F' }),
                   new TextRun({ text: `  (노션참조번호 #${h.no})`, font: FONT, size: 17, color: '777777' })] }));
      children.push(small('1. 요청사항:', h.request));
      children.push(small('2. 현황:', h.reason));
    });
  }
} else {
if (content.reqtable) reqTablePage(content.reqtable);
donePages(fresh(done), () => '');
if (content.explain) explainPages(content.explain);
askPages(fresh(ask), () => '');
if (content.schedule) schedulePage(content.schedule, content.schedule_note);
}
if (!MEETING && sentDates.length) {
  children.push(new Paragraph({ children: [new PageBreak()] }));
  children.push(new Paragraph({ spacing: { before: 2400, after: 200 }, alignment: AlignmentType.CENTER,
    children: [new TextRun({ text: '기 전달 항목', font: FONT, size: 36, bold: true, color: '1F3A5F' })] }));
  children.push(new Paragraph({ alignment: AlignmentType.CENTER, spacing: { after: 200 },
    children: [new TextRun({ text: `${sentDates.map(mmdd).join(', ')} 메일로 전달한 내용입니다. [개발완료] ${sent(done).length}건 / [문의] ${sent(ask).length}건`, font: FONT, size: 22, color: '555555' })] }));
  const tag = (c) => ` (${mmdd(c.sent)} 전달)`;
  donePages(sent(done), tag);
  askPages(sent(ask), tag);
}

// 요약 페이지 — 개발확인중·검토중 항목을 사진 없이 한 줄씩 리스트업
// 노션 '진행 중'·'신규요청'·'재요청'은 [개발확인중] 하나로 합친다
const SUMMARY = [
  [['progress', 'new'], '개발확인중', '2. 진행 상황:'],
  [['hold'], '검토중', '2. 보류 사유:'],
];
const counts = {};
for (const [keys, tag, second] of MEETING || content.schedule ? [] : SUMMARY) { // 일정표가 있으면 요약 페이지 대신 일정표
  const list = keys.flatMap((k) => content[k] || []);
  counts[tag] = list.length;
  if (!list.length) continue;
  children.push(new Paragraph({ children: [new PageBreak()] }));
  children.push(heading(`참고 — [${tag}] 항목`));
  list.forEach((h, idx) => {
    const issue = byNo[h.no] || { title: '', no: h.no };
    children.push(new Paragraph({ spacing: { before: 140, after: 40 }, keepNext: true,
      children: [new TextRun({ text: `[${tag}] ${idx + 1}. ${issue.title}`, font: FONT, size: 20, bold: true, color: '1F3A5F' }),
                 new TextRun({ text: `  (노션참조번호 #${h.no})`, font: FONT, size: 17, color: '777777' })] }));
    children.push(small('1. 요청사항:', h.request));
    children.push(small(second, h.reason));
  });
}

const doc = new Document({
  styles: { default: { document: { run: { font: FONT, size: 21 } } } },
  sections: [{ properties: { page: { size: { width: 11906, height: 16838 }, margin: { top: 1200, bottom: 1200, left: 1440, right: 1440 } } }, children }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync(outFile, buf);
  console.log(MEETING ? `written ${outFile} (문의 ${ask.length} / 안건 ${hold.length})` : `written ${outFile} (개발완료 ${done.length} / 문의 ${ask.length} / ${Object.entries(counts).map(([k, n]) => `${k} ${n}`).join(' / ')})`);
});
