// touch.js — 화면 위 가상 게임패드 → buttonPress/buttonUnpress (멀티터치 지원)
//
// 일반 버튼: 누르는 동안만 입력.
// L/R(범퍼): 오른쪽으로 슬라이드하면 '고정'(계속 누름), 왼쪽으로 슬라이드하면 해제.
import { getModule, resumeAudio } from './engine.js';

const SLIDE_TH = 24;   // 슬라이드 판정 임계 (px)

function haptic(pattern) {
  if (localStorage.getItem('haptic') === '1' && navigator.vibrate) navigator.vibrate(pattern);
}

export function initTouchControls(root) {
  root.querySelectorAll('[data-input]').forEach((btn) => {
    const input = btn.dataset.input;
    const lockable = btn.classList.contains('bumper');   // L / R 만 고정 지원

    const doPress = () => {
      btn.classList.add('active');
      resumeAudio();
      haptic(8);
      getModule().buttonPress(input);
    };
    const doRelease = () => {
      btn.classList.remove('active');
      getModule().buttonUnpress(input);
    };

    btn.addEventListener('contextmenu', (e) => e.preventDefault());

    if (!lockable) {
      // ── 일반 버튼 ──
      const press = (e) => { e.preventDefault(); doPress(); };
      const release = (e) => { e.preventDefault(); if (btn.classList.contains('active')) doRelease(); };
      btn.addEventListener('pointerdown', press);
      btn.addEventListener('pointerup', release);
      btn.addEventListener('pointercancel', release);
      btn.addEventListener('pointerleave', (e) => { if (btn.classList.contains('active')) release(e); });
      return;
    }

    // ── L / R (슬라이드 고정) ──
    let startX = 0;
    let sliding = false;

    btn.addEventListener('pointerdown', (e) => {
      e.preventDefault();
      startX = e.clientX;
      sliding = true;
      btn.setPointerCapture?.(e.pointerId);
      if (!btn.classList.contains('locked')) doPress();   // 고정 아니면 누르기 시작
      // 이미 고정된 상태면 눌림 유지(해제 슬라이드 대기)
    });
    btn.addEventListener('pointermove', (e) => {
      if (!sliding) return;
      const dx = e.clientX - startX;
      if (!btn.classList.contains('locked') && dx > SLIDE_TH) {
        btn.classList.add('locked');        // → 오른쪽 슬라이드: 고정
        haptic([8, 30, 8]);
      } else if (btn.classList.contains('locked') && dx < -SLIDE_TH) {
        btn.classList.remove('locked');      // ← 왼쪽 슬라이드: 해제
        doRelease();
        sliding = false;
        haptic(8);
      }
    });
    const endGesture = () => {
      if (!sliding) return;
      sliding = false;
      // 고정 상태면 계속 누름 유지, 아니면 뗀다.
      if (!btn.classList.contains('locked')) doRelease();
    };
    btn.addEventListener('pointerup', endGesture);
    btn.addEventListener('pointercancel', endGesture);
  });
}
