// 운영 반영 내역 문서(docx) — report.py release 가 호출한다.
//   node release.js <작업 디렉토리> <출력 파일 경로>
// 작업 디렉토리: release.json(구역별 변경 내용) · release_issues.json(노션 제목·출처·요청일)
// 완료된 항목만, 고객사가 '이번에 무엇이 바뀌었는지'를 한눈에 보도록 요약표 + 구역별 변경 전/후로 구성한다.
const fs = require('fs');
const path = require('path');
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  PageBreak, Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
} = require('./node_modules/docx');

const [dir, outFile] = process.argv.slice(2);
const read = (f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));
const rel = read('release.json');
const meta = read('release_issues.json');
const byNo = Object.fromEntries(meta.issues.map((i) => [String(i.no), i]));

const FONT = 'Malgun Gothic';
const CONTENT_W = 9026; // A4 폭 11906 - 좌우 여백 1440*2
const NAVY = '1F3A5F';

const run = (text, o = {}) => new TextRun({ text, font: FONT, size: 21, ...o });
const p = (text, o = {}, para = {}) => new Paragraph({ spacing: { after: 80, line: 300 }, ...para, children: [run(text, o)] });
const bullet = (text) => new Paragraph({ spacing: { after: 60, line: 290 }, indent: { left: 360, hanging: 220 }, children: [run(`• ${text}`)] });

const heading = (text) => new Paragraph({
  heading: HeadingLevel.HEADING_1, spacing: { before: 120, after: 160 },
  border: { bottom: { style: BorderStyle.SINGLE, size: 8, color: NAVY, space: 4 } },
  children: [run(text, { size: 28, bold: true, color: NAVY })],
});

function cell(text, width, { head = false, center = false } = {}) {
  return new TableCell({
    width: { size: width, type: WidthType.DXA },
    shading: head ? { type: ShadingType.CLEAR, fill: 'E8EEF5', color: 'auto' } : undefined,
    margins: { top: 50, bottom: 50, left: 90, right: 90 },
    children: [new Paragraph({ alignment: center ? AlignmentType.CENTER : AlignmentType.LEFT,
      children: [run(text, { size: 18, bold: head })] })],
  });
}

const date = rel.date || meta.date;
const mmdd = `${Number(date.slice(5, 7))}/${Number(date.slice(8, 10))}`;
const items = rel.sections.flatMap((s) => s.items.map((it) => ({ ...it, section: s.title })));
const children = [];

// 표지
children.push(new Paragraph({ spacing: { before: 2400, after: 200 }, alignment: AlignmentType.CENTER,
  children: [run(`${mmdd} 기준 운영 반영 내역`, { size: 40, bold: true, color: NAVY })] }));
children.push(new Paragraph({ alignment: AlignmentType.CENTER, spacing: { after: 600 },
  children: [run('Node-B PMS', { size: 24, color: '555555' })] }));
children.push(p(rel.intro || `${rel.since} 이후 운영에 반영된 변경 사항 ${items.length}건을 정리했습니다.`));
if (rel.note) children.push(p(rel.note, { color: '555555', size: 19 }));

// 요약표 — 번호 / 구역 / 항목 / 바뀐 점(한 줄) / 노션
children.push(new Paragraph({ children: [new PageBreak()] }));
children.push(heading('변경 사항 한눈에 보기'));
const cols = [500, 1400, 3000, 3426, 700];
children.push(new Table({
  width: { size: CONTENT_W, type: WidthType.DXA }, columnWidths: cols,
  rows: [
    new TableRow({ tableHeader: true, children: ['No', '구분', '항목', '바뀐 점', '노션'].map((h, i) => cell(h, cols[i], { head: true, center: true })) }),
    ...items.map((it, idx) => new TableRow({ children: [
      cell(String(idx + 1), cols[0], { center: true }),
      cell(it.section, cols[1]),
      cell(it.title || (byNo[it.no] || {}).title || it.summary || '', cols[2]),
      cell(it.summary || '', cols[3]),
      cell(it.no ? `#${it.no}` : '-', cols[4], { center: true }),
    ] })),
  ],
}));

// 구역별 상세 — 변경 전 / 변경 후
let n = 0;
for (const sec of rel.sections) {
  children.push(new Paragraph({ children: [new PageBreak()] }));
  children.push(heading(sec.title));
  sec.items.forEach((it) => {
    n += 1;
    const issue = byNo[it.no] || { title: it.title || it.summary || '', sources: [], created: '' };
    children.push(new Paragraph({ spacing: { before: 240, after: 60 }, keepNext: true,
      children: [run(`${n}. ${it.title || issue.title}`, { size: 22, bold: true, color: NAVY }),
                 ...(it.no ? [run(`  (노션참조번호 #${it.no} · 요청일 ${String(issue.created).slice(0, 10)})`, { size: 17, color: '777777' })] : [])] }));
    if (it.before) {
      children.push(new Paragraph({ spacing: { after: 40 }, keepNext: true, indent: { left: 140 },
        children: [run('변경 전  ', { size: 19, bold: true, color: '9A3B3B' }), run(it.before, { size: 19, color: '444444' })] }));
    }
    children.push(new Paragraph({ spacing: { after: 40 }, keepNext: true, indent: { left: 140 },
      children: [run('변경 후', { size: 19, bold: true, color: '2E6B3F' })] }));
    (it.after || []).forEach((t) => children.push(bullet(t)));
  });
}

const doc = new Document({
  styles: { default: { document: { run: { font: FONT, size: 21 } } } },
  sections: [{ properties: { page: { size: { width: 11906, height: 16838 }, margin: { top: 1200, bottom: 1200, left: 1440, right: 1440 } } }, children }],
});

Packer.toBuffer(doc).then((buf) => {
  fs.writeFileSync(outFile, buf);
  console.log(`written ${outFile} (${items.length}건, 구역 ${rel.sections.length}개)`);
});
