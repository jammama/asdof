// server-saves.js — 서버 세이브 저장소 API 클라이언트 (/api/saves)
//
// 토큰은 localStorage('save-token') 에서 읽는다(설정 > 서버에서 입력).
// 이름/원본파일명은 비ASCII(한글) 대비 URI 인코딩해서 주고받는다.
const API = '/api/saves';

function authHeaders(extra = {}) {
  const t = localStorage.getItem('save-token') || '';
  return t ? { 'X-Token': t, ...extra } : { ...extra };
}

function safeDecode(s) {
  try { return decodeURIComponent(s); } catch { return s; }
}

export async function listServerSaves() {
  const res = await fetch(API, { headers: authHeaders() });
  if (res.status === 401) throw new Error('서버 토큰이 없거나 틀렸어요. 설정 > 서버에서 토큰을 입력하세요.');
  if (!res.ok) throw new Error(`서버 목록 실패 (HTTP ${res.status})`);
  const data = await res.json();
  return (data.saves || []).map((s) => ({ ...s, origin: safeDecode(s.origin || '') }));
}

export async function uploadServerSave(name, originName, bytes) {
  const res = await fetch(`${API}/${encodeURIComponent(name)}`, {
    method: 'POST',
    headers: authHeaders({ 'X-Origin-Name': encodeURIComponent(originName || '') }),
    body: bytes,
  });
  if (res.status === 401) throw new Error('서버 토큰 오류 — 설정 > 서버에서 확인하세요.');
  if (!res.ok) throw new Error(`업로드 실패 (HTTP ${res.status})`);
}

export async function downloadServerSave(name) {
  const res = await fetch(`${API}/${encodeURIComponent(name)}`, { headers: authHeaders() });
  if (!res.ok) throw new Error(`다운로드 실패 (HTTP ${res.status})`);
  const origin = res.headers.get('X-Origin-Name');
  const bytes = new Uint8Array(await res.arrayBuffer());
  return { bytes, origin: origin ? safeDecode(origin) : '' };
}

export async function deleteServerSave(name) {
  const res = await fetch(`${API}/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: authHeaders(),
  });
  if (!res.ok) throw new Error(`삭제 실패 (HTTP ${res.status})`);
}
