// player.js — 게임 실행 / 상태저장 / 실행상태 / 자동복원 / 배속 / 되돌리기(rewind)
import { getModule, persist, resumeAudio } from './engine.js';

const GAME_DIR = '/data/games/';
const STATE_DIR = '/data/states/';
const SYNC_INTERVAL_MS = 15000;   // 배터리 세이브 + 되돌리기 스냅샷을 주기적으로 IDB 영속화
const TEMP_SLOT = 100;           // 스냅샷 캡처용 임시 슬롯(수동 슬롯 1~20 범위 밖)
const RING_MAX = 11;             // 되돌리기 링 크기 (인덱스 0~10 = 11개)

// 데스크톱 기본 키 바인딩 (SDL 키 이름 → GBA 입력)
const DEFAULT_KEYS = [
  ['Up', 'up'], ['Down', 'down'], ['Left', 'left'], ['Right', 'right'],
  ['X', 'a'], ['Z', 'b'], ['A', 'l'], ['S', 'r'],
  ['Return', 'start'], ['Backspace', 'select'],
];

const ROM_EXT_RE = /\.(gba|gbc|gb|zip|7z)$/i;

let syncTimer = null;
let boundKeys = false;
let running = false;
const sessionSlots = new Set();

// 롬 실행. 성공 시 true.
export function launch(name) {
  const m = getModule();
  const path = GAME_DIR + name;
  const ok = m.loadGame(path);
  console.log('[emu] loadGame', path, '→', ok);
  if (!ok) return false;
  sessionSlots.clear();

  if (!boundKeys) {
    for (const [key, input] of DEFAULT_KEYS) {
      try { m.bindKey(key, input); } catch (e) { console.warn('[emu] bindKey 실패', key, e); }
    }
    boundKeys = true;
  }
  m.setVolume(1.0);
  m.toggleInput(true);
  resumeAudio();
  m.setFastForwardMultiplier(1);
  applyAutoSaveSettings();
  running = true;
  rewindStart();      // running=true 이후에 호출해야 첫 스냅샷이 찍힘

  clearInterval(syncTimer);
  syncTimer = setInterval(persist, SYNC_INTERVAL_MS);
  return true;
}

export async function quit() {
  const m = getModule();
  clearInterval(syncTimer);
  syncTimer = null;
  running = false;
  rewindReset();
  await persist();
  m.toggleInput(false);
  m.quitGame();
}

// 실행/일시정지 통합 제어 (모달 열림·백그라운드 시). 키 입력도 함께 토글.
export function setRunning(on) {
  const m = getModule();
  if (!m || on === running) return;
  running = on;
  if (on) {
    m.resumeGame();
    resumeAudio();
    m.toggleInput(true);
  } else {
    m.pauseGame();
    m.toggleInput(false);
    persist();
  }
}

export function isPlaying() { return running; }

// ── 수동 상태저장 슬롯 (💾 세이브 모달, 슬롯 1~6) ──────
export async function saveState(slot = 1) {
  const m = getModule();
  const ok = m.saveState(slot);
  if (ok) sessionSlots.add(slot);
  await persist();
  return ok;
}

export function loadState(slot = 1) {
  return getModule().loadState(slot);
}

export function filledStateSlots() {
  const m = getModule();
  const base = (m.gameName || '').split('/').pop().replace(ROM_EXT_RE, '');
  const out = new Set(sessionSlots);
  let files = [];
  try { files = m.FS.readdir(STATE_DIR); } catch { return out; }
  for (const f of files) {
    if (f === '.' || f === '..') continue;
    if (base && !f.includes(base)) continue;
    const mm = f.match(/\.?ss(\d+)$/i);
    if (mm) out.add(parseInt(mm[1], 10));
  }
  return out;
}

// 슬롯 n 의 상태파일 경로(존재하는 것만).
function slotFile(n) {
  const m = getModule();
  const base = (m.gameName || '').split('/').pop().replace(ROM_EXT_RE, '');
  try {
    const re = new RegExp('\\.ss' + n + '$', 'i');
    const f = m.FS.readdir(STATE_DIR).find((x) => (!base || x.includes(base)) && re.test(x));
    return f ? STATE_DIR + f : null;
  } catch { return null; }
}

// 슬롯별 정보 { [slot]: {mtime} } — 채워진 슬롯만 (저장일시 표시용).
export function stateSlotInfo() {
  const m = getModule();
  const base = (m.gameName || '').split('/').pop().replace(ROM_EXT_RE, '');
  const map = {};
  let files = [];
  try { files = m.FS.readdir(STATE_DIR); } catch { return map; }
  for (const f of files) {
    if (f === '.' || f === '..') continue;
    if (base && !f.includes(base)) continue;
    const mm = f.match(/\.?ss(\d+)$/i);
    if (!mm) continue;
    let mtime = null;
    try { mtime = m.FS.stat(STATE_DIR + f).mtime; } catch {}
    map[parseInt(mm[1], 10)] = { mtime };
  }
  return map;
}

export function slotBytes(n) {
  const f = slotFile(n);
  if (!f) return null;
  try { return getModule().FS.readFile(f); } catch { return null; }
}

// 바이트를 슬롯 n 에 기록(가져오기/이동). saveState 로 올바른 이름 생성 후 덮어씀.
export function saveSlotBytes(n, bytes) {
  const m = getModule();
  try {
    if (!m.saveState(n)) return false;
    const f = slotFile(n);
    if (!f) return false;
    m.FS.writeFile(f, bytes);
    persist();
    return true;
  } catch (e) { console.warn('[emu] 슬롯 쓰기 실패', e); return false; }
}

export function deleteSlot(n) {
  const f = slotFile(n);
  if (!f) return false;
  try { getModule().FS.unlink(f); persist(); return true; }
  catch (e) { console.warn('[emu] 슬롯 삭제 실패', e); return false; }
}

// ── 배속 / 자동복원 ────────────────────────────────
// 배속 설정. mGBA 규약: 1=보통, >1 빨리감기, <0 슬로우(1/abs).
// → 0<mult<1 (슬로우 희망)은 음수형으로 변환. 클램프는 호출부에서.
export function setSpeed(mult) {
  const m = getModule();
  if (!m) return;
  const v = (mult > 0 && mult < 1) ? -(1 / mult) : mult;
  m.setFastForwardMultiplier(v);
}

// 코어 내장 자동 상태저장: '이어하기'(비정상종료 복원)용으로 항상 켠다.
export function applyAutoSaveSettings() {
  const m = getModule();
  if (!m) return;
  try {
    m.setCoreSettings({
      autoSaveStateEnable: true,
      autoSaveStateTimerIntervalSeconds: 60,
      restoreAutoSaveStateOnLoad: true,
    });
  } catch (e) { console.warn('[emu] setCoreSettings 실패', e); }
}

// 현재 게임의 배터리 세이브 (서버 동기화/업로드용).
export function currentSave() {
  const m = getModule();
  try { return m.getSave(); } catch { return null; }
}

export function currentSaveName() {
  const m = getModule();
  return (m.saveName || '').split('/').pop() || '';
}

// 현재 롬을 빠른 재시작 — 교체한 배터리 세이브(.sav)를 반영한다.
export function reloadGame() {
  try { getModule().quickReload(); } catch (e) { console.warn('[emu] quickReload 실패', e); }
}

// ── 서버 동기화용 상태저장 바이트 (되돌리기 캡처 로직 재사용) ──
export function currentRomName() { return romBase(); }              // 예: harvest-moon-fomt.gba
export function captureStateBytes() { return captureBytes(); }      // 현재 상태 → Uint8Array | null
export function loadStateBytes(bytes) { return restoreBytes(bytes); } // 바이트 → 즉시 로드(재시작 없음)

// ── 되돌리기(rewind) 링 — /data/states 에 파일로 영속 ──────
// 파일명: <롬파일명>.rw.<seq> (seq 증가). 같은 롬이면 새로고침 후에도 스캔해 재활성화.
// 되돌리기(load)는 링 파일을 읽기만 하고 바꾸지 않는다. 분단위 타이머만 갱신.
// 0번(복귀점)만 되돌리는 순간 실시간 캡처(메모리). IDB 영속화는 15초 syncTimer 가 처리.
let ring = [];               // [{seq, file}] newest → oldest, 최대 RING_MAX (0=최신)
let ringSeq = 0;
let ringTimer = null;
let onRingChange = null;

export function setRingChangeHandler(fn) { onRingChange = fn; }
function notifyRing() { if (onRingChange) onRingChange(); }

function romBase() {
  const m = getModule();
  return (m.gameName || '').split('/').pop() || '';   // 예: harvest-moon-fomt.gba
}
function ringPrefix() { return romBase() + '.rw.'; }

// 임시 슬롯으로 현재 상태를 바이트로 캡처(임시 파일은 즉시 제거).
function tempStatePath() {
  const m = getModule();
  try {
    const re = new RegExp('\\.ss' + TEMP_SLOT + '$', 'i');
    const f = m.FS.readdir(STATE_DIR).find((x) => re.test(x));
    return f ? STATE_DIR + f : null;
  } catch { return null; }
}
function captureBytes() {
  const m = getModule();
  try {
    if (!m.saveState(TEMP_SLOT)) return null;
    const path = tempStatePath();
    if (!path) return null;
    const bytes = m.FS.readFile(path).slice();
    try { m.FS.unlink(path); } catch {}
    return bytes;
  } catch (e) { console.warn('[emu] 스냅샷 캡처 실패', e); return null; }
}
function restoreBytes(bytes) {
  const m = getModule();
  try {
    if (!m.saveState(TEMP_SLOT)) return false;   // 임시 파일 생성
    const path = tempStatePath();
    if (!path) return false;
    m.FS.writeFile(path, bytes);                 // 스냅샷으로 덮어쓰기
    const ok = m.loadState(TEMP_SLOT);
    try { m.FS.unlink(path); } catch {}
    return ok;
  } catch (e) { console.warn('[emu] 스냅샷 복원 실패', e); return false; }
}

// 같은 롬의 기존 스냅샷 파일을 스캔해 링 복원(새로고침 후 재활성화).
function scanRing() {
  const m = getModule();
  ring = [];
  ringSeq = 0;
  const pre = ringPrefix();
  let files = [];
  try { files = m.FS.readdir(STATE_DIR); } catch { return; }
  const found = [];
  for (const f of files) {
    if (!f.startsWith(pre)) continue;
    const seq = parseInt(f.slice(pre.length), 10);
    if (Number.isFinite(seq)) found.push({ seq, file: STATE_DIR + f });
  }
  found.sort((a, b) => b.seq - a.seq);   // 최신 먼저
  ring = found.slice(0, RING_MAX);
  ringSeq = (found.length ? found[0].seq : 0) + 1;
  for (const e of found.slice(RING_MAX)) { try { m.FS.unlink(e.file); } catch {} }  // 초과 옛 파일 정리
}

export function rewindReset() {
  clearInterval(ringTimer);
  ringTimer = null;
  ring = [];
  ringSeq = 0;
}

// 설정(되돌리기 on/off + 간격)에 따라 타이머 시작. 기존 스냅샷 파일은 유지·복원.
export function rewindStart() {
  clearInterval(ringTimer);
  ringTimer = null;
  scanRing();
  if (localStorage.getItem('autostate') === '0') { notifyRing(); return; }  // 기본 ON
  const min = parseInt(localStorage.getItem('autostate-min') || '1', 10) || 1;
  pushSnapshot();
  ringTimer = setInterval(pushSnapshot, Math.max(15, min * 60) * 1000);
  notifyRing();
}

function pushSnapshot() {
  if (!running) return;   // 일시정지(메뉴/백그라운드) 중엔 스킵
  const m = getModule();
  const bytes = captureBytes();
  if (!bytes) return;
  const file = STATE_DIR + ringPrefix() + ringSeq;
  try { m.FS.writeFile(file, bytes); }
  catch (e) { console.warn('[emu] 스냅샷 저장 실패', e); return; }
  ring.unshift({ seq: ringSeq, file });
  ringSeq++;
  while (ring.length > RING_MAX) { const old = ring.pop(); try { m.FS.unlink(old.file); } catch {} }
  notifyRing();   // 파일 IDB 영속화는 15초 syncTimer(persist)가 담당
}

export function rewindCount() { return ring.length; }

// 스냅샷 목록 [{index, mtime}] (0=최신). 저장일시 표시용.
export function rewindList() {
  const m = getModule();
  return ring.map((e, i) => {
    let mtime = null;
    try { mtime = m.FS.stat(e.file).mtime; } catch {}
    return { index: i, mtime };
  });
}

// 인덱스 n 스냅샷으로 로드 (0=최신, 1=1칸전 …). 링 파일은 읽기만(안 바꿈).
export function rewindTo(n) {
  const m = getModule();
  if (n < 0 || n >= ring.length) return false;
  let bytes;
  try { bytes = m.FS.readFile(ring[n].file); }
  catch { return false; }
  return restoreBytes(bytes);
}
