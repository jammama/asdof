/*
 * 자가진단 설문 전체 '그렇다' 체크 + 진행 버튼 클릭 북마크렛
 *
 * - 페이지 안의 모든 라디오 그룹(name 기준)을 찾아, 라벨 텍스트가 긍정 답변인
 *   보기를 골라 클릭한다. 문항 개수/이름에 의존하지 않으므로 다음 페이지에서도
 *   같은 스크립트를 그대로 쓸 수 있다.
 * - display:none 으로 숨어 있는 문항(예: 2페이지 문항)도 함께 체크한다.
 * - 이미 선택된 그룹은 건드리지 않는다.
 * - 체크가 끝나면 화면에 보이는 '다음' 버튼을(없으면 '제출' 버튼을) 눌러 진행한다.
 */
(function () {
	var POSITIVE = /그렇다|그렇습니다|예\b|^예$|네\b|있다|동의/;
	var NEGATIVE = /아니|그렇지\s*않|않다|없다|비동의|모름|해당\s*없/;
	var NEXT = /다음|계속|next/i;
	var SUBMIT = /제출|완료|결과\s*보기|확인하기|submit/i;
	var NOT_A_BUTTON = /이전|뒤로|취소|목록|처음|다시|닫기|이전글|다음글/;

	function labelTextOf(input) {
		var text = '';
		if (input.id) {
			var forLabel = document.querySelector('label[for="' + input.id.replace(/"/g, '\\"') + '"]');
			if (forLabel) text = forLabel.textContent;
		}
		if (!text) {
			var wrapLabel = input.closest('label');
			if (wrapLabel) text = wrapLabel.textContent;
		}
		if (!text) {
			var cell = input.closest('li,td,div,span,p');
			if (cell) text = cell.textContent;
		}
		return (text || input.value || '').replace(/\s+/g, ' ').trim();
	}

	// 점수가 높을수록 '그렇다'에 가까운 보기
	function score(text) {
		if (NEGATIVE.test(text)) return 0;
		if (text === '그렇다') return 3;
		if (/그렇다/.test(text)) return 2;   // '매우 그렇다', '대체로 그렇다' 등
		if (POSITIVE.test(text)) return 1;
		return 0;
	}

	function isVisible(el) {
		return !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
	}

	function textOf(el) {
		return ((el.tagName === 'INPUT' ? el.value : el.textContent) || '').replace(/\s+/g, ' ').trim();
	}

	// anchor 에서 위로 올라가며 가장 가까운 진행 버튼을 찾는다(푸터/네비 오탐 방지)
	function findButton(anchor, pattern) {
		var scope = anchor;
		while (scope) {
			var hit = null;
			scope.querySelectorAll('a,button,input[type=button],input[type=submit]').forEach(function (el) {
				if (hit || el.disabled || !isVisible(el)) return;
				var t = textOf(el);
				if (!t || NOT_A_BUTTON.test(t) || !pattern.test(t)) return;
				hit = el;
			});
			if (hit) return hit;
			scope = scope.parentElement;
		}
		return null;
	}

	var groups = new Map();
	document.querySelectorAll('input[type=radio][name]').forEach(function (input) {
		if (input.disabled) return;
		if (!groups.has(input.name)) groups.set(input.name, []);
		groups.get(input.name).push(input);
	});

	var checked = 0, already = 0, skipped = [], lastPicked = null;

	groups.forEach(function (inputs, name) {
		if (inputs.some(function (i) { return i.checked; })) {
			already++;
			lastPicked = lastPicked || inputs[0];
			return;
		}

		var best = null, bestScore = 0;
		inputs.forEach(function (input) {
			var s = score(labelTextOf(input));
			if (s > bestScore) { bestScore = s; best = input; }
		});

		// 라벨로 못 고른 경우: 보기가 2개면 value='1'(대개 '그렇다')을 대안으로
		if (!best && inputs.length === 2) {
			best = inputs.filter(function (i) { return i.value === '1'; })[0] || null;
		}

		if (!best) { skipped.push(name); return; }

		best.click();                                     // 사이트 이벤트 핸들러까지 태우기
		if (!best.checked) {                              // click 이 먹히지 않은 경우 대비
			best.checked = true;
			best.dispatchEvent(new Event('input', { bubbles: true }));
			best.dispatchEvent(new Event('change', { bubbles: true }));
		}
		checked++;
		lastPicked = best;
	});

	var msg = '✅ ' + checked + '개 문항 체크';
	if (already) msg += ' (이미 선택됨 ' + already + ')';
	if (skipped.length) msg += ' ⚠️ 판단 불가: ' + skipped.join(', ');
	if (!groups.size) msg = '⚠️ 라디오 문항을 찾지 못했습니다';

	// 진행 버튼: 보이는 '다음'이 있으면 그것, 없으면 '제출'
	var anchor = (lastPicked && lastPicked.closest('form,section,article,div')) || document.body;
	var button = findButton(anchor, NEXT) || findButton(anchor, SUBMIT);

	function toast(text) {
		var el = document.getElementById('__yesToast') || document.createElement('div');
		el.id = '__yesToast';
		el.textContent = text;
		el.setAttribute('style', 'position:fixed;left:50%;top:20px;transform:translateX(-50%);' +
			'z-index:2147483647;background:rgba(17,24,39,.94);color:#fff;font:14px/1.5 -apple-system,BlinkMacSystemFont,"Apple SD Gothic Neo",sans-serif;' +
			'padding:10px 16px;border-radius:8px;box-shadow:0 4px 16px rgba(0,0,0,.3);max-width:80vw;text-align:center');
		document.body.appendChild(el);
		clearTimeout(window.__yesToastTimer);
		window.__yesToastTimer = setTimeout(function () { el.remove(); }, 3000);
	}

	if (!button) {
		toast(msg + ' · 진행 버튼 없음');
		return;
	}

	toast(msg + " → '" + textOf(button).slice(0, 12) + "' 클릭");
	// 사이트의 change 핸들러가 끝난 뒤 누르도록 한 틱 미룬다
	setTimeout(function () { button.click(); }, 150);
})();
