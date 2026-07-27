// player.js — 게임 실행 / 상태저장 / 실행상태(일시정지·입력) / 자동저장 / 배속 제어
import { getModule, persist, resumeAudio } from './engine.js';

const GAME_DIR = '/data/games/';
const SYNC_INTERVAL_MS = 15000;   // 배터리 세이브(SRAM)를 주기적으로 영속화

// 데스크톱 기본 키 바인딩 (SDL 키 이름 → GBA 입력)
const DEFAULT_KEYS = [
  ['Up', 'up'], ['Down', 'down'], ['Left', 'left'], ['Right', 'right'],
  ['X', 'a'], ['Z', 'b'], ['A', 'l'], ['S', 'r'],
  ['Return', 'start'], ['Backspace', 'select'],
];

const ROM_EXT_RE = /\.(gba|gbc|gb|zip|7z)$/i;

let syncTimer = null;
let fastForward = false;
let boundKeys = false;
let running = false;
const sessionSlots = new Set();   // 이번 세션에 저장한 슬롯(디렉터리 탐지 보강)

// 롬 실행. 성공 시 true. (loadGame 은 전체 경로를 받는다)
export function launch(name) {
  const m = getModule();
  const path = GAME_DIR + name;
  const ok = m.loadGame(path);
  console.log('[emu] loadGame', path, '→', ok);
  if (!ok) return false;
  sessionSlots.clear();   // 새 게임 → 세션 슬롯 초기화

  // 코어 설정은 게임 로드 후에 적용한다 (로드 전엔 crash 위험).
  if (!boundKeys) {
    for (const [key, input] of DEFAULT_KEYS) {
      try { m.bindKey(key, input); } catch (e) { console.warn('[emu] bindKey 실패', key, e); }
    }
    boundKeys = true;
  }
  m.setVolume(1.0);
  m.toggleInput(true);
  resumeAudio();
  fastForward = false;
  m.setFastForwardMultiplier(1);
  applyAutoSaveSettings();
  running = true;

  clearInterval(syncTimer);
  syncTimer = setInterval(persist, SYNC_INTERVAL_MS);
  return true;
}

// 게임 종료 → 라이브러리로. 종료 전 세이브를 반드시 영속화.
export async function quit() {
  const m = getModule();
  clearInterval(syncTimer);
  syncTimer = null;
  running = false;
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

// 현재 게임에서 채워진 상태저장 슬롯 번호 집합.
export function filledStateSlots() {
  const m = getModule();
  const base = (m.gameName || '').split('/').pop().replace(ROM_EXT_RE, '');
  const out = new Set(sessionSlots);
  let files = [];
  try { files = m.FS.readdir('/data/states/'); } catch { return out; }
  for (const f of files) {
    if (f === '.' || f === '..') continue;
    if (base && !f.includes(base)) continue;
    const mm = f.match(/\.?ss(\d+)$/i);
    if (mm) out.add(parseInt(mm[1], 10));
  }
  return out;
}

// 빨리감기: 설정된 배속(기본 2x)으로 토글.
export function toggleFastForward() {
  const m = getModule();
  fastForward = !fastForward;
  const speed = parseFloat(localStorage.getItem('ff-speed') || '2') || 2;
  m.setFastForwardMultiplier(fastForward ? speed : 1);
  return fastForward;
}

// 코어 내장 자동 상태저장 설정 적용 (설정값 반영).
export function applyAutoSaveSettings() {
  const m = getModule();
  if (!m) return;
  const enable = localStorage.getItem('autostate') !== '0';        // 기본 ON
  const min = parseInt(localStorage.getItem('autostate-min') || '1', 10) || 1;
  try {
    m.setCoreSettings({
      autoSaveStateEnable: enable,
      autoSaveStateTimerIntervalSeconds: Math.max(10, min * 60),
      restoreAutoSaveStateOnLoad: true,
    });
  } catch (e) { console.warn('[emu] setCoreSettings 실패', e); }
}

// 현재 게임의 배터리 세이브 바이트 (서버 자동동기화/업로드용).
export function currentSave() {
  const m = getModule();
  try { return m.getSave(); } catch { return null; }   // Uint8Array | null
}

export function currentSaveName() {
  const m = getModule();
  return (m.saveName || '').split('/').pop() || '';
}

export function isPlaying() {
  return running;
}
