// app.js — 화면 전환(라이브러리 ↔ 플레이)과 UI 연결
import { initCore, persist } from './engine.js';
import {
  listLocalRoms, addRomFiles, deleteRom, fetchServerShelf, importServerRom,
  listSaveFiles, readSaveFile, writeSaveFile,
} from './library.js';
import {
  listServerSaves, uploadServerSave, downloadServerSave, deleteServerSave,
} from './server-saves.js';
import * as player from './player.js';
import { initTouchControls } from './touch.js';

const $ = (sel) => document.querySelector(sel);
let playing = false;

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
  $('#btn-ff').addEventListener('click', (e) => {
    e.currentTarget.classList.toggle('on', player.toggleFastForward());
  });
  $('#btn-fs').addEventListener('click', () => {
    if (document.fullscreenElement) document.exitFullscreen();
    else $('#stage').requestFullscreen?.();
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
  });
  const tokenInput = $('#set-token');
  tokenInput.value = localStorage.getItem('save-token') || '';
  tokenInput.addEventListener('change', () => {
    localStorage.setItem('save-token', tokenInput.value.trim());
  });

  // 라이브러리: 설정 버튼(게임 진입 전에도 접근)
  $('#btn-lib-settings').addEventListener('click', () => openModal('settings'));
  // 설정 > 저장 데이터 관리 → 관리 모달 열기
  $('#btn-savedata').addEventListener('click', () => {
    closeModal('settings');
    renderSaveFiles();
    openModal('savefiles');
  });

  // 설정: 햅틱 / 배속 / 자동 상태저장 / 서버 자동동기화 (localStorage 연동)
  bindToggle('#set-haptic', 'haptic');
  bindSelect('#set-ffspeed', 'ff-speed', '2');
  bindToggle('#set-autostate', 'autostate', true, () => { if (playing) player.applyAutoSaveSettings(); });
  bindSelect('#set-autostate-min', 'autostate-min', '1', () => { if (playing) player.applyAutoSaveSettings(); });
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
  player.setRunning(!document.hidden && !anyModalOpen());
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
  const bytes = player.currentSave();
  const name = player.currentSaveName();
  if (!bytes || !bytes.length || !name) return;
  try {
    await uploadServerSave(name, name, bytes);
    console.log('[emu] 자동 서버동기화 완료:', name);
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

async function renderLibrary() {
  renderResume();
  // 서버 선반과 로컬 롬을 하나의 목록으로 합친다.
  // 표시 이름은 매니페스트(roms.json)의 name, 저장/실행은 ascii 파일명(file).
  const shelf = await fetchServerShelf();
  const nameByFile = new Map(shelf.map((e) => [e.file, e.name || e.file]));
  const local = listLocalRoms();
  const localSet = new Set(local);

  const items = [
    ...local.map((file) => ({
      file, label: nameByFile.get(file) || labelOf(file), local: true,
    })),
    ...shelf.filter((e) => !localSet.has(e.file)).map((e) => ({
      file: e.file, label: e.name || labelOf(e.file), local: false, entry: e,
    })),
  ];

  const list = $('#rom-list');
  list.innerHTML = '';
  if (!items.length) {
    list.innerHTML =
      '<li class="empty">아직 롬이 없어요. 파일을 끌어다 놓거나 위에서 올리세요.</li>';
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
      it.local ? launch(it.file) : importAndPlay(it.entry));

    const del = document.createElement('button');
    del.className = 'rom-del';
    if (it.local) {
      del.title = '삭제';
      del.textContent = '✕';
      del.addEventListener('click', async () => {
        if (confirm(`${it.label} 을(를) 라이브러리에서 지울까요?\n(세이브 파일은 남습니다)`)) {
          await deleteRom(it.file);
          await renderLibrary();
        }
      });
    } else {
      del.title = '서버에서 받기';
      del.textContent = '⭳';
      del.addEventListener('click', () => importAndPlay(it.entry));
    }

    li.append(play, del);
    list.appendChild(li);
  }
}

// 서버 선반 롬: 받아서(로컬 임포트) 곧바로 실행. (목록 클릭 시)
async function importAndPlay(entry) {
  toast('서버에서 받는 중…');
  try {
    console.log('[emu] 서버 임포트:', entry.file);
    await importServerRom(entry);
    await renderLibrary();
    launch(entry.file);
  } catch (e) {
    console.warn('[emu] 임포트 실패:', e);
    alert(e.message);
  }
}

// ── 모달 / 세이브 슬롯 ─────────────────────────────
const SLOT_COUNT = 6;
function openModal(id) {
  $('#' + id).classList.add('show');
  updateRunState();   // 모달 열림 → 일시정지 + 입력 차단
}
function closeModal(id) {
  $('#' + id).classList.remove('show');
  updateRunState();   // 모달 닫힘 → (다른 모달 없으면) 재개
}

// 상태 저장 슬롯 목록 렌더 (채워진 슬롯은 점 표시 · 빈 슬롯은 불러오기 비활성)
function renderSlots() {
  const filled = player.filledStateSlots();
  const ul = $('#slot-list');
  ul.innerHTML = '';
  for (let n = 1; n <= SLOT_COUNT; n++) {
    const has = filled.has(n);
    const li = document.createElement('li');
    li.className = 'slot-row';

    const label = document.createElement('span');
    label.className = 'slot-label';
    label.textContent = `슬롯 ${n}`;
    const mark = document.createElement('span');
    if (has) { mark.className = 'slot-dot'; }
    else { mark.className = 'slot-empty'; mark.textContent = '비어있음'; }
    label.append(mark);

    const save = document.createElement('button');
    save.className = 'slot-save';
    save.textContent = '저장';
    save.addEventListener('click', async () => {
      const ok = await player.saveState(n);
      renderSlots();
      toast(ok ? `슬롯 ${n}에 저장됨` : '저장 실패');
    });

    const load = document.createElement('button');
    load.className = 'slot-load';
    load.textContent = '불러오기';
    load.disabled = !has;
    load.addEventListener('click', () => {
      if (player.loadState(n)) { closeModal('saves'); toast(`슬롯 ${n} 불러옴`); }
      else toast('빈 슬롯이에요');
    });

    li.append(label, save, load);
    ul.appendChild(li);
  }
}

// ── 저장 파일 (서버 동기화) ─────────────────────────
function fmtSize(n) { return n >= 1024 ? `${Math.round(n / 1024)}KB` : `${n}B`; }

async function renderSaveFiles() {
  const ul = $('#local-saves');
  ul.innerHTML = '';
  const locals = listSaveFiles();
  if (!locals.length) {
    ul.innerHTML = '<li class="empty-sm">세이브 파일이 아직 없어요. (게임을 저장하면 생겨요)</li>';
  }
  for (const f of locals) {
    const li = document.createElement('li');
    li.className = 'file-row';
    const nm = document.createElement('span');
    nm.className = 'file-name';
    nm.textContent = `${f.name} · ${fmtSize(f.size)}`;
    const up = document.createElement('button');
    up.textContent = '서버에 올리기';
    up.addEventListener('click', async () => {
      const name = prompt('서버에 저장할 이름:', f.name);
      if (!name) return;
      up.disabled = true; up.textContent = '올리는 중…';
      try {
        await uploadServerSave(name.trim(), f.name, readSaveFile(f.path));
        toast('서버에 저장됨: ' + name.trim());
        await renderServerSaves();
      } catch (e) { alert(e.message); }
      up.disabled = false; up.textContent = '서버에 올리기';
    });
    const dl = document.createElement('button');
    dl.textContent = '기기에 저장';
    dl.addEventListener('click', () => downloadToDevice(f.name, readSaveFile(f.path)));
    li.append(nm, up, dl);
    ul.appendChild(li);
  }
  await renderServerSaves();
}

async function renderServerSaves() {
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
    ul.innerHTML = '<li class="empty-sm">서버에 저장된 세이브가 없어요.</li>';
    return;
  }
  for (const s of saves) {
    const li = document.createElement('li');
    li.className = 'file-row';
    const nm = document.createElement('span');
    nm.className = 'file-name';
    nm.textContent = s.origin ? `${s.name}  → ${s.origin}` : s.name;
    const get = document.createElement('button');
    get.textContent = '받기';
    get.addEventListener('click', async () => {
      get.disabled = true; get.textContent = '받는 중…';
      try {
        const { bytes, origin } = await downloadServerSave(s.name);
        const target = origin || s.origin || s.name;
        await writeSaveFile(target, bytes);
        toast(`받아서 저장함: ${target}`);
        renderSaveFiles();
      } catch (e) {
        alert(e.message);
        get.disabled = false; get.textContent = '받기';
      }
    });
    const del = document.createElement('button');
    del.className = 'danger-btn';
    del.textContent = '✕';
    del.title = '서버에서 삭제';
    del.addEventListener('click', async () => {
      if (!confirm(`서버에서 "${s.name}" 삭제할까요?`)) return;
      try { await deleteServerSave(s.name); await renderServerSaves(); }
      catch (e) { alert(e.message); }
    });
    li.append(nm, get, del);
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
}

async function backToLibrary() {
  await player.quit();
  playing = false;
  document.body.classList.remove('playing');
  $('#btn-ff').classList.remove('on');
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
