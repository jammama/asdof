// app.js — 화면 전환(라이브러리 ↔ 플레이)과 UI 연결
import { initCore, persist } from './engine.js';
import {
  listLocalRoms, addRomFiles, deleteRom, fetchServerShelf, importServerRom,
} from './library.js';
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
  initTouchControls($('#stage'));
  wireUi();
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
  $('#btn-save').addEventListener('click', async () => {
    toast((await player.saveState(1)) ? '상태 저장됨 (슬롯1)' : '상태 저장 실패');
  });
  $('#btn-load').addEventListener('click', () => {
    toast(player.loadState(1) ? '상태 불러옴 (슬롯1)' : '저장된 상태 없음');
  });
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
  $('#btn-settings').addEventListener('click', () => $('#settings').classList.add('show'));
  $('#settings-close').addEventListener('click', () => $('#settings').classList.remove('show'));
  $('#settings').addEventListener('click', (e) => {
    if (e.target.id === 'settings') $('#settings').classList.remove('show');   // 배경 클릭 시 닫기
  });
  // 설정 > 화면 > 게임패드: 체크=표시(기본), 해제=숨김
  $('#set-gamepad').addEventListener('change', (e) => {
    document.body.classList.toggle('no-pad', !e.target.checked);
  });

  document.addEventListener('visibilitychange', () => {
    if (!playing) return;
    if (document.hidden) player.pause();
    else player.resume();
  });
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

async function renderLibrary() {
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

function launch(name) {
  if (!player.launch(name)) {
    alert(`${labelOf(name)} 실행에 실패했어요.\n파일이 손상됐거나 지원하지 않는 롬일 수 있습니다.`);
    return;
  }
  playing = true;
  document.body.classList.add('playing');
}

async function backToLibrary() {
  await player.quit();
  playing = false;
  document.body.classList.remove('playing');
  $('#btn-ff').classList.remove('on');
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
