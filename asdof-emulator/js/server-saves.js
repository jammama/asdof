// server-saves.js — asdof-saves(외부 범용 세이브 스토리지) 클라이언트.
//
// 별도 서비스(saves.asdof.xyz)를 호출한다 — 에뮬레이터에 백엔드를 박지 않는다.
//   base URL : localStorage('saves-url')  (기본 https://saves.asdof.xyz)
//   토큰      : localStorage('save-token')
//   네임스페이스: 'asdof-emulator'  (다른 앱/게임은 각자 다른 ns 사용)
// 크로스 오리진이지만 서비스가 CORS 를 주므로 cross-origin isolated 페이지에서도 동작.
const NS = 'asdof-emulator';
const DEFAULT_BASE = 'https://saves.asdof.xyz';

function apiRoot() {
  const base = (localStorage.getItem('saves-url') || DEFAULT_BASE).replace(/\/+$/, '');
  return `${base}/v1/${NS}/saves`;
}

function authHeaders(extra = {}) {
  const t = localStorage.getItem('save-token') || '';
  return t ? { 'X-Token': t, ...extra } : { ...extra };
}

function safeDecode(s) {
  try { return decodeURIComponent(s); } catch { return s; }
}

export async function listServerSaves() {
  const res = await fetch(apiRoot(), { headers: authHeaders() });
  if (res.status === 401) throw new Error('서버 토큰이 없거나 틀렸어요. 설정 > 서버에서 토큰을 입력하세요.');
  if (!res.ok) throw new Error(`서버 목록 실패 (HTTP ${res.status})`);
  const data = await res.json();
  return (data.saves || []).map((s) => ({ ...s, origin: safeDecode(s.origin || '') }));
}

export async function uploadServerSave(name, originName, bytes) {
  const res = await fetch(`${apiRoot()}/${encodeURIComponent(name)}`, {
    method: 'POST',
    headers: authHeaders({ 'X-Origin-Name': encodeURIComponent(originName || '') }),
    body: bytes,
  });
  if (res.status === 401) throw new Error('서버 토큰 오류 — 설정 > 서버에서 확인하세요.');
  if (!res.ok) throw new Error(`업로드 실패 (HTTP ${res.status})`);
}

export async function downloadServerSave(name) {
  const res = await fetch(`${apiRoot()}/${encodeURIComponent(name)}`, { headers: authHeaders() });
  if (!res.ok) throw new Error(`다운로드 실패 (HTTP ${res.status})`);
  const origin = res.headers.get('X-Origin-Name');
  const bytes = new Uint8Array(await res.arrayBuffer());
  return { bytes, origin: origin ? safeDecode(origin) : '' };
}

export async function deleteServerSave(name) {
  const res = await fetch(`${apiRoot()}/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: authHeaders(),
  });
  if (!res.ok) throw new Error(`삭제 실패 (HTTP ${res.status})`);
}
