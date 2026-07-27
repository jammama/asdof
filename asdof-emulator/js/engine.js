// engine.js — mGBA wasm 코어 로드/초기화 및 영속화 헬퍼
//
// 코어(@thenick775/mgba-wasm)는 ES 모듈로, `mGBA({canvas})` 를 호출하면
// Emscripten 모듈(Module)을 반환한다. Module 은 캔버스 영상 출력·키보드 입력·
// 오디오·IDBFS(IndexedDB 파일시스템)를 자체적으로 처리하고, 명령형 API 를 노출한다.
//
// 이 코어는 pthread(스레드)를 쓰므로 cross-origin isolation 이 반드시 필요하다.
// (COOP: same-origin + COEP: require-corp) — 로컬은 serve.py, 배포는 nginx 설정 참고.
import mGBA from '../core/mgba.js';

let mod = null;
let initPromise = null;

export function getModule() {
  return mod;
}

// 코어를 캔버스에 한 번 초기화한다. 여러 번 불러도 최초 1회만 실행.
export function initCore(canvas) {
  if (initPromise) return initPromise;
  initPromise = (async () => {
    if (!self.crossOriginIsolated) {
      throw new Error(
        'cross-origin isolation 이 꺼져 있어요. 이 코어는 스레드(SharedArrayBuffer)를 ' +
        '써서 COOP(same-origin)/COEP(require-corp) 헤더가 필요합니다. ' +
        '로컬은 ./serve.py 로 실행하고, 배포는 nginx-emulator.conf 를 적용하세요.'
      );
    }
    const m = await mGBA({ canvas });
    await m.FSInit();               // IDBFS 마운트 + 저장돼 있던 롬/세이브 로드
    // 기본은 입력 OFF: 코어가 켜져 있으면 키다운을 preventDefault 해서 라이브러리/설정
    // 텍스트칸(토큰 등)에 타이핑이 안 됨. 게임 로드/모달종료 시점에만 켠다.
    try { m.toggleInput(false); } catch (e) { console.warn('toggleInput(false) 실패', e); }
    // 볼륨/키바인딩은 게임 로드 후에 적용(로드 전 crash 회피)
    mod = m;
    console.log('[emu] 코어 준비 완료', m.version);
    return m;
  })();
  return initPromise;
}

// /data 마운트는 autoPersist 가 아니므로 명시적으로 IndexedDB 에 써줘야 한다.
// (업로드/삭제/상태저장 직후, 그리고 플레이 중 주기적으로 호출)
export async function persist() {
  if (!mod) return;
  try {
    await mod.FSSync();
  } catch (e) {
    console.warn('FSSync 실패:', e);
  }
}

// 오디오 컨텍스트는 브라우저 정책상 사용자 제스처가 있어야 재개된다.
// (코어의 SDL2 오디오 컨텍스트를 다루므로 엔진 레벨 헬퍼)
export function resumeAudio() {
  const ctx = mod?.SDL2?.audioContext;
  if (ctx && ctx.state === 'suspended') ctx.resume();
}
