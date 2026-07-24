// player.js — 게임 실행/상태저장/영속화 제어
import { getModule, persist, resumeAudio } from './engine.js';

const GAME_DIR = '/data/games/';
const SYNC_INTERVAL_MS = 15000;   // 배터리 세이브(SRAM)를 주기적으로 영속화

// 데스크톱 기본 키 바인딩 (SDL 키 이름 → GBA 입력)
const DEFAULT_KEYS = [
  ['Up', 'up'], ['Down', 'down'], ['Left', 'left'], ['Right', 'right'],
  ['X', 'a'], ['Z', 'b'], ['A', 'l'], ['S', 'r'],
  ['Return', 'start'], ['Backspace', 'select'],
];

let syncTimer = null;
let fastForward = false;
let boundKeys = false;

// 롬 실행. 성공 시 true. (loadGame 은 전체 경로를 받는다)
export function launch(name) {
  const m = getModule();
  const path = GAME_DIR + name;
  const ok = m.loadGame(path);
  console.log('[emu] loadGame', path, '→', ok);
  if (!ok) return false;

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

  clearInterval(syncTimer);
  syncTimer = setInterval(persist, SYNC_INTERVAL_MS);
  return true;
}

// 게임 종료 → 라이브러리로. 종료 전 세이브를 반드시 영속화.
export async function quit() {
  const m = getModule();
  clearInterval(syncTimer);
  syncTimer = null;
  await persist();
  m.toggleInput(false);
  m.quitGame();
}

export async function saveState(slot = 1) {
  const m = getModule();
  const ok = m.saveState(slot);
  await persist();
  return ok;
}

export function loadState(slot = 1) {
  return getModule().loadState(slot);
}

export function toggleFastForward() {
  const m = getModule();
  fastForward = !fastForward;
  m.setFastForwardMultiplier(fastForward ? 2 : 1);
  return fastForward;
}

export function pause() {
  getModule().pauseGame();
  persist();
}

export function resume() {
  getModule().resumeGame();
  resumeAudio();
}
