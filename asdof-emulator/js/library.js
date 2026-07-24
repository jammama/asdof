// library.js — 롬 라이브러리: 로컬 업로드 + 서버 선반(roms.json) + 목록/삭제
import { getModule, persist } from './engine.js';

const GAME_DIR = '/data/games/';
const SAVE_DIR = '/data/saves/';
const STATE_DIR = '/data/states/';
const ROM_EXTS = ['gba', 'gbc', 'gb', 'zip', '7z'];

const ext = (name) => name.split('.').pop().toLowerCase();

// FS.readdir 은 '.' / '..' 를 포함하므로 걸러낸다.
export function listLocalRoms() {
  const m = getModule();
  return m.FS.readdir(GAME_DIR)
    .filter((n) => n !== '.' && n !== '..')
    .sort((a, b) => a.localeCompare(b, 'ko'));
}

// uploadRom 은 내부적으로 FileReader(비동기) → 콜백. Promise 로 감싼다.
function uploadRom(file) {
  return new Promise((resolve, reject) => {
    if (!ROM_EXTS.includes(ext(file.name))) {
      reject(new Error(`지원하지 않는 확장자: ${file.name}`));
      return;
    }
    getModule().uploadRom(file, resolve);
  });
}

// 로컬 파일 여러 개를 라이브러리에 추가하고 IndexedDB 에 영속화.
export async function addRomFiles(fileList) {
  const added = [];
  for (const file of fileList) {
    try {
      await uploadRom(file);
      added.push(file.name);
    } catch (e) {
      console.warn(e.message);
    }
  }
  await persist();
  return added;
}

export async function deleteRom(name) {
  const m = getModule();
  try {
    m.FS.unlink(GAME_DIR + name);
  } catch (e) {
    console.warn('삭제 실패:', e);
  }
  await persist();
}

// ── 서버 선반 ──────────────────────────────────────────
// roms/roms.json 매니페스트를 읽어, 아직 로컬에 없는 항목만 돌려준다.
// 형식: { "roms": [ { "name": "표시이름.gba", "file": "server-file.gba", "system": "GBA" } ] }
export async function fetchServerShelf() {
  try {
    const res = await fetch('roms/roms.json', { cache: 'no-cache' });
    if (!res.ok) return [];
    const data = await res.json();
    return Array.isArray(data.roms) ? data.roms : [];
  } catch {
    return [];
  }
}

// 서버 롬을 받아 로컬 라이브러리로 가져온다(임포트).
export async function importServerRom(entry) {
  const res = await fetch('roms/' + encodeURIComponent(entry.file));
  if (!res.ok) throw new Error(`서버에서 ${entry.file} 를 못 가져왔어요 (HTTP ${res.status})`);
  const buf = await res.arrayBuffer();
  // FS 경로/세이브 매칭 안정성을 위해 저장 파일명은 ascii(entry.file)로 통일.
  const file = new File([buf], entry.file);
  await uploadRom(file);
  await persist();
}

// ── 로컬 세이브 파일 (서버 동기화용) ────────────────────
// /data/saves (배터리 세이브 .sav) + /data/states (상태저장) 를 함께 나열.
export function listSaveFiles() {
  const m = getModule();
  const out = [];
  for (const dir of [SAVE_DIR, STATE_DIR]) {
    let files = [];
    try { files = m.FS.readdir(dir); } catch { files = []; }
    for (const f of files) {
      if (f === '.' || f === '..') continue;
      let size = 0;
      try { size = m.FS.stat(dir + f).size; } catch {}
      out.push({ name: f, path: dir + f, size });
    }
  }
  return out.sort((a, b) => a.name.localeCompare(b.name, 'ko'));
}

export function readSaveFile(path) {
  return getModule().FS.readFile(path);   // Uint8Array
}

// 서버에서 받은 세이브를 로컬 FS 에 기록 (.ss* → states, 그 외 → saves) + 영속화.
// 파일명이 게임의 세이브명과 같아야 인게임에서 이어진다(그래서 origin 을 우선 사용).
export async function writeSaveFile(name, bytes) {
  const dir = /\.ss\d*$/i.test(name) ? STATE_DIR : SAVE_DIR;
  getModule().FS.writeFile(dir + name, bytes);
  await persist();
}
