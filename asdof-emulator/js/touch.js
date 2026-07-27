// touch.js — 화면 위 가상 게임패드 → buttonPress/buttonUnpress (멀티터치 지원)
//
// 각 버튼에 data-input="a|b|start|select|up|down|left|right|l|r" 가 붙어 있고,
// 포인터 이벤트로 눌림/뗌을 코어에 전달한다. 터치는 포인터마다 독립적으로
// 대상 요소에 이벤트가 가므로 멀티터치(방향 + A 동시)가 자연스럽게 된다.
import { getModule, resumeAudio } from './engine.js';

export function initTouchControls(root) {
  root.querySelectorAll('[data-input]').forEach((btn) => {
    const input = btn.dataset.input;

    const press = (e) => {
      e.preventDefault();
      btn.classList.add('active');
      resumeAudio();
      // 햅틱(설정 토글, 안드로이드 크롬 등에서만 동작)
      if (localStorage.getItem('haptic') === '1' && navigator.vibrate) navigator.vibrate(8);
      getModule().buttonPress(input);
    };
    const release = (e) => {
      e.preventDefault();
      btn.classList.remove('active');
      getModule().buttonUnpress(input);
    };

    btn.addEventListener('pointerdown', press);
    btn.addEventListener('pointerup', release);
    btn.addEventListener('pointercancel', release);
    // 손가락이 버튼을 벗어나면 뗌 처리 (방향키에서 흘러나가는 경우)
    btn.addEventListener('pointerleave', (e) => {
      if (btn.classList.contains('active')) release(e);
    });
    // 컨텍스트 메뉴(길게 누르기) 방지
    btn.addEventListener('contextmenu', (e) => e.preventDefault());
  });
}
