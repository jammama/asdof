// app.js — 화면 전환(라이브러리 ↔ 플레이)과 UI 연결
import { initCore, persist } from './engine.js';
import {
  listLocalRoms, addRomFiles, deleteRom, readRomBytes,
} from './library.js';
import {
  listServerSaves, uploadServerSave, downloadServerSave, deleteServerSave, sanitizeName,
  listServerRoms, downloadServerRom, uploadServerRom,
} from './server-saves.js';
import * as player from './player.js';
import { initTouchControls } from './touch.js';

const $ = (sel) => document.querySelector(sel);
let playing = false;
let userPaused = false;
let ffMode = 'off';   // 'off' | 'a'(빨리감기) | 'b'(더 빠르게)

async function boot() {
  const loading = $('#loading');
  try {
    await initCore($('#canvas'));
  } catch (e) {
    loading.innerHTML = `<div class="err">⚠ 실행할 수 없어요<br><small>${e.message}</small></div>`;
    return;
  }
  loading.style.display = 'none';
  applyOrient();
  // UI 배선 중 오류가 나도 라이브러리는 반드시 렌더되도록 보호
  try {
    initTouchControls($('#stage'));
    wireUi();
  } catch (e) {
    console.error('[emu] UI 초기화 오류 (라이브러리는 계속 표시):', e);
  }
  await renderLibrary();
}

// 가로/세로 방향 결정. orientOverride 가 null 이면 창 방향을 따른다.
let orientOverride = null;   // null=자동, true=가로, false=세로
function applyOrient() {
  const landscape = orientOverride ?? window.matchMedia('(orientation: landscape)').matches;
  document.body.dataset.orient = landscape ? 'landscape' : 'portrait';
}

function wireUi() {
  $('#file-input').addEventListener('change', async (e) => {
    const files = [...e.target.files];
    e.target.value = '';
    if (files.length) await handleIncoming(files);
  });

  $('#btn-back').addEventListener('click', backToLibrary);
  $('#btn-saves').addEventListener('click', () => { renderSlots(); openModal('saves'); });
  $('#btn-snap').addEventListener('click', () => { renderRewindList(); openModal('snapshots'); });
  // ⋯ > 가져오기: 기기 파일 → 지정 슬롯에 넣기
  $('#slot-import-file').addEventListener('change', async (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file || !slotImportTarget) return;
    const bytes = new Uint8Array(await file.arrayBuffer());
    if (player.saveSlotBytes(slotImportTarget, bytes)) { renderSlots(); toast(`슬롯 ${slotImportTarget}에 넣음`); }
    else toast('가져오기 실패');
    slotImportTarget = 0;
  });
  $('#btn-ff').addEventListener('click', () => { ffMode = ffMode === 'a' ? 'off' : 'a'; applyFf(); });
  $('#btn-ff2').addEventListener('click', () => { ffMode = ffMode === 'b' ? 'off' : 'b'; applyFf(); });
  $('#btn-pause').addEventListener('click', () => {
    userPaused = !userPaused;
    const b = $('#btn-pause');
    b.classList.toggle('on', userPaused);
    b.textContent = userPaused ? '▶' : '⏸';
    updateRunState();
  });
  $('#btn-fs').addEventListener('click', () => {
    // 문서 전체를 전체화면으로 (모달·토스트가 #stage 바깥이라 문서 단위여야 위에 뜸)
    if (document.fullscreenElement) document.exitFullscreen();
    else document.documentElement.requestFullscreen?.();
  });
  $('#btn-rotate').addEventListener('click', () => {
    orientOverride = document.body.dataset.orient !== 'landscape';   // 현재 반대로
    applyOrient();
  });
  $('#btn-settings').addEventListener('click', () => openModal('settings'));

  // 모달 공통: 배경 클릭 / [data-close] 버튼으로 닫기
  document.querySelectorAll('.modal').forEach((m) => {
    m.addEventListener('click', (e) => { if (e.target === m) closeModal(m.id); });
  });
  document.querySelectorAll('[data-close]').forEach((b) => {
    b.addEventListener('click', () => closeModal(b.dataset.close));
  });

  // 설정 > 화면 > 게임패드: 체크=표시(기본), 해제=숨김
  $('#set-gamepad').addEventListener('change', (e) => {
    document.body.classList.toggle('no-pad', !e.target.checked);
  });

  // 설정 > 서버 (asdof-saves 주소 + 토큰) — localStorage 에 보관
  const urlInput = $('#set-saves-url');
  urlInput.value = localStorage.getItem('saves-url') || '';
  urlInput.addEventListener('change', () => {
    const v = urlInput.value.trim();
    if (v) localStorage.setItem('saves-url', v);
    else localStorage.removeItem('saves-url');
    renderLibrary();   // 서버 바뀌면 롬 목록 즉시 갱신
  });
  const tokenInput = $('#set-token');
  tokenInput.value = localStorage.getItem('save-token') || '';
  tokenInput.addEventListener('change', () => {
    localStorage.setItem('save-token', tokenInput.value.trim());
    renderLibrary();   // 토큰 넣으면 서버 롬 목록 뜨게
  });

  // 라이브러리: 설정 버튼(게임 진입 전에도 접근)
  $('#btn-lib-settings').addEventListener('click', () => openModal('settings'));
  // 설정 > 저장 데이터 관리 → 관리 모달 열기
  $('#btn-savedata').addEventListener('click', () => {
    closeModal('settings');
    renderStateSync();
    openModal('savefiles');
  });
  $('#btn-upload-state').addEventListener('click', uploadCurrentState);
  // 기기에 저장된 상태 파일 → 현재 게임에 즉시 불러오기
  $('#import-state-file').addEventListener('change', async (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (!file) return;
    if (!playing) { toast('게임 실행 중에 불러오세요'); return; }
    const bytes = new Uint8Array(await file.arrayBuffer());
    if (player.loadStateBytes(bytes)) { closeModal('savefiles'); toast('기기 파일에서 불러왔어요'); }
    else toast('불러오기 실패 (같은 게임 상태 파일인지 확인)');
  });

  // 설정: 햅틱 / 배속 / 자동 상태저장 / 서버 자동동기화 (localStorage 연동)
  bindToggle('#set-haptic', 'haptic');
  bindNumber('#set-ffspeed-a', 'ff-speed-a', '2', applyFf);
  bindNumber('#set-ffspeed-b', 'ff-speed-b', '4', applyFf);
  bindToggle('#set-autostate', 'autostate', true, restartRewind);
  bindSelect('#set-autostate-min', 'autostate-min', '1', restartRewind);
  bindToggle('#set-serversync', 'serversync', false, restartServerSync);
  bindSelect('#set-serversync-min', 'serversync-min', '5', restartServerSync);

  // WebGL 컨텍스트 손실 복원 허용 (모바일 백그라운드 복귀 크래시 완화)
  $('#canvas').addEventListener('webglcontextlost', (e) => {
    e.preventDefault();
    console.warn('[emu] WebGL 컨텍스트 손실 — 복원 시도');
  });

  // 모달 열림 / 백그라운드 전환 → 실행상태 갱신(일시정지·입력·저장)
  document.addEventListener('visibilitychange', updateRunState);
  window.addEventListener('pagehide', () => { if (playing) persist(); });
  window.addEventListener('resize', applyOrient);   // 창 방향 바뀌면 반영(자동 모드일 때)

  wireDragDrop();
}

// 파일 유입 공통 처리(업로드 버튼 / 드래그앤드롭 공용): 라이브러리에 추가 → 갱신.
// (자동 실행 안 함 — 목록에서 눌러 실행)
async function handleIncoming(files) {
  console.log('[emu] 파일 처리:', files.map((f) => f.name));
  const added = await addRomFiles(files);
  console.log('[emu] 라이브러리 추가됨:', added);
  await renderLibrary();
  if (added.length === 0) toast('지원하지 않는 파일이에요 (.gba/.gbc/.gb/.zip)');
  else toast(`${added.length}개 추가됨 — 목록에서 선택`);
}

// 창 어디에 놓아도 롬 파일을 받는다. (dragenter/leave 깊이 카운트로 깜빡임 방지)
function wireDragDrop() {
  const overlay = $('#drop');
  let depth = 0;
  const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
  const hide = () => { depth = 0; overlay.classList.remove('show'); };

  window.addEventListener('dragenter', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth++;
    overlay.classList.add('show');
  });
  window.addEventListener('dragover', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });
  window.addEventListener('dragleave', (e) => {
    e.preventDefault();
    if (--depth <= 0) hide();
  });
  window.addEventListener('drop', async (e) => {
    e.preventDefault();
    hide();
    const files = [...(e.dataTransfer?.files || [])];
    if (files.length) await handleIncoming(files);
  });
}

// ── 실행상태 / 설정 / 서버동기화 / 유틸 ──────────────
function anyModalOpen() { return !!document.querySelector('.modal.show'); }

// 플레이 중이면: 모달 안 열림 && 탭 보임 → 실행, 아니면 일시정지(입력·저장 포함)
function updateRunState() {
  if (!playing) return;
  player.setRunning(!document.hidden && !anyModalOpen() && !userPaused);
}

// 배속 적용 (ffMode + 설정값, 0.01~100 클램프)
function speedFor(key, def) {
  let n = parseFloat(localStorage.getItem(key));
  if (!isFinite(n)) n = def;
  return Math.min(100, Math.max(0.01, n));
}
function applyFf() {
  const mult = ffMode === 'a' ? speedFor('ff-speed-a', 2)
    : ffMode === 'b' ? speedFor('ff-speed-b', 4) : 1;
  player.setSpeed(mult);
  $('#btn-ff').classList.toggle('on', ffMode === 'a');
  $('#btn-ff2').classList.toggle('on', ffMode === 'b');
}

// localStorage 연동 토글/셀렉트
function bindToggle(sel, key, defaultOn = false, onChange) {
  const el = $(sel);
  const stored = localStorage.getItem(key);
  el.checked = stored === null ? defaultOn : stored === '1';
  el.addEventListener('change', () => {
    localStorage.setItem(key, el.checked ? '1' : '0');
    if (onChange) onChange();
  });
}
function bindSelect(sel, key, def, onChange) {
  const el = $(sel);
  el.value = localStorage.getItem(key) || def;
  el.addEventListener('change', () => {
    localStorage.setItem(key, el.value);
    if (onChange) onChange();
  });
}
function bindNumber(sel, key, def, onChange) {
  const el = $(sel);
  el.value = localStorage.getItem(key) || def;
  el.addEventListener('change', () => {
    let n = parseFloat(el.value);
    if (!isFinite(n)) n = parseFloat(def);
    n = Math.min(100, Math.max(0.01, n));
    el.value = String(n);
    localStorage.setItem(key, String(n));
    if (onChange) onChange();
  });
}

// 서버 자동 동기화 (주기별 현재 세이브 업로드)
let serverSyncTimer = null;
function startServerSync() {
  stopServerSync();
  if (localStorage.getItem('serversync') !== '1') return;
  const min = parseInt(localStorage.getItem('serversync-min') || '5', 10) || 5;
  serverSyncTimer = setInterval(autoServerSync, min * 60000);
  console.log('[emu] 서버 자동동기화 ON:', min, '분');
}
function stopServerSync() { clearInterval(serverSyncTimer); serverSyncTimer = null; }
function restartServerSync() { stopServerSync(); if (playing) startServerSync(); }
async function autoServerSync() {
  if (!playing || !localStorage.getItem('save-token')) return;
  const rom = player.currentRomName();
  const bytes = player.captureStateBytes();
  if (!bytes || !bytes.length || !rom) return;
  const name = `${labelOf(rom)} (자동)`;   // 게임별 롤링 최신 상태(덮어씀)
  try {
    await uploadServerSave(name, rom, bytes);
    console.log('[emu] 자동 서버동기화(상태) 완료:', name);
  } catch (e) { console.warn('[emu] 자동 서버동기화 실패:', e.message); }
}

// 세이브 바이트를 기기에 파일로 다운로드
function downloadToDevice(name, bytes) {
  const blob = new Blob([bytes], { type: 'application/octet-stream' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// 이어하기: 비정상 종료(탭 사망) 대비 마지막 롬을 다시 실행
function renderResume() {
  const el = $('#resume');
  el.innerHTML = '';
  const last = localStorage.getItem('last-rom');
  if (!last || !listLocalRoms().includes(last)) return;
  const btn = document.createElement('button');
  btn.className = 'resume-btn';
  btn.textContent = `▶ 이어하기: ${labelOf(last)}`;
  btn.addEventListener('click', () => launch(last));
  el.appendChild(btn);
}

// 되돌리기 목록: 💾 세이브 모달의 '되돌리기' 버튼으로 펼침. 스냅샷 + 저장일시, 클릭 시 그 지점으로.
function renderRewindList() {
  const ul = $('#rewind-list');
  ul.innerHTML = '';
  const list = player.rewindList();   // [{index, mtime}], 0=최신
  if (!list.length) {
    ul.innerHTML = '<li class="empty-sm">아직 자동 스냅샷이 없어요. (되돌리기 켜짐 + 시간 경과 필요)</li>';
    return;
  }
  for (const snap of list) {
    const li = document.createElement('li');
    li.className = 'file-row';
    const info = document.createElement('div');
    info.className = 'file-info';
    const when = fmtSlotTime(snap.mtime);
    const nm = document.createElement('span');
    nm.className = 'file-name';
    nm.textContent = when ? `${snap.index}. ${when}` : `${snap.index}.`;
    info.append(nm);
    const go = document.createElement('button');
    go.textContent = '되돌리기';
    go.addEventListener('click', () => {
      if (player.rewindTo(snap.index)) { closeModal('snapshots'); toast(snap.index === 0 ? '최신으로' : `${snap.index}칸 전으로`); }
      else toast('되돌리기 실패');
    });
    li.append(info, go);
    ul.appendChild(li);
  }
}

function restartRewind() {
  if (playing) player.rewindStart();
}

async function renderLibrary() {
  renderResume();
  // 로컬 롬(IndexedDB) + 서버 롬(asdof-saves, 토큰 필요)을 하나의 목록으로.
  const local = listLocalRoms();
  const localSet = new Set(local);
  let serverRoms = [];
  try { serverRoms = await listServerRoms(); }
  catch (e) { console.warn('[emu] 서버 롬 목록 실패:', e.message); }
  const pending = serverRoms.filter((r) => !localSet.has(r.name));

  const items = [
    ...local.map((file) => ({ file, label: labelOf(file), local: true })),
    ...pending.map((r) => ({ file: r.name, label: labelOf(r.name), local: false })),
  ];

  const list = $('#rom-list');
  list.innerHTML = '';
  if (!items.length) {
    list.innerHTML =
      '<li class="empty">아직 롬이 없어요. 파일을 끌어다 놓거나, 서버 롬은 설정 > 서버에서 토큰을 넣으면 보여요.</li>';
    return;
  }

  for (const it of items) {
    const li = document.createElement('li');

    const play = document.createElement('button');
    play.className = 'rom-play';
    if (it.local) {
      play.textContent = it.label;
    } else {
      const cloud = document.createElement('span');
      cloud.className = 'cloud';
      cloud.textContent = '☁';
      play.append(cloud, document.createTextNode(it.label));
    }
    play.addEventListener('click', () =>
      it.local ? launch(it.file) : importAndPlay(it.file));
    li.append(play);

    if (it.local) {
      const up = document.createElement('button');
      up.className = 'rom-del';
      up.title = '서버(개인 저장소)에 올리기';
      up.textContent = '↑';
      up.addEventListener('click', () => uploadRomToServer(it.file));
      const del = document.createElement('button');
      del.className = 'rom-del';
      del.title = '삭제';
      del.textContent = '✕';
      del.addEventListener('click', async () => {
        if (confirm(`${it.label} 을(를) 라이브러리에서 지울까요?\n(세이브 파일은 남습니다)`)) {
          await deleteRom(it.file);
          await renderLibrary();
        }
      });
      li.append(up, del);
    } else {
      const get = document.createElement('button');
      get.className = 'rom-del';
      get.title = '받아서 실행';
      get.textContent = '⭳';
      get.addEventListener('click', () => importAndPlay(it.file));
      li.append(get);
    }

    list.appendChild(li);
  }
}

// 서버 롬을 받아 로컬 임포트 후 실행
async function importAndPlay(name) {
  toast('서버에서 받는 중…');
  try {
    console.log('[emu] 서버 롬 임포트:', name);
    const bytes = await downloadServerRom(name);
    await addRomFiles([new File([bytes], name)]);
    await renderLibrary();
    launch(name);
  } catch (e) {
    console.warn('[emu] 임포트 실패:', e);
    alert(e.message);
  }
}

// 로컬 롬을 서버(개인 저장소)에 업로드
async function uploadRomToServer(name) {
  toast('서버에 올리는 중…');
  try {
    const bytes = readRomBytes(name);
    if (!bytes || !bytes.length) { toast('롬을 못 읽었어요'); return; }
    await uploadServerRom(name, bytes);
    toast('서버에 올림: ' + labelOf(name));
    await renderLibrary();
  } catch (e) { alert(e.message); }
}

// ── 모달 / 세이브 슬롯 ─────────────────────────────
const SLOT_COUNT = 20;
let slotImportTarget = 0;
function openModal(id) {
  $('#' + id).classList.add('show');
  updateRunState();   // 모달 열림 → 일시정지 + 입력 차단
}
function closeModal(id) {
  $('#' + id).classList.remove('show');
  updateRunState();   // 모달 닫힘 → (다른 모달 없으면) 재개
}

// 저장일시 표시용 포맷 (파일 mtime)
function fmtSlotTime(mtime) {
  if (!mtime) return '';
  const d = mtime instanceof Date ? mtime : new Date(mtime);
  if (isNaN(d.getTime())) return '';
  const p2 = (x) => String(x).padStart(2, '0');
  return `${p2(d.getFullYear() % 100)}/${p2(d.getMonth() + 1)}/${p2(d.getDate())} `
    + `${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}
function stampNow() {
  const d = new Date();
  const p2 = (x) => String(x).padStart(2, '0');
  return `${p2(d.getFullYear() % 100)}${p2(d.getMonth() + 1)}${p2(d.getDate())}${p2(d.getHours())}${p2(d.getMinutes())}`;
}
function mkAction(text, fn) {
  const b = document.createElement('button');
  b.textContent = text;
  b.addEventListener('click', fn);
  return b;
}

// 상태 저장 슬롯 렌더 (20개 · 저장일시 · ⋯ 메뉴: 삭제/가져오기/서버저장/슬롯변경)
function renderSlots() {
  const info = player.stateSlotInfo();
  const ul = $('#slot-list');
  ul.innerHTML = '';
  for (let n = 1; n <= SLOT_COUNT; n++) {
    const has = !!info[n];
    const li = document.createElement('li');
    li.className = 'slot-item';

    const main = document.createElement('div');
    main.className = 'slot-main';
    const label = document.createElement('span');
    label.className = 'slot-label';
    label.innerHTML = `슬롯 ${n}` + (has
      ? ` <span class="slot-when">${fmtSlotTime(info[n].mtime) || '저장됨'}</span>`
      : ' <span class="slot-empty">비어있음</span>');

    const save = document.createElement('button');
    save.className = 'slot-save';
    save.textContent = '저장';
    save.addEventListener('click', async () => {
      const ok = await player.saveState(n);
      renderSlots();
      toast(ok ? `슬롯 ${n} 저장됨` : '저장 실패');
    });

    const load = document.createElement('button');
    load.textContent = '불러오기';
    load.disabled = !has;
    load.addEventListener('click', () => {
      if (player.loadState(n)) { closeModal('saves'); toast(`슬롯 ${n} 불러옴`); }
      else toast('빈 슬롯이에요');
    });

    const more = document.createElement('button');
    more.className = 'slot-more';
    more.textContent = '⋯';
    more.title = '더보기';

    main.append(label, save, load, more);

    const actions = document.createElement('div');
    actions.className = 'slot-actions';
    actions.hidden = true;
    more.addEventListener('click', () => { actions.hidden = !actions.hidden; });

    const del = mkAction('삭제', () => {
      if (!confirm(`슬롯 ${n} 삭제할까요?`)) return;
      player.deleteSlot(n); renderSlots(); toast(`슬롯 ${n} 삭제`);
    });
    const imp = mkAction('가져오기', () => { slotImportTarget = n; $('#slot-import-file').click(); });
    const up = mkAction('서버저장', async () => {
      const bytes = player.slotBytes(n);
      if (!bytes) { toast('빈 슬롯'); return; }
      const raw = prompt('서버에 저장할 이름:', `슬롯${n} ${stampNow()}`);
      if (!raw) return;
      try { await uploadServerSave(sanitizeName(raw), player.currentRomName(), bytes); toast('서버에 저장됨'); }
      catch (e) { alert(e.message); }
    });
    const mv = mkAction('슬롯변경', () => moveSlot(n, info));
    del.disabled = up.disabled = mv.disabled = !has;

    actions.append(del, imp, up, mv);
    li.append(main, actions);
    ul.appendChild(li);
  }
}

function moveSlot(n, info) {
  const raw = prompt(`슬롯 ${n} 을(를) 옮길 번호 (1-${SLOT_COUNT}):`, '');
  if (!raw) return;
  const t = parseInt(raw, 10);
  if (!(t >= 1 && t <= SLOT_COUNT) || t === n) { toast('잘못된 슬롯 번호'); return; }
  if (info[t] && !confirm(`슬롯 ${t}에 이미 있어요. 덮어쓸까요?`)) return;
  const bytes = player.slotBytes(n);
  if (!bytes) { toast('빈 슬롯'); return; }
  if (player.saveSlotBytes(t, bytes)) { player.deleteSlot(n); renderSlots(); toast(`슬롯 ${n} → ${t}`); }
  else toast('이동 실패');
}

// ── 저장 파일 (서버 동기화) ─────────────────────────
function fmtSize(n) { return n >= 1024 ? `${Math.round(n / 1024)}KB` : `${n}B`; }

// 저장 데이터 관리(상태저장 서버 동기화) 모달 렌더
function renderStateSync() {
  const rom = playing ? player.currentRomName() : '';
  $('#cur-game').textContent = rom
    ? `실행 중: ${labelOf(rom)}`
    : '게임을 실행하면 이 게임의 상태를 저장/불러올 수 있어요.';
  $('#btn-upload-state').disabled = !playing;
  renderServerStates();
}

// 지금 상태(save-state)를 서버에 업로드
async function uploadCurrentState() {
  if (!playing) { toast('게임 실행 중에 저장하세요'); return; }
  const bytes = player.captureStateBytes();
  if (!bytes || !bytes.length) { toast('상태 캡처 실패'); return; }
  const d = new Date();
  const p2 = (x) => String(x).padStart(2, '0');
  const stamp = `${p2(d.getFullYear() % 100)}${p2(d.getMonth() + 1)}${p2(d.getDate())}${p2(d.getHours())}${p2(d.getMinutes())}`;
  const raw = prompt('서버에 저장할 이름:', stamp);
  if (!raw) return;
  const name = sanitizeName(raw);
  try {
    await uploadServerSave(name, player.currentRomName(), bytes);
    toast('서버에 저장됨: ' + name);
    await renderServerStates();
  } catch (e) { alert(e.message); }
}

async function renderServerStates() {
  const ul = $('#server-saves');
  ul.innerHTML = '<li class="empty-sm">불러오는 중…</li>';
  let saves;
  try {
    saves = await listServerSaves();
  } catch (e) {
    ul.innerHTML = `<li class="empty-sm">${e.message}</li>`;
    return;
  }
  ul.innerHTML = '';
  if (!saves.length) {
    ul.innerHTML = '<li class="empty-sm">서버에 저장된 상태가 없어요.</li>';
    return;
  }
  const curRom = playing ? player.currentRomName() : '';
  for (const s of saves) {
    const li = document.createElement('li');
    li.className = 'file-row';
    const finfo = document.createElement('div');
    finfo.className = 'file-info';
    const nm = document.createElement('span');
    nm.className = 'file-name';
    nm.textContent = s.origin ? `${s.name} · ${labelOf(s.origin)}` : s.name;
    finfo.append(nm);
    const when = fmtSlotTime(s.mtime);
    if (when) {
      const w = document.createElement('span');
      w.className = 'file-when';
      w.textContent = when;
      finfo.append(w);
    }

    // 불러오기: 같은 게임 실행 중일 때만 그 순간으로 즉시 로드
    const sameGame = playing && s.origin === curRom;
    const get = document.createElement('button');
    get.textContent = '불러오기';
    get.disabled = !sameGame;
    get.title = sameGame ? '이 순간으로 즉시 불러오기'
      : (playing ? '다른 게임의 상태예요' : '먼저 이 게임을 실행하세요');
    get.addEventListener('click', async () => {
      get.disabled = true; get.textContent = '받는 중…';
      try {
        const { bytes } = await downloadServerSave(s.name);
        if (player.loadStateBytes(bytes)) {
          closeModal('savefiles');
          toast('그 순간으로 불러왔어요');
        } else { toast('불러오기 실패'); get.disabled = false; get.textContent = '불러오기'; }
      } catch (e) { alert(e.message); get.disabled = false; get.textContent = '불러오기'; }
    });

    const dl = document.createElement('button');
    dl.textContent = '기기에 저장';
    dl.addEventListener('click', async () => {
      try { const { bytes } = await downloadServerSave(s.name); downloadToDevice(s.name + '.ss', bytes); }
      catch (e) { alert(e.message); }
    });

    const del = document.createElement('button');
    del.className = 'danger-btn';
    del.textContent = '✕';
    del.title = '서버에서 삭제';
    del.addEventListener('click', async () => {
      if (!confirm(`서버에서 "${s.name}" 삭제할까요?`)) return;
      try { await deleteServerSave(s.name); await renderServerStates(); }
      catch (e) { alert(e.message); }
    });

    li.append(finfo, get, dl, del);
    ul.appendChild(li);
  }
}

function launch(name) {
  if (!player.launch(name)) {
    alert(`${labelOf(name)} 실행에 실패했어요.\n파일이 손상됐거나 지원하지 않는 롬일 수 있습니다.`);
    return;
  }
  playing = true;
  document.body.classList.add('playing');
  localStorage.setItem('last-rom', name);   // 탭 사망 대비 (이어하기)
  startServerSync();
  ffMode = 'off';
  userPaused = false;
  applyFf();
  const pb = $('#btn-pause');
  pb.classList.remove('on');
  pb.textContent = '⏸';
}

async function backToLibrary() {
  await player.quit();
  playing = false;
  document.body.classList.remove('playing');
  ffMode = 'off';
  userPaused = false;
  $('#btn-ff').classList.remove('on');
  $('#btn-ff2').classList.remove('on');
  $('#btn-pause').classList.remove('on');
  $('#btn-pause').textContent = '⏸';
  stopServerSync();
  localStorage.removeItem('last-rom');   // 정상 종료 → 이어하기 해제
  await renderLibrary();
}

function labelOf(name) {
  return name.replace(/\.(gba|gbc|gb|zip|7z)$/i, '');
}

let toastTimer;
function toast(msg) {
  const t = $('#toast');
  t.textContent = msg;
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 1600);
}

boot();
