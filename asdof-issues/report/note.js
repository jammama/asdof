// 내부용 정리 문서(docx) — 제목·소제목·문단·글머리·표만 있는 간단한 문서. report.py note 가 호출한다.
//   node note.js <note.json> <출력 파일 경로>
// note.json: {"title", "subtitle", "blocks": [{"h": "소제목"} | {"p": "문단"} | {"li": "글머리", "lv": 0|1} | {"table": [[머리...], [행...]...]}]}
const fs = require('fs');
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, AlignmentType,
  Table, TableRow, TableCell, WidthType, ShadingType, BorderStyle,
} = require('./node_modules/docx');

const [src, outFile] = process.argv.slice(2);
const note = JSON.parse(fs.readFileSync(src, 'utf8'));
const FONT = 'Malgun Gothic';
const CONTENT_W = 9026;
const NAVY = '1F3A5F';
const run = (text, o = {}) => new TextRun({ text, font: FONT, size: 20, ...o });

// `코드` 표기는 고정폭 회색으로
function runs(text, o = {}) {
  return String(text).split(/(`[^`]+`)/).filter(Boolean).map((s) => (s.startsWith('`')
    ? run(s.slice(1, -1), { ...o, font: 'Consolas', color: '555555' })
    : run(s, o)));
}

function table(rows) {
  const n = rows[0].length;
  const w = Math.floor(CONTENT_W / n);
  return new Table({
    width: { size: CONTENT_W, type: WidthType.DXA },
    columnWidths: Array(n).fill(w),
    rows: rows.map((r, i) => new TableRow({ tableHeader: i === 0, children: r.map((c) => new TableCell({
      width: { size: w, type: WidthType.DXA },
      shading: i === 0 ? { type: ShadingType.CLEAR, fill: 'E8EEF5', color: 'auto' } : undefined,
      margins: { top: 40, bottom: 40, left: 80, right: 80 },
      children: [new Paragraph({ children: runs(c, { size: 18, bold: i === 0 }) })],
    })) })),
  });
}

const children = [
  new Paragraph({ spacing: { after: 80 }, children: [run(note.title, { size: 32, bold: true, color: NAVY })] }),
];
if (note.subtitle) children.push(new Paragraph({ spacing: { after: 240 }, children: runs(note.subtitle, { color: '666666' }) }));
for (const b of note.blocks) {
  if (b.h) {
    children.push(new Paragraph({ heading: HeadingLevel.HEADING_2, spacing: { before: 280, after: 100 },
      border: { bottom: { style: BorderStyle.SINGLE, size: 6, color: '9AA5B1', space: 2 } },
      children: [run(b.h, { size: 24, bold: true, color: NAVY })] }));
  } else if (b.p) {
    children.push(new Paragraph({ spacing: { after: 80, line: 290 }, children: runs(b.p) }));
  } else if (b.li) {
    const lv = b.lv || 0;
    children.push(new Paragraph({ spacing: { after: 50, line: 280 }, indent: { left: 300 + lv * 320, hanging: 200 },
      children: runs(`${lv ? '–' : '•'} ${b.li}`) }));
  } else if (b.table) {
    children.push(table(b.table));
    children.push(new Paragraph({ spacing: { after: 80 }, children: [] }));
  }
}

const doc = new Document({
  styles: { default: { document: { run: { font: FONT, size: 20 } } } },
  sections: [{ properties: { page: { size: { width: 11906, height: 16838 }, margin: { top: 1100, bottom: 1100, left: 1440, right: 1440 } } }, children }],
});
Packer.toBuffer(doc).then((buf) => { fs.writeFileSync(outFile, buf); console.log(`written ${outFile}`); });
