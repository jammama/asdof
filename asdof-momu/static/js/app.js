/* asdof-momu — 회사 식당 방문 기록. 카카오맵으로 찾아 등록하고, 갈 때마다 뭘 먹었는지 남긴다. */
(function () {
  'use strict';

  // ---------- 상수 ----------
  var CATEGORIES = ['한식', '중식', '일식', '양식', '분식', '고기', '치킨', '카페·디저트', '술집', '아시안', '패스트푸드', '기타'];
  var DIST_STEPS = [
    { key: 0, label: '전체' },
    { key: 300, label: '300m' },
    { key: 500, label: '500m' },
    { key: 1000, label: '1km' },
    { key: 2000, label: '2km' },
    { key: 5000, label: '5km' }
  ];
  var SORTS = [
    { key: 'recent', label: '최근 방문' },
    { key: 'dist', label: '가까운 순' },
    { key: 'name', label: '이름' },
    { key: 'count', label: '방문 많은 순' },
    { key: 'rating', label: '평점' }
  ];
  // 회사 = 팁스타운 S5 (서울 강남구 역삼로 177). 거리 기준점의 기본값이자 첫 지도 중심.
  var OFFICE = { lat: 37.4958317756805, lng: 127.038470950073, label: '회사(팁스타운 S5)' };
  var DEFAULT_CENTER = OFFICE;
  // '최근 N일 안에 간 곳은 후보에서 뺀다' — 같은 집 연달아 가는 걸 막는 게 핵심
  var REC_DAYS = [
    { key: 0, label: '안 뺌' },
    { key: 3, label: '3일' },
    { key: 7, label: '1주' },
    { key: 14, label: '2주' },
    { key: 30, label: '한달' }
  ];
  var PW_KEY = 'momu.pw';

  // ---------- 상태 ----------
  var S = {
    all: [],            // 등록된 식당 전체
    view: [],           // 필터/정렬 적용 결과
    tab: 'mine',
    activeId: null,
    kakaoResults: [],
    pw: localStorage.getItem(PW_KEY) || '',
    map: null,
    clusterer: null,
    markers: {},        // restaurant id -> Marker
    kMarkers: [],       // 카카오 검색결과 마커
    overlay: null,
    origin: { lat: OFFICE.lat, lng: OFFICE.lng }, // 거리 기준점 (null = 지도 중심을 따라간다)
    originKind: 'office',                         // 'office' | 'geo' | 'center'
    filters: { cats: {}, dist: 0, sort: 'recent', bounds: false, novisit: false },
    q: '',
    // 오늘 뭐 먹지 — 점심에 걸어갈 만한 거리·1주 안에 안 간 곳이 기본값
    rec: { dist: 500, cats: {}, days: 7, picked: null, spinning: false },
    // 공무원 픽 — 자치구별 업무추진비 집계 (data/*-spending.json)
    gov: { district: 'gangnam', cache: {}, data: null,
           dist: 0, band: '', sort: 'count', q: '', picked: null }
  };

  // ---------- 유틸 ----------
  function $(sel) { return document.querySelector(sel); }
  function el(tag, cls, html) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html != null) n.innerHTML = html;
    return n;
  }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function toast(msg, isErr) {
    var t = $('#toast');
    t.textContent = msg;
    t.className = isErr ? 'err' : '';
    clearTimeout(toast._t);
    toast._t = setTimeout(function () { t.className = 'hidden'; }, isErr ? 3800 : 2200);
  }

  // 하버사인 거리(m)
  function distM(a, b) {
    if (!a || !b) return null;
    var R = 6371000, rad = Math.PI / 180;
    var dLat = (b.lat - a.lat) * rad, dLng = (b.lng - a.lng) * rad;
    var la1 = a.lat * rad, la2 = b.lat * rad;
    var h = Math.sin(dLat / 2) * Math.sin(dLat / 2) +
            Math.cos(la1) * Math.cos(la2) * Math.sin(dLng / 2) * Math.sin(dLng / 2);
    return 2 * R * Math.asin(Math.sqrt(h));
  }
  function fmtDist(m) {
    if (m == null) return '';
    return m < 1000 ? Math.round(m) + 'm' : (m / 1000).toFixed(m < 10000 ? 1 : 0) + 'km';
  }
  function fmtWon(n) { return n ? n.toLocaleString('ko-KR') + '원' : ''; }
  function stars(n) { return n ? '★'.repeat(n) + '☆'.repeat(5 - n) : ''; }
  function today() {
    var d = new Date();
    return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
  }

  function originLabel() {
    return S.originKind === 'office' ? OFFICE.label
      : S.originKind === 'geo' ? '내 위치' : '지도 중심';
  }

  // 오늘로부터 며칠 지났나 (YYYY-MM-DD)
  function daysSince(dstr) {
    if (!dstr) return null;
    var then = new Date(dstr + 'T00:00:00');
    if (isNaN(then.getTime())) return null;
    return Math.floor((new Date(today() + 'T00:00:00') - then) / 86400000);
  }

  // 기준점 — 사용자가 고정했으면 그 좌표, 아니면 현재 지도 중심
  function originPoint() {
    if (S.origin) return S.origin;
    if (!S.map) return DEFAULT_CENTER;
    var c = S.map.getCenter();
    return { lat: c.getLat(), lng: c.getLng() };
  }

  // 식당 파생값 — 마지막 방문일 / 방문 횟수 / 평균 평점(방문·메뉴) / 목록에 띄울 메뉴
  function derive(r) {
    var vs = r.visits || [];
    var last = '', sum = 0, cnt = 0;
    vs.forEach(function (v) {
      if (v.date > last) last = v.date;
      if (v.rating) { sum += v.rating; cnt++; }
    });
    var ms = r.menus || [];
    var msum = 0, mcnt = 0;
    ms.forEach(function (m) { if (m.rating) { msum += m.rating; mcnt++; } });

    var avgRating = cnt ? sum / cnt : 0;
    var menuAvg = mcnt ? msum / mcnt : 0;
    return {
      lastDate: last,
      visitCount: vs.length,
      avgRating: avgRating,
      menuAvg: menuAvg,
      // 목록에 보여줄 별점 — 방문 평점이 없으면 메뉴 별점으로 대체
      showRating: avgRating || menuAvg,
      menuCount: ms.length,
      // 방문 기록이 있으면 최근에 먹은 것, 없으면 등록해 둔 메뉴
      listMenus: vs.length && (vs[0].menus || []).length
        ? vs[0].menus
        : ms.map(function (m) { return m.name; })
    };
  }

  // ---------- API ----------
  function api(method, path, body) {
    var opt = { method: method, headers: {} };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    if (S.pw) opt.headers['X-Admin-Password'] = S.pw;
    return fetch(path, opt).then(function (res) {
      return res.text().then(function (txt) {
        var data = null;
        try { data = txt ? JSON.parse(txt) : null; } catch (e) { /* 비JSON 응답 */ }
        if (!res.ok) {
          var msg = (data && data.error) || ('요청 실패 (' + res.status + ')');
          if (res.status === 401) S.pw = '';
          throw new Error(msg);
        }
        return data;
      });
    });
  }

  function requirePw() {
    if (S.pw) return Promise.resolve();
    return askPassword();
  }

  function askPassword() {
    return new Promise(function (resolve, reject) {
      openModal('관리자 인증', function (body, close) {
        body.appendChild(el('div', 'field',
          '<label>관리자 비밀번호</label><input type="password" id="pw-in" autocomplete="current-password">' +
          '<div class="hint">등록·수정·기록 작성에 필요합니다. 한 번 입력하면 이 브라우저에 저장됩니다.</div>'));
        var btns = el('div', 'modal-btns');
        var cancel = el('button', 'ghost', '취소');
        var ok = el('button', 'primary', '확인');
        btns.appendChild(cancel); btns.appendChild(ok);
        body.appendChild(btns);
        var input = body.querySelector('#pw-in');
        setTimeout(function () { input.focus(); }, 50);

        function submit() {
          var pw = input.value;
          if (!pw) return;
          ok.disabled = true;
          fetch('/api/auth', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: pw })
          }).then(function (res) {
            ok.disabled = false;
            if (!res.ok) { toast('비밀번호가 틀렸습니다.', true); input.select(); return; }
            S.pw = pw;
            localStorage.setItem(PW_KEY, pw);
            renderAdminBtn();
            close();
            resolve();
          }).catch(function (e) { ok.disabled = false; toast(e.message, true); });
        }
        ok.onclick = submit;
        input.onkeydown = function (e) { if (e.key === 'Enter') { e.preventDefault(); submit(); } };
        cancel.onclick = function () { close(); reject(new Error('취소')); };
      }, function () { reject(new Error('취소')); });
    });
  }

  function load() {
    return api('GET', '/api/restaurants').then(function (d) {
      S.all = (d.restaurants || []).map(function (r) {
        r._d = derive(r);
        return r;
      });
      rebuildMarkers();
      renderCatChips();
      apply();
    });
  }

  // ---------- 필터/정렬 ----------
  function matches(r) {
    var f = S.filters;
    var catKeys = Object.keys(f.cats);
    if (catKeys.length && !f.cats[r.category || '기타']) return false;
    if (f.novisit && r._d.visitCount > 0) return false;
    if (f.dist > 0) {
      if (r._dist == null || r._dist > f.dist) return false;
    }
    if (f.bounds && S.map && r.lat && r.lng) {
      if (!S.map.getBounds().contain(new kakao.maps.LatLng(r.lat, r.lng))) return false;
    }
    if (S.q) {
      var hay = [r.name, r.category, r.address, r.road_address, r.memo].join(' ');
      (r.menus || []).forEach(function (m) { hay += ' ' + m.name; });
      (r.visits || []).forEach(function (v) {
        hay += ' ' + (v.menus || []).join(' ') + ' ' + (v.memo || '') + ' ' + (v.company || '');
      });
      if (hay.toLowerCase().indexOf(S.q) === -1) return false;
    }
    return true;
  }

  function apply() {
    var o = originPoint();
    S.all.forEach(function (r) {
      r._dist = (r.lat && r.lng) ? distM(o, { lat: r.lat, lng: r.lng }) : null;
    });
    S.view = S.all.filter(matches);

    var s = S.filters.sort;
    S.view.sort(function (a, b) {
      if (s === 'dist') return (a._dist == null ? Infinity : a._dist) - (b._dist == null ? Infinity : b._dist);
      if (s === 'name') return a.name.localeCompare(b.name, 'ko');
      if (s === 'count') return b._d.visitCount - a._d.visitCount || a.name.localeCompare(b.name, 'ko');
      if (s === 'rating') return b._d.showRating - a._d.showRating || b._d.visitCount - a._d.visitCount;
      // recent: 마지막 방문일 내림차순, 미방문은 뒤로
      var av = a._d.lastDate || '', bv = b._d.lastDate || '';
      if (av !== bv) return bv.localeCompare(av);
      return a.name.localeCompare(b.name, 'ko');
    });

    renderList();
    renderCount();
    if (S.tab === 'rec') renderRec(); else syncMarkers();
  }

  // ---------- 렌더: 컨트롤 ----------
  function renderCatChips() {
    var box = $('#f-cat');
    box.innerHTML = '';
    var seen = {};
    var cats = CATEGORIES.slice();
    S.all.forEach(function (r) {
      var c = r.category || '기타';
      if (cats.indexOf(c) === -1) cats.push(c);
    });
    cats.forEach(function (c) {
      if (seen[c]) return;
      seen[c] = 1;
      var n = 0;
      S.all.forEach(function (r) { if ((r.category || '기타') === c) n++; });
      var chip = el('button', 'chip' + (S.filters.cats[c] ? ' on' : ''), esc(c) + (n ? ' <small>' + n + '</small>' : ''));
      chip.onclick = function () {
        if (S.filters.cats[c]) delete S.filters.cats[c]; else S.filters.cats[c] = true;
        renderCatChips(); apply();
      };
      box.appendChild(chip);
    });
  }

  function renderChipRow(sel, items, current, onPick) {
    var box = $(sel);
    box.innerHTML = '';
    items.forEach(function (it) {
      var chip = el('button', 'chip' + (it.key === current ? ' on' : ''), esc(it.label));
      chip.onclick = function () { onPick(it.key); };
      box.appendChild(chip);
    });
  }

  function renderFilterCtl() {
    renderChipRow('#f-dist', DIST_STEPS, S.filters.dist, function (k) {
      S.filters.dist = k; renderFilterCtl(); apply();
    });
    renderChipRow('#f-sort', SORTS, S.filters.sort, function (k) {
      S.filters.sort = k; renderFilterCtl(); apply();
    });
    $('#origin-label').textContent = originLabel();
    $('#f-bounds').checked = S.filters.bounds;
    $('#f-novisit').checked = S.filters.novisit;
  }

  function renderCount() {
    var n = S.view.length, total = S.all.length;
    $('#count').textContent = n === total ? '· ' + total + '곳' : '· ' + n + '/' + total + '곳';
  }

  function renderAdminBtn() {
    $('#admin-btn').textContent = S.pw ? '🔓' : '🔒';
    $('#admin-btn').title = S.pw ? '관리자 인증됨 (클릭하면 해제)' : '관리자 로그인';
  }

  // ---------- 렌더: 목록 ----------
  function renderList() {
    var ul = $('#list-mine');
    ul.innerHTML = '';
    if (S.tab !== 'mine') return;

    if (!S.view.length) {
      var msg = S.all.length
        ? '조건에 맞는 식당이 없습니다.<br>필터를 조정해 보세요.'
        : '아직 등록된 식당이 없습니다.<br><b>식당 찾기</b> 탭에서 카카오맵으로 검색해 등록하세요.';
      showEmpty(msg);
      return;
    }
    hideEmpty();

    S.view.forEach(function (r) {
      var d = r._d;
      var li = el('li', 'item' + (r.id === S.activeId ? ' on' : ''));
      var main = el('div', 'item-main');
      main.appendChild(el('div', 'item-name',
        esc(r.name) +
        '<span class="cat-tag">' + esc(r.category || '기타') + '</span>' +
        (d.visitCount === 0 ? '<span class="badge-new">미방문</span>' : '')));
      main.appendChild(el('div', 'item-sub', esc(r.road_address || r.address || '')));
      if (d.listMenus.length) {
        main.appendChild(el('div', 'item-menus', '🍽 ' + esc(d.listMenus.join(', '))));
      }
      li.appendChild(main);

      var right = el('div', 'item-right');
      if (r._dist != null) right.appendChild(el('div', null, fmtDist(r._dist)));
      if (d.visitCount) right.appendChild(el('div', null, d.visitCount + '회'));
      if (d.lastDate) right.appendChild(el('div', null, d.lastDate.slice(2).replace(/-/g, '.')));
      if (d.showRating) right.appendChild(el('div', 'stars', stars(Math.round(d.showRating))));
      li.appendChild(right);

      li.onclick = function () { focus(r); };
      ul.appendChild(li);
    });
  }

  function renderKakaoList() {
    var ul = $('#list-kakao');
    ul.innerHTML = '';
    if (!S.kakaoResults.length) {
      showEmpty('음식점 이름이나 지역을 검색하세요.<br>예: <b>역삼 국밥</b>, <b>판교 파스타</b>');
      return;
    }
    hideEmpty();
    var o = originPoint();
    S.kakaoResults.forEach(function (p) {
      var already = S.all.some(function (r) { return r.kakao_id === p.id; });
      var li = el('li', 'item');
      var main = el('div', 'item-main');
      main.appendChild(el('div', 'item-name',
        esc(p.place_name) +
        (p.category_group_name ? '<span class="cat-tag">' + esc(p.category_group_name) + '</span>' : '') +
        (already ? '<span class="badge-new">등록됨</span>' : '')));
      main.appendChild(el('div', 'item-sub', esc(p.road_address_name || p.address_name || '')));
      main.appendChild(el('div', 'item-sub', esc(p.category_name || '')));
      li.appendChild(main);

      var right = el('div', 'item-right');
      var dm = distM(o, { lat: +p.y, lng: +p.x });
      if (dm != null) right.appendChild(el('div', null, fmtDist(dm)));
      var addBtn = el('button', already ? 'ghost sm' : 'primary', already ? '보기' : '등록');
      addBtn.style.marginTop = '4px';
      addBtn.onclick = function (e) {
        e.stopPropagation();
        if (already) {
          var ex = S.all.filter(function (r) { return r.kakao_id === p.id; })[0];
          switchTab('mine');
          focus(ex);
        } else {
          openRestaurantForm(null, p);
        }
      };
      right.appendChild(addBtn);
      li.appendChild(right);

      li.onclick = function () { panTo(+p.y, +p.x, p.place_name, p.road_address_name || p.address_name); };
      ul.appendChild(li);
    });
  }

  function showEmpty(html) { var e = $('#empty'); e.innerHTML = html; e.className = 'empty'; }
  function hideEmpty() { $('#empty').className = 'empty hidden'; }

  // ---------- 오늘 뭐 먹지 ----------

  // 조건에 맞는 후보. 거리는 기준점(기본 = 회사) 기준.
  function recPool() {
    var o = originPoint();
    var cats = Object.keys(S.rec.cats);
    return S.all.filter(function (r) {
      if (cats.length && !S.rec.cats[r.category || '기타']) return false;
      if (S.rec.dist > 0) {
        var d = (r.lat && r.lng) ? distM(o, { lat: r.lat, lng: r.lng }) : null;
        if (d == null || d > S.rec.dist) return false;
      }
      if (S.rec.days > 0) {
        var ds = daysSince(r._d.lastDate);
        if (ds != null && ds < S.rec.days) return false;   // 최근에 갔으면 뺀다
      }
      return true;
    });
  }

  function renderRecCtl() {
    renderChipRow('#rec-dist', DIST_STEPS, S.rec.dist, function (k) {
      S.rec.dist = k; renderRecCtl(); renderRec();
    });
    renderChipRow('#rec-days', REC_DAYS, S.rec.days, function (k) {
      S.rec.days = k; renderRecCtl(); renderRec();
    });

    // 카테고리 칩 — 후보에 실제로 존재하는 것만 (누를 수 없는 칩을 안 보여준다)
    var box = $('#rec-cat');
    box.innerHTML = '';
    var counts = {};
    S.all.forEach(function (r) {
      var c = r.category || '기타';
      counts[c] = (counts[c] || 0) + 1;
    });
    Object.keys(counts).sort(function (a, b) { return counts[b] - counts[a]; }).forEach(function (c) {
      var chip = el('button', 'chip' + (S.rec.cats[c] ? ' on' : ''), esc(c) + ' <small>' + counts[c] + '</small>');
      chip.onclick = function () {
        if (S.rec.cats[c]) delete S.rec.cats[c]; else S.rec.cats[c] = true;
        renderRecCtl(); renderRec();
      };
      box.appendChild(chip);
    });
  }

  // 후보 목록 + (뽑힌 게 있으면) 결과 카드
  function renderRec() {
    if (S.tab !== 'rec') return;
    var pool = recPool();

    $('#rec-pool-count').textContent = '후보 ' + pool.length + '곳';
    var ul = $('#rec-pool');
    ul.innerHTML = '';
    pool.forEach(function (r) {
      var li = el('li', 'item' + (S.rec.picked === r.id ? ' on' : ''));
      var main = el('div', 'item-main');
      main.appendChild(el('div', 'item-name',
        esc(r.name) + '<span class="cat-tag">' + esc(r.category || '기타') + '</span>'));
      if (r._d.listMenus.length) {
        main.appendChild(el('div', 'item-menus', '🍽 ' + esc(r._d.listMenus.slice(0, 4).join(', '))));
      }
      li.appendChild(main);

      var right = el('div', 'item-right');
      if (r._dist != null) right.appendChild(el('div', null, fmtDist(r._dist)));
      var ds = daysSince(r._d.lastDate);
      right.appendChild(el('div', null, ds == null ? '미방문' : ds === 0 ? '오늘' : ds + '일 전'));
      if (r._d.showRating) right.appendChild(el('div', 'stars', stars(Math.round(r._d.showRating))));
      li.appendChild(right);

      li.onclick = function () { settleRec(r); };
      ul.appendChild(li);
    });

    if (!pool.length) {
      ul.appendChild(el('li', 'empty',
        '조건에 맞는 식당이 없습니다.<br>거리를 넓히거나 <b>최근 방문 제외</b>를 줄여 보세요.'));
    }

    // 뽑힌 곳이 후보에서 빠졌으면 결과도 지운다
    if (S.rec.picked && !pool.some(function (r) { return r.id === S.rec.picked; })) {
      S.rec.picked = null;
      $('#rec-result').innerHTML = '';
      $('#rec-slot-box').className = 'hidden';
    }
    syncMarkers(pool);
  }

  // 룰렛 — 이름이 빠르게 바뀌다가 점점 느려지고, 마지막에 뜬 이름이 결과다
  function spinRec() {
    if (S.rec.spinning) return;
    var pool = recPool();
    if (!pool.length) { toast('후보가 없습니다. 조건을 넓혀 보세요.', true); return; }

    var slotBox = $('#rec-slot-box'), slot = $('#rec-slot'), sub = $('#rec-slot-sub');
    slotBox.className = '';
    $('#rec-result').innerHTML = '';
    S.rec.picked = null;

    if (pool.length === 1) {
      slot.textContent = pool[0].name;
      sub.textContent = '후보가 한 곳뿐입니다';
      settleRec(pool[0]);
      return;
    }

    S.rec.spinning = true;
    var btn = $('#rec-spin');
    btn.disabled = true;
    sub.textContent = '후보 ' + pool.length + '곳';
    var n = 0, total = 25 + Math.floor(Math.random() * 3);

    (function step() {
      var r = pool[Math.floor(Math.random() * pool.length)];
      slot.textContent = r.name;
      slot.className = 'tick';
      setTimeout(function () { slot.className = ''; }, 60);
      n++;
      if (n >= total) {
        S.rec.spinning = false;
        btn.disabled = false;
        settleRec(r);          // 화면에 마지막으로 뜬 곳이 그대로 결과
        return;
      }
      // 점점 느려진다 — 전체 5초 안팎, 마지막 몇 칸은 0.5초씩 느긋하게 떨어진다
      setTimeout(step, 42 + n * n * 0.75);
    })();
  }

  // 결과 확정 — 카드 그리고 지도도 그 곳으로
  function settleRec(r) {
    S.rec.picked = r.id;
    var d = r._d;
    var box = $('#rec-result');
    box.innerHTML = '';

    var card = el('div', 'rec-card');
    card.appendChild(el('div', 'rec-card-name',
      esc(r.name) + ' <span class="cat-tag">' + esc(r.category || '기타') + '</span>'));

    var bits = [];
    if (r._dist != null) bits.push(fmtDist(r._dist));
    var ds = daysSince(d.lastDate);
    bits.push(ds == null ? '아직 안 가봤음' : ds === 0 ? '오늘 갔음' : ds + '일 전에 갔음');
    if (d.visitCount) bits.push('총 ' + d.visitCount + '회');
    if (d.showRating) bits.push(stars(Math.round(d.showRating)));
    card.appendChild(el('div', 'rec-card-meta', bits.join(' · ')));
    if (r.road_address) card.appendChild(el('div', 'rec-card-addr', esc(r.road_address)));
    if (r.memo) card.appendChild(el('div', 'rec-card-addr', esc(r.memo)));

    // 별점 매긴 메뉴가 있으면 높은 순으로 먼저 보여 준다 (뭘 시킬지도 정해 준다)
    var menus = (r.menus || []).slice().sort(function (a, b) { return (b.rating || 0) - (a.rating || 0); });
    if (menus.length) {
      var mm = el('div', 'rec-card-menus');
      menus.slice(0, 6).forEach(function (m) {
        mm.appendChild(el('span', 'menu-tag',
          esc(m.name) + (m.rating ? ' <span class="stars">' + stars(m.rating).slice(0, m.rating) + '</span>' : '')));
      });
      card.appendChild(mm);
    }

    var acts = el('div', 'rec-card-btns');
    var again = el('button', 'ghost sm', '🎲 다시');
    again.onclick = spinRec;
    var det = el('button', 'ghost sm', '상세');
    det.onclick = function () { openDetail(r); };
    var vis = el('button', 'primary sm', '＋ 방문 기록');
    vis.onclick = function () { openVisitForm(r, null); };
    var nav = el('button', 'ghost sm', '길찾기');
    nav.onclick = function () {
      window.open('https://map.kakao.com/link/to/' + encodeURIComponent(r.name) + ',' + r.lat + ',' + r.lng,
        '_blank', 'noopener');
    };
    [again, det, vis, nav].forEach(function (b) { acts.appendChild(b); });
    card.appendChild(acts);
    box.appendChild(card);

    $('#rec-slot').textContent = r.name;
    $('#rec-slot-sub').textContent = '결정!';
    $('#rec-slot-box').className = 'settled';

    panTo(r.lat, r.lng, r.name, r.road_address || r.address);
    renderRecPoolActive();
  }

  // 후보 목록에서 뽑힌 항목만 표시 갱신 (전체 리렌더 없이)
  function renderRecPoolActive() {
    var pool = recPool();
    var ul = $('#rec-pool');
    for (var i = 0; i < ul.children.length && i < pool.length; i++) {
      ul.children[i].className = 'item' + (pool[i].id === S.rec.picked ? ' on' : '');
    }
  }

  // ---------- 공무원 픽 (강남구 업무추진비) ----------
  // 강남구청 역삼1·2동이 법인카드로 실제 결제한 가게를 가게 단위로 집계한 것.
  // 개인별 구분은 없다 — 원자료의 '사용자' 칸도 직위(역삼1동장)만 적혀 있다.
  var GOV_BANDS = [
    { key: '', label: '전체' },
    { key: '0', label: '~1만' },
    { key: '1', label: '1~1.5만' },
    { key: '2', label: '1.5~2만' },
    { key: '3', label: '2만+' }
  ];
  var GOV_SORTS = [
    { key: 'count', label: '많이 간 순' },
    { key: 'dist', label: '가까운 순' },
    { key: 'cheap', label: '싼 순' },
    { key: 'recent', label: '최근' }
  ];

  function bandBucket(lo) {
    if (lo == null) return null;
    if (lo < 10000) return '0';
    if (lo < 15000) return '1';
    if (lo < 20000) return '2';
    return '3';
  }

  // 자치구별 데이터 파일. 구마다 기준점(origin)과 기간이 다르며 JSON 안에 들어 있다.
  var GOV_DISTRICTS = [
    { key: 'gangnam', label: '강남구 (역삼1·2동)', file: 'gn-spending.json' },
    { key: 'jongno', label: '종로구 (전 부서)', file: 'jn-spending.json' }
  ];

  function renderDistrictSel() {
    var sel = $('#gov-district');
    if (sel.options.length) return;
    GOV_DISTRICTS.forEach(function (d) {
      var o = document.createElement('option');
      o.value = d.key;
      o.textContent = d.label;
      sel.appendChild(o);
    });
    sel.value = S.gov.district;
    sel.onchange = function () {
      S.gov.district = sel.value;
      S.gov.picked = null;
      S.gov.q = '';
      $('#q-gov').value = '';
      closeDetail();
      loadGov(true);
    };
  }

  function loadGov(panMap) {
    renderDistrictSel();
    var key = S.gov.district;
    var meta = GOV_DISTRICTS.filter(function (d) { return d.key === key; })[0];

    var after = function (d) {
      if (S.gov.district !== key) return;   // 로딩 중 구를 바꿨으면 버린다
      S.gov.data = d;
      renderGovCtl();
      renderGov();
      if (panMap && d.origin) {
        S.map.setCenter(new kakao.maps.LatLng(d.origin.lat, d.origin.lng));
        S.map.setLevel(5);
      }
    };

    if (S.gov.cache[key]) { after(S.gov.cache[key]); return; }
    $('#gov-note').innerHTML = '<div class="empty">' + esc(meta.label) + ' 불러오는 중…</div>';
    $('#gov-list').innerHTML = '';
    fetch('data/' + meta.file).then(function (r) {
      if (!r.ok) throw new Error('데이터를 불러오지 못했습니다 (' + r.status + ')');
      return r.json();
    }).then(function (d) {
      S.gov.cache[key] = d;
      after(d);
    }).catch(function (e) {
      $('#gov-note').innerHTML = '<div class="empty">' + esc(e.message) + '</div>';
    });
  }

  function govPool() {
    var d = S.gov.data;
    if (!d) return [];
    // 거리는 그 구의 기준점(JSON 의 origin)에서 잰 값이다 — 강남구는 회사, 종로구는 종로구청
    var out = d.places.filter(function (p) {
      p._dist = p.dist_m;
      if (S.gov.dist > 0 && p._dist > S.gov.dist) return false;
      if (S.gov.band && bandBucket(p.per_person_band_lo) !== S.gov.band) return false;
      if (S.gov.q) {
        var hay = (p.name + ' ' + p.category + ' ' + p.road_address + ' ' + p.aliases.join(' ')).toLowerCase();
        if (hay.indexOf(S.gov.q) === -1) return false;
      }
      return true;
    });
    var s = S.gov.sort;
    out.sort(function (a, b) {
      if (s === 'dist') return a._dist - b._dist;
      if (s === 'recent') return b.last.localeCompare(a.last);
      if (s === 'cheap') {
        var av = a.per_person == null ? Infinity : a.per_person;
        var bv = b.per_person == null ? Infinity : b.per_person;
        return av - bv || b.count - a.count;
      }
      return b.count - a.count || a._dist - b._dist;
    });
    return out;
  }

  function renderGovCtl() {
    renderChipRow('#gov-dist', DIST_STEPS, S.gov.dist, function (k) {
      S.gov.dist = k; renderGovCtl(); renderGov();
    });
    renderChipRow('#gov-band', GOV_BANDS, S.gov.band, function (k) {
      S.gov.band = k; renderGovCtl(); renderGov();
    });
    renderChipRow('#gov-sort', GOV_SORTS, S.gov.sort, function (k) {
      S.gov.sort = k; renderGovCtl(); renderGov();
    });
  }

  function renderGov() {
    if (S.tab !== 'gov') return;
    var d = S.gov.data, pool = govPool();

    $('#gov-note').innerHTML =
      '<div class="gov-head"><b>' + pool.length + '곳</b> · ' + esc(d.source) + ' ' + esc(d.period) +
      '<br><span class="mute-s">거리 기준점: ' + esc(d.origin.label) +
      ' · 가게 단위 집계 · 개인 구분 없음 · ' +
      '<a href="' + esc(d.source_url) + '" target="_blank" rel="noopener">원자료</a></span></div>';

    var ul = $('#gov-list');
    ul.innerHTML = '';
    pool.forEach(function (p) {
      var mine = S.all.filter(function (r) { return r.kakao_id === p.kakao_id; })[0];
      var li = el('li', 'item' + (S.gov.picked === p.kakao_id ? ' on' : ''));
      var main = el('div', 'item-main');
      main.appendChild(el('div', 'item-name',
        esc(p.name) + '<span class="cat-tag">' + esc(p.category) + '</span>' +
        (mine ? '<span class="badge-mine">내 목록</span>' : '')));
      main.appendChild(el('div', 'item-sub', esc(p.road_address)));
      main.appendChild(el('div', 'item-menus',
        '💳 ' + p.count + '회 · ' + esc(p.per_person_band) +
        (p.per_person ? ' (인당 ' + p.per_person.toLocaleString('ko-KR') + '원)' : '')));
      li.appendChild(main);

      var right = el('div', 'item-right');
      right.appendChild(el('div', 'gov-count', p.count + '회'));
      right.appendChild(el('div', null, fmtDist(p._dist)));
      right.appendChild(el('div', null, p.last.slice(2, 7).replace('-', '.')));
      li.appendChild(right);

      li.onclick = function () { pickGov(p); };
      ul.appendChild(li);
    });

    if (!pool.length) {
      ul.appendChild(el('li', 'empty', '조건에 맞는 가게가 없습니다.'));
    }
    drawGovMarkers(pool);
  }

  function drawGovMarkers(pool) {
    clearKakaoMarkers();
    var img = new kakao.maps.MarkerImage(
      'data:image/svg+xml;base64,' + btoa(
        '<svg xmlns="http://www.w3.org/2000/svg" width="26" height="34" viewBox="0 0 30 40">' +
        '<path d="M15 39C15 39 28 24 28 14A13 13 0 1 0 2 14C2 24 15 39 15 39Z" fill="#5b8c5a" stroke="#14161a" stroke-width="2"/>' +
        '<circle cx="15" cy="14" r="5" fill="#14161a"/></svg>'),
      new kakao.maps.Size(26, 34), { offset: new kakao.maps.Point(13, 33) });
    pool.slice(0, 120).forEach(function (p) {
      var m = new kakao.maps.Marker({
        position: new kakao.maps.LatLng(p.lat, p.lng), image: img,
        map: S.map, title: p.name + ' (' + p.count + '회)', zIndex: 4
      });
      kakao.maps.event.addListener(m, 'click', function () { pickGov(p); });
      S.kMarkers.push(m);
    });
  }

  function pickGov(p) {
    S.gov.picked = p.kakao_id;
    panTo(p.lat, p.lng, p.name, p.count + '회 · ' + p.per_person_band);
    openGovDetail(p);
    renderGov();
  }

  function openGovDetail(p) {
    var box = $('#detail-body');
    box.innerHTML = '';
    box.appendChild(el('div', 'd-name', esc(p.name) + ' <span class="cat-tag">' + esc(p.category) + '</span>'));

    var meta = [];
    if (p.road_address) meta.push(esc(p.road_address));
    meta.push(esc(S.gov.data.origin.label) + '에서 ' + fmtDist(p.dist_m));
    box.appendChild(el('div', 'd-meta', meta.join('<br>')));

    var stat = el('div', 'd-stat');
    stat.appendChild(el('div', null, '집행<b>' + p.count + '회</b>'));
    stat.appendChild(el('div', null, '인당<b>' + (p.per_person ? p.per_person.toLocaleString('ko-KR') + '원' : '–') + '</b>'));
    stat.appendChild(el('div', null, '건당<b>' + p.avg_amount.toLocaleString('ko-KR') + '원</b>'));
    stat.appendChild(el('div', null, '최근<b>' + p.last.slice(2, 7).replace('-', '.') + '</b>'));
    box.appendChild(stat);

    var acts = el('div', 'd-actions');
    var mine = S.all.filter(function (r) { return r.kakao_id === p.kakao_id; })[0];
    if (mine) {
      var go = el('button', 'primary', '내 기록에서 보기');
      go.onclick = function () { switchTab('mine'); focus(mine); };
      acts.appendChild(go);
    } else {
      var add = el('button', 'primary', '＋ 내 목록에 추가');
      add.onclick = function () {
        openRestaurantForm(null, {
          place_name: p.name, id: p.kakao_id,
          address_name: '', road_address_name: p.road_address, phone: '',
          y: p.lat, x: p.lng,
          place_url: 'http://place.map.kakao.com/' + p.kakao_id,
          category_name: '음식점 > ' + p.category
        });
      };
      acts.appendChild(add);
    }
    var kb = el('button', 'ghost', '카카오맵');
    kb.onclick = function () { window.open('http://place.map.kakao.com/' + p.kakao_id, '_blank', 'noopener'); };
    acts.appendChild(kb);
    var nav = el('button', 'ghost', '길찾기');
    nav.onclick = function () {
      window.open('https://map.kakao.com/link/to/' + encodeURIComponent(p.name) + ',' + p.lat + ',' + p.lng, '_blank', 'noopener');
    };
    acts.appendChild(nav);
    box.appendChild(acts);

    box.appendChild(el('div', 'd-sec-h', '<span>인당금액대 분포</span><span class="hint-inline">' +
      esc(p.per_person_band) + ' 중앙값</span>'));
    if (p.band_dist && Object.keys(p.band_dist).length) {
      var wrap = el('div', 'menu-list');
      var max = Math.max.apply(null, Object.keys(p.band_dist).map(function (k) { return p.band_dist[k]; }));
      Object.keys(p.band_dist).forEach(function (k) {
        var n = p.band_dist[k];
        var row = el('div', 'menu-row');
        row.appendChild(el('div', 'menu-row-name', esc(k)));
        var bar = el('div', 'gov-bar');
        bar.innerHTML = '<span style="width:' + Math.round(n / max * 100) + '%"></span><i>' + n + '건</i>';
        row.appendChild(bar);
        wrap.appendChild(row);
      });
      box.appendChild(wrap);
    } else {
      box.appendChild(el('div', 'empty sm', '집행인원이 공개되지 않아 인당금액을 낼 수 없는 가게입니다.'));
    }

    box.appendChild(el('div', 'd-sec-h', '<span>집행 기록</span>'));
    box.appendChild(el('div', 'd-meta',
      esc(p.first) + ' ~ ' + esc(p.last) + ' 사이 <b>' + p.count + '회</b><br>' +
      '누적 ' + p.total_amount.toLocaleString('ko-KR') + '원 · 건당 평균 ' +
      p.avg_amount.toLocaleString('ko-KR') + '원' +
      (p.aliases.length > 1 ? '<br><span class="mute-s">가맹점명: ' + esc(p.aliases.join(' / ')) + '</span>' : '')));

    $('#detail').className = '';
  }

  // ---------- 지도 ----------
  function initMap() {
    S.map = new kakao.maps.Map($('#map'), {
      center: new kakao.maps.LatLng(DEFAULT_CENTER.lat, DEFAULT_CENTER.lng),
      level: 5
    });
    S.map.addControl(new kakao.maps.ZoomControl(), kakao.maps.ControlPosition.RIGHT);
    S.clusterer = new kakao.maps.MarkerClusterer({ map: S.map, averageCenter: true, minLevel: 7 });

    kakao.maps.event.addListener(S.map, 'click', closeOverlay);
    // 지도를 움직이면 거리(기준점=지도 중심)와 '보이는 곳만' 필터를 다시 계산
    kakao.maps.event.addListener(S.map, 'idle', function () {
      if (!S.origin || S.filters.bounds) apply();
      renderBadge();
    });
    renderBadge();
  }

  function renderBadge() {
    var b = $('#map-badge');
    var bits = [];
    bits.push('기준점: ' + originLabel());
    if (S.filters.dist) bits.push('반경 ' + DIST_STEPS.filter(function (d) { return d.key === S.filters.dist; })[0].label);
    b.textContent = bits.join(' · ');
    b.className = '';
  }

  function rebuildMarkers() {
    if (!S.map) return;
    S.clusterer.clear();
    S.markers = {};
    var list = [];
    S.all.forEach(function (r) {
      if (!r.lat || !r.lng) return;
      var m = new kakao.maps.Marker({ position: new kakao.maps.LatLng(r.lat, r.lng), title: r.name });
      kakao.maps.event.addListener(m, 'click', function () { switchTab('mine'); focus(r); });
      S.markers[r.id] = m;
      list.push(m);
    });
    S.clusterer.addMarkers(list);
  }

  // 넘긴 목록에 없는 마커는 지도에서 뺀다
  function syncMarkers(list) {
    if (!S.map) return;
    if (!list) {
      if (S.tab !== 'mine') return;
      list = S.view;
    }
    var visible = {};
    list.forEach(function (r) { visible[r.id] = true; });
    var add = [], remove = [];
    Object.keys(S.markers).forEach(function (id) {
      var m = S.markers[id];
      var want = !!visible[id];
      if (want && !m._shown) { add.push(m); m._shown = true; }
      else if (!want && m._shown) { remove.push(m); m._shown = false; }
    });
    if (remove.length) S.clusterer.removeMarkers(remove);
    if (add.length) S.clusterer.addMarkers(add);
  }

  function clearKakaoMarkers() {
    S.kMarkers.forEach(function (m) { m.setMap(null); });
    S.kMarkers = [];
  }

  function closeOverlay() {
    if (S.overlay) { S.overlay.setMap(null); S.overlay = null; }
  }

  function showOverlay(lat, lng, title, sub) {
    closeOverlay();
    var html = '<div class="ov"><b>' + esc(title) + '</b>' + (sub ? '<small>' + esc(sub) + '</small>' : '') + '</div>';
    S.overlay = new kakao.maps.CustomOverlay({
      position: new kakao.maps.LatLng(lat, lng),
      content: html, yAnchor: 1.35, clickable: false
    });
    S.overlay.setMap(S.map);
  }

  function panTo(lat, lng, title, sub) {
    if (!lat || !lng) return;
    S.map.panTo(new kakao.maps.LatLng(lat, lng));
    showOverlay(lat, lng, title, sub);
  }

  function focus(r) {
    S.activeId = r.id;
    renderList();
    panTo(r.lat, r.lng, r.name, r.road_address || r.address);
    openDetail(r);
  }

  // ---------- 카카오맵 검색 ----------
  function searchKakao(q) {
    if (!q.trim()) return;
    var ps = new kakao.maps.services.Places();
    var opt = { size: 15, category_group_code: 'FD6' }; // FD6 = 음식점
    if ($('#k-near').checked) {
      var c = S.map.getCenter();
      opt.location = c;
      opt.radius = 5000;
      opt.sort = kakao.maps.services.SortBy.DISTANCE;
    }
    ps.keywordSearch(q, function (data, status) {
      if (status === kakao.maps.services.Status.ZERO_RESULT) {
        S.kakaoResults = [];
        renderKakaoList();
        clearKakaoMarkers();
        toast('검색 결과가 없습니다.');
        return;
      }
      if (status !== kakao.maps.services.Status.OK) {
        toast('카카오맵 검색에 실패했습니다.', true);
        return;
      }
      S.kakaoResults = data;
      renderKakaoList();
      drawKakaoMarkers(data);
    }, opt);
  }

  function drawKakaoMarkers(list) {
    clearKakaoMarkers();
    var img = new kakao.maps.MarkerImage(
      'data:image/svg+xml;base64,' + btoa(
        '<svg xmlns="http://www.w3.org/2000/svg" width="30" height="40" viewBox="0 0 30 40">' +
        '<path d="M15 39C15 39 28 24 28 14A13 13 0 1 0 2 14C2 24 15 39 15 39Z" fill="#ffb454" stroke="#14161a" stroke-width="2"/>' +
        '<circle cx="15" cy="14" r="5" fill="#14161a"/></svg>'),
      new kakao.maps.Size(30, 40), { offset: new kakao.maps.Point(15, 39) });

    var bounds = new kakao.maps.LatLngBounds();
    list.forEach(function (p) {
      var pos = new kakao.maps.LatLng(+p.y, +p.x);
      var m = new kakao.maps.Marker({ position: pos, image: img, map: S.map, title: p.place_name, zIndex: 5 });
      kakao.maps.event.addListener(m, 'click', function () {
        showOverlay(+p.y, +p.x, p.place_name, p.road_address_name || p.address_name);
      });
      S.kMarkers.push(m);
      bounds.extend(pos);
    });
    if (list.length) S.map.setBounds(bounds);
  }

  // ---------- 모달 ----------
  function openModal(title, build, onClose) {
    var modal = $('#modal');
    $('#modal-title').textContent = title;
    var body = $('#modal-body');
    body.innerHTML = '';
    modal.className = 'modal';

    var closed = false;
    function close() {
      if (closed) return;
      closed = true;
      modal.className = 'modal hidden';
      body.innerHTML = '';
      modal._close = null;
      modal.onclick = null;
    }
    modal._close = function () { var wasClosed = closed; close(); if (!wasClosed && onClose) onClose(); };
    modal.onclick = function (e) { if (e.target === modal) modal._close(); };
    build(body, close);
  }

  $('.modal-x').onclick = function () { var m = $('#modal'); if (m._close) m._close(); };
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Escape') return;
    var m = $('#modal');
    if (!m.classList.contains('hidden') && m._close) { m._close(); return; }
    if (!$('#detail').classList.contains('hidden')) closeDetail();
  });

  // '카카오에서 가져오기' — 카카오에 등록된 메뉴를 메뉴 칸에 채운다.
  // 서버가 카카오 API 를 대신 호출한다(브라우저에서 직접 부르면 CORS 로 막힌다).
  function wireKakaoFetch(body, kakaoId) {
    var btn = body.querySelector('#m-fetch');
    var input = body.querySelector('#m-menus');
    var hint = body.querySelector('#m-menus-hint');
    if (!btn) return;

    if (!kakaoId) {
      btn.disabled = true;
      btn.title = '카카오맵 검색으로 등록한 식당만 가져올 수 있습니다';
      return;
    }
    btn.onclick = function () {
      var label = btn.textContent;
      btn.disabled = true;
      btn.textContent = '가져오는 중…';
      api('GET', '/api/kakao/place/' + encodeURIComponent(kakaoId)).then(function (d) {
        var menus = d.menus || [];
        if (!menus.length) {
          toast('카카오에 등록된 메뉴가 없습니다.');
          return;
        }
        // 이미 적어 둔 메뉴는 살리고 없는 것만 뒤에 붙인다
        var cur = input.value.split(',').map(function (x) { return x.trim(); }).filter(Boolean);
        var added = 0;
        menus.forEach(function (m) {
          if (cur.indexOf(m.name) === -1) { cur.push(m.name); added++; }
        });
        input.value = cur.join(', ');

        // 가격은 저장하지 않으므로 참고용으로만 보여 준다
        var priced = menus.filter(function (m) { return m.price > 0; })
          .map(function (m) { return m.name + ' ' + m.price.toLocaleString('ko-KR') + '원'; });
        hint.innerHTML = '카카오 메뉴 ' + menus.length + '개' +
          (d.score ? ' · 평점 ' + d.score + '★ (리뷰 ' + d.reviews + ')' : '') +
          (priced.length ? '<br><span style="color:#7d8595">' + esc(priced.join(' · ')) + '</span>' : '');
        toast(added ? '메뉴 ' + added + '개 추가했습니다.' : '이미 다 들어있는 메뉴입니다.');
      }).catch(function (e) {
        toast(e.message, true);
      }).then(function () {
        btn.disabled = false;
        btn.textContent = label;
      });
    };
  }

  // 식당 등록/수정 폼. place 가 있으면 카카오 검색결과로 프리필.
  function openRestaurantForm(existing, place) {
    requirePw().then(function () {
      var r = existing || {};
      var name = r.name || (place ? place.place_name : '');
      var addr = r.address || (place ? place.address_name : '');
      var road = r.road_address || (place ? place.road_address_name : '');
      var phone = r.phone || (place ? place.phone : '');
      var lat = r.lat || (place ? +place.y : 0);
      var lng = r.lng || (place ? +place.x : 0);
      var placeUrl = r.place_url || (place ? place.place_url : '');
      var kakaoId = r.kakao_id || (place ? place.id : '');
      var cat = r.category || (place ? guessCategory(place) : '');
      // 별점은 폼에서 건드리지 않는다 — 이름만 보내면 서버가 기존 별점을 살려 준다
      var menuNames = (r.menus || []).map(function (m) { return m.name; }).join(', ');

      openModal(existing ? '식당 수정' : '식당 등록', function (body, close) {
        var opts = CATEGORIES.map(function (c) {
          return '<option value="' + esc(c) + '"' + (c === cat ? ' selected' : '') + '>' + esc(c) + '</option>';
        }).join('');
        if (cat && CATEGORIES.indexOf(cat) === -1) {
          opts = '<option value="' + esc(cat) + '" selected>' + esc(cat) + '</option>' + opts;
        }
        body.appendChild(el('div', null,
          '<div class="field"><label>식당명 *</label><input type="text" id="m-name" value="' + esc(name) + '"></div>' +
          '<div class="field"><label>카테고리</label><select id="m-cat">' + opts + '</select></div>' +
          '<div class="field"><label>메뉴</label>' +
            '<div class="row"><input type="text" id="m-menus" value="' + esc(menuNames) + '" placeholder="짜장면, 탕수육, 군만두">' +
              '<button type="button" id="m-fetch" class="ghost sm nowrap">카카오에서 가져오기</button></div>' +
            '<div class="hint" id="m-menus-hint">쉼표로 구분해 여러 개. 등록하면 상세 화면에서 메뉴별 별점을 바로 누를 수 있습니다.</div></div>' +
          '<div class="field"><label>도로명 주소</label><input type="text" id="m-road" value="' + esc(road) + '"></div>' +
          '<div class="field"><label>지번 주소</label><input type="text" id="m-addr" value="' + esc(addr) + '"></div>' +
          '<div class="field field-2">' +
            '<div><label>전화</label><input type="text" id="m-phone" value="' + esc(phone) + '"></div>' +
            '<div><label>좌표 (위도, 경도)</label><input type="text" id="m-ll" value="' + (lat && lng ? lat + ', ' + lng : '') + '"></div>' +
          '</div>' +
          '<div class="field"><label>메모</label><textarea id="m-memo">' + esc(r.memo || '') + '</textarea></div>'));

        wireKakaoFetch(body, kakaoId);

        var btns = el('div', 'modal-btns');
        if (existing) {
          var del = el('button', 'danger', '삭제');
          del.style.marginRight = 'auto';
          del.onclick = function () {
            if (!confirm('“' + r.name + '” 과 방문 기록 ' + (r.visits || []).length + '건을 모두 삭제합니다.')) return;
            api('DELETE', '/api/restaurants/' + r.id).then(function () {
              close(); closeDetail(); toast('삭제했습니다.');
              return load();
            }).catch(function (e) { toast(e.message, true); });
          };
          btns.appendChild(del);
        }
        var cancel = el('button', 'ghost', '취소');
        cancel.onclick = close;
        var save = el('button', 'primary', existing ? '저장' : '등록');
        save.onclick = function () {
          var ll = (body.querySelector('#m-ll').value || '').split(',');
          var payload = {
            name: body.querySelector('#m-name').value.trim(),
            category: body.querySelector('#m-cat').value,
            road_address: body.querySelector('#m-road').value.trim(),
            address: body.querySelector('#m-addr').value.trim(),
            phone: body.querySelector('#m-phone').value.trim(),
            lat: parseFloat(ll[0]) || 0,
            lng: parseFloat(ll[1]) || 0,
            place_url: placeUrl,
            kakao_id: kakaoId,
            memo: body.querySelector('#m-memo').value.trim(),
            menus: body.querySelector('#m-menus').value.split(',')
              .map(function (x) { return x.trim(); }).filter(Boolean)
          };
          if (!payload.name) { toast('식당명은 필수입니다.', true); return; }
          save.disabled = true;
          var p = existing
            ? api('PUT', '/api/restaurants/' + r.id, payload)
            : api('POST', '/api/restaurants', payload);
          p.then(function (rec) {
            close();
            toast(existing ? '수정했습니다.'
              : (payload.menus.length ? '등록했습니다. 메뉴 별점을 바로 눌러 보세요.' : '등록했습니다.'));
            return load().then(function () {
              var fresh = S.all.filter(function (x) { return x.id === rec.id; })[0];
              if (!fresh) return;
              // 등록 직후엔 지도로 돌아가지 않고 그 식당 상세를 펼쳐 둔다
              switchTab('mine');
              focus(fresh);
            });
          }).catch(function (e) { save.disabled = false; toast(e.message, true); });
        };
        btns.appendChild(cancel); btns.appendChild(save);
        body.appendChild(btns);
      });
    }).catch(function () { /* 인증 취소 */ });
  }

  // 카카오 카테고리 문자열("음식점 > 한식 > 국밥")에서 우리 카테고리 추정
  function guessCategory(place) {
    var c = (place.category_name || '') + ' ' + (place.place_name || '');
    var rules = [
      ['치킨', '치킨'], ['카페·디저트', '카페|디저트|베이커리|빵|아이스크림|커피'],
      ['술집', '술집|호프|이자카야|바\\b|포차|와인'], ['고기', '육류|고기|삼겹|갈비|곱창|족발|구이'],
      ['일식', '일식|초밥|스시|돈까스|라멘|우동'], ['중식', '중식|중국'],
      ['양식', '양식|이탈리|파스타|피자|스테이크'], ['아시안', '아시아|베트남|태국|인도|쌀국수'],
      ['패스트푸드', '패스트푸드|버거|햄버거'], ['분식', '분식|떡볶이|김밥'],
      ['한식', '한식|국밥|칼국수|백반|찌개|해장']
    ];
    for (var i = 0; i < rules.length; i++) {
      if (new RegExp(rules[i][1]).test(c)) return rules[i][0];
    }
    return '기타';
  }

  // 방문 기록 등록/수정
  function openVisitForm(r, existing) {
    requirePw().then(function () {
      var v = existing || {};
      openModal(existing ? '방문 기록 수정' : r.name + ' — 뭐 먹었지?', function (body, close) {
        var rating = v.rating || 0;
        body.appendChild(el('div', null,
          '<div class="field field-2">' +
            '<div><label>날짜</label><input type="date" id="v-date" value="' + esc(v.date || today()) + '"></div>' +
            '<div><label>1인 가격 (원)</label><input type="number" id="v-price" min="0" step="500" value="' + (v.price || '') + '"></div>' +
          '</div>' +
          '<div class="field"><label>먹은 메뉴 *</label><input type="text" id="v-menus" value="' + esc((v.menus || []).join(', ')) + '" placeholder="김치찌개, 계란말이">' +
            '<div class="hint">쉼표로 여러 개 입력</div></div>' +
          '<div class="field"><label>평점</label><div class="rating-pick" id="v-rating"></div></div>' +
          '<div class="field"><label>같이 간 사람 / 모임</label><input type="text" id="v-company" value="' + esc(v.company || '') + '" placeholder="팀 점심"></div>' +
          '<div class="field"><label>메모</label><textarea id="v-memo" placeholder="맛·웨이팅·주차 등">' + esc(v.memo || '') + '</textarea></div>'));

        var pick = body.querySelector('#v-rating');
        function drawStars() {
          pick.innerHTML = '';
          for (var i = 1; i <= 5; i++) {
            (function (n) {
              var b = el('button', n <= rating ? 'on' : '', '★');
              b.type = 'button';
              b.onclick = function () { rating = (rating === n ? 0 : n); drawStars(); };
              pick.appendChild(b);
            })(i);
          }
          var clr = el('button', '', '↺');
          clr.type = 'button';
          clr.style.fontSize = '15px';
          clr.title = '평점 지우기';
          clr.onclick = function () { rating = 0; drawStars(); };
          pick.appendChild(clr);
        }
        drawStars();

        var btns = el('div', 'modal-btns');
        var cancel = el('button', 'ghost', '취소');
        cancel.onclick = close;
        var save = el('button', 'primary', '저장');
        save.onclick = function () {
          var menus = body.querySelector('#v-menus').value.split(',')
            .map(function (s) { return s.trim(); }).filter(Boolean);
          if (!menus.length) { toast('먹은 메뉴를 한 개 이상 입력하세요.', true); return; }
          var payload = {
            date: body.querySelector('#v-date').value || today(),
            menus: menus,
            rating: rating,
            price: parseInt(body.querySelector('#v-price').value, 10) || 0,
            company: body.querySelector('#v-company').value.trim(),
            memo: body.querySelector('#v-memo').value.trim()
          };
          save.disabled = true;
          var base = '/api/restaurants/' + r.id + '/visits';
          var p = existing ? api('PUT', base + '/' + v.id, payload) : api('POST', base, payload);
          p.then(function () {
            close();
            toast(existing ? '기록을 수정했습니다.' : '기록했습니다. 🍚');
            return load().then(function () {
              var fresh = S.all.filter(function (x) { return x.id === r.id; })[0];
              if (fresh) openDetail(fresh);
            });
          }).catch(function (e) { save.disabled = false; toast(e.message, true); });
        };
        btns.appendChild(cancel); btns.appendChild(save);
        body.appendChild(btns);
      });
    }).catch(function () { /* 인증 취소 */ });
  }

  // ---------- 상세 ----------

  // 메뉴 한 줄의 별 5개. 누른 별과 같은 점수를 다시 누르면 0점(미평가)으로 지운다.
  function menuStars(r, m) {
    var wrap = el('div', 'm-stars');
    function paint(v) {
      for (var i = 0; i < 5; i++) {
        wrap.children[i].className = 'm-star' + (i < v ? ' on' : '');
      }
      wrap.title = v ? v + '점' : '미평가';
    }
    for (var i = 1; i <= 5; i++) {
      (function (n) {
        var b = el('button', 'm-star', '★');
        b.type = 'button';
        b.setAttribute('aria-label', n + '점');
        b.onclick = function (e) {
          e.stopPropagation();
          rateMenu(r, m, (m.rating || 0) === n ? 0 : n, paint);
        };
        wrap.appendChild(b);
      })(i);
    }
    paint(m.rating || 0);
    return wrap;
  }

  // 별점 저장 — 누르는 즉시 칠하고(낙관적), 서버가 거부하면 되돌린다.
  function rateMenu(r, m, rating, paint) {
    var prev = m.rating || 0;
    m.rating = rating;
    paint(rating);
    requirePw().then(function () {
      return api('PUT', '/api/restaurants/' + r.id + '/menus/' + m.id, { rating: rating });
    }).then(function () {
      r._d = derive(r);
      renderList();                       // 목록의 별점·메뉴 표시도 같이 갱신
      var avg = $('#menu-avg');
      if (avg) avg.textContent = r._d.menuAvg ? '★' + r._d.menuAvg.toFixed(1) : '';
    }).catch(function (e) {
      m.rating = prev;
      paint(prev);
      if (e.message !== '취소') toast(e.message, true);
    });
  }

  function openDetail(r) {
    var d = derive(r);
    var box = $('#detail-body');
    box.innerHTML = '';

    box.appendChild(el('div', 'd-name', esc(r.name) + ' <span class="cat-tag">' + esc(r.category || '기타') + '</span>'));

    var meta = [];
    if (r.road_address) meta.push(esc(r.road_address));
    else if (r.address) meta.push(esc(r.address));
    if (r.phone) meta.push('<a href="tel:' + esc(r.phone) + '">' + esc(r.phone) + '</a>');
    if (r._dist != null) meta.push('기준점에서 ' + fmtDist(r._dist));
    if (r.memo) meta.push('<span style="color:#a8b0bd">' + esc(r.memo) + '</span>');
    box.appendChild(el('div', 'd-meta', meta.join('<br>')));

    var acts = el('div', 'd-actions');
    var addV = el('button', 'primary', '＋ 방문 기록');
    addV.onclick = function () { openVisitForm(r, null); };
    acts.appendChild(addV);
    var edit = el('button', 'ghost', '식당 수정');
    edit.onclick = function () { openRestaurantForm(r, null); };
    acts.appendChild(edit);
    if (r.place_url) {
      var kb = el('button', 'ghost', '카카오맵');
      kb.onclick = function () { window.open(r.place_url, '_blank', 'noopener'); };
      acts.appendChild(kb);
    }
    var nav = el('button', 'ghost', '길찾기');
    nav.onclick = function () {
      window.open('https://map.kakao.com/link/to/' + encodeURIComponent(r.name) + ',' + r.lat + ',' + r.lng, '_blank', 'noopener');
    };
    acts.appendChild(nav);
    box.appendChild(acts);

    // 통계는 방문 기록이 있을 때만 — 갓 등록한 식당은 메뉴 별점이 바로 보이게 한다
    if (d.visitCount) {
      var avgPrice = 0, pc = 0;
      r.visits.forEach(function (v) { if (v.price) { avgPrice += v.price; pc++; } });
      var stat = el('div', 'd-stat');
      stat.appendChild(el('div', null, '방문<b>' + d.visitCount + '회</b>'));
      stat.appendChild(el('div', null, '평점<b>' + (d.avgRating ? d.avgRating.toFixed(1) : '–') + '</b>'));
      stat.appendChild(el('div', null, '평균 1인<b>' + (pc ? Math.round(avgPrice / pc).toLocaleString('ko-KR') + '원' : '–') + '</b>'));
      stat.appendChild(el('div', null, '최근<b>' + (d.lastDate ? d.lastDate.slice(5).replace('-', '/') : '–') + '</b>'));
      box.appendChild(stat);
    }

    // ── 메뉴 — 별을 누르면 저장 버튼 없이 바로 반영된다 ──
    var menus = r.menus || [];
    var mh = el('div', 'd-sec-h',
      '<span>메뉴 ' + menus.length + '개 <span id="menu-avg" class="stars">' +
      (d.menuAvg ? '★' + d.menuAvg.toFixed(1) : '') + '</span></span>' +
      (menus.length ? '<span class="hint-inline">별을 누르면 바로 저장</span>' : ''));
    box.appendChild(mh);

    if (!menus.length) {
      box.appendChild(el('div', 'empty sm',
        '등록된 메뉴가 없습니다.<br><b>식당 수정</b>에서 쉼표로 구분해 넣으면 여기서 별점을 매길 수 있습니다.'));
    } else {
      var mlist = el('div', 'menu-list');
      menus.forEach(function (m) {
        var row = el('div', 'menu-row');
        row.appendChild(el('div', 'menu-row-name', esc(m.name)));
        row.appendChild(menuStars(r, m));
        mlist.appendChild(row);
      });
      box.appendChild(mlist);
    }

    var h = el('div', 'd-sec-h', '<span>방문 기록 ' + d.visitCount + '건</span>');
    box.appendChild(h);

    if (!d.visitCount) {
      box.appendChild(el('div', 'empty', '아직 기록이 없습니다.<br>다녀왔으면 <b>＋ 방문 기록</b>을 눌러 남겨 두세요.'));
    }
    (r.visits || []).forEach(function (v) {
      var card = el('div', 'visit');
      var top = el('div', 'visit-top');
      var left = el('div', 'visit-date', esc(v.date) +
        (v.rating ? ' <span class="stars">' + stars(v.rating) + '</span>' : '') +
        (v.price ? ' <span style="color:#7d8595;font-weight:400;font-size:12px">· ' + fmtWon(v.price) + '</span>' : ''));
      top.appendChild(left);
      var vb = el('div', 'visit-btns');
      var ev = el('button', 'mini-x', '수정');
      ev.onclick = function () { openVisitForm(r, v); };
      var dv = el('button', 'mini-x', '삭제');
      dv.onclick = function () {
        if (!confirm(v.date + ' 기록을 삭제할까요?')) return;
        requirePw().then(function () {
          return api('DELETE', '/api/restaurants/' + r.id + '/visits/' + v.id);
        }).then(function () {
          toast('삭제했습니다.');
          return load().then(function () {
            var fresh = S.all.filter(function (x) { return x.id === r.id; })[0];
            if (fresh) openDetail(fresh);
          });
        }).catch(function (e) { if (e.message !== '취소') toast(e.message, true); });
      };
      vb.appendChild(ev); vb.appendChild(dv);
      top.appendChild(vb);
      card.appendChild(top);

      if ((v.menus || []).length) {
        var mm = el('div', 'visit-menus');
        v.menus.forEach(function (m) { mm.appendChild(el('span', 'menu-tag', esc(m))); });
        card.appendChild(mm);
      }
      var note = [];
      if (v.company) note.push('👥 ' + esc(v.company));
      if (v.memo) note.push(esc(v.memo));
      if (note.length) card.appendChild(el('div', 'visit-note', note.join('\n')));
      box.appendChild(card);
    });

    $('#detail').className = '';
  }

  function closeDetail() {
    $('#detail').className = 'hidden';
    S.activeId = null;
    renderList();
    closeOverlay();
  }

  // ---------- 탭 ----------
  function switchTab(tab) {
    S.tab = tab;
    document.querySelectorAll('.tab').forEach(function (b) {
      b.className = 'tab' + (b.dataset.tab === tab ? ' on' : '');
    });
    $('#pane-mine-ctl').className = 'pane-ctl' + (tab === 'mine' ? '' : ' hidden');
    $('#pane-rec-ctl').className = 'pane-ctl' + (tab === 'rec' ? '' : ' hidden');
    $('#pane-gov-ctl').className = 'pane-ctl' + (tab === 'gov' ? '' : ' hidden');
    $('#pane-kakao-ctl').className = 'pane-ctl' + (tab === 'kakao' ? '' : ' hidden');
    $('#list-mine').className = 'list' + (tab === 'mine' ? '' : ' hidden');
    $('#list-kakao').className = 'list' + (tab === 'kakao' ? '' : ' hidden');
    $('#rec-wrap').className = tab === 'rec' ? '' : 'hidden';
    $('#gov-wrap').className = tab === 'gov' ? '' : 'hidden';

    if (tab === 'kakao') {
      // 등록 마커를 걷고 검색결과 마커만 남긴다
      S.clusterer.clear();
      Object.keys(S.markers).forEach(function (id) { S.markers[id]._shown = false; });
      hideEmpty();
      renderKakaoList();
      setTimeout(function () { $('#q-kakao').focus(); }, 60);
      return;
    }

    clearKakaoMarkers();
    hideEmpty();
    if (tab === 'rec') {
      renderRecCtl();
      renderRec();
    } else if (tab === 'gov') {
      // 등록 마커는 걷고 공무원 픽 마커만 띄운다
      S.clusterer.clear();
      Object.keys(S.markers).forEach(function (id) { S.markers[id]._shown = false; });
      loadGov();
    } else {
      renderList();
      syncMarkers();
    }
  }

  // ---------- 이벤트 배선 ----------
  function wire() {
    document.querySelectorAll('.tab').forEach(function (b) {
      b.onclick = function () { switchTab(b.dataset.tab); };
    });

    var qm = $('#q-mine');
    qm.oninput = function () {
      clearTimeout(qm._t);
      qm._t = setTimeout(function () { S.q = qm.value.trim().toLowerCase(); apply(); }, 150);
    };

    $('#kakao-form').onsubmit = function (e) { e.preventDefault(); searchKakao($('#q-kakao').value); };

    $('#rec-spin').onclick = spinRec;

    var qg = $('#q-gov');
    qg.oninput = function () {
      clearTimeout(qg._t);
      qg._t = setTimeout(function () { S.gov.q = qg.value.trim().toLowerCase(); renderGov(); }, 150);
    };

    $('#filter-btn').onclick = function () {
      var p = $('#filter-panel');
      var open = p.classList.contains('hidden');
      p.className = open ? '' : 'hidden';
      $('#filter-btn').className = 'ghost' + (open ? ' on' : '');
    };

    $('#f-bounds').onchange = function () { S.filters.bounds = this.checked; apply(); };
    $('#f-novisit').onchange = function () { S.filters.novisit = this.checked; apply(); };
    $('#f-reset').onclick = function () {
      S.filters = { cats: {}, dist: 0, sort: 'recent', bounds: false, novisit: false };
      S.q = ''; $('#q-mine').value = '';
      S.origin = { lat: OFFICE.lat, lng: OFFICE.lng }; S.originKind = 'office';
      renderCatChips(); renderFilterCtl(); renderBadge(); apply();
    };

    $('#origin-geo').onclick = function () {
      if (!navigator.geolocation) { toast('이 브라우저는 위치를 지원하지 않습니다.', true); return; }
      toast('위치 확인 중…');
      navigator.geolocation.getCurrentPosition(function (pos) {
        S.origin = { lat: pos.coords.latitude, lng: pos.coords.longitude };
        S.originKind = 'geo';
        S.map.setCenter(new kakao.maps.LatLng(S.origin.lat, S.origin.lng));
        renderFilterCtl(); renderBadge(); apply();
        toast('내 위치를 기준점으로 설정했습니다.');
      }, function () {
        toast('위치를 가져올 수 없습니다. (HTTPS·권한 확인)', true);
      }, { enableHighAccuracy: true, timeout: 8000 });
    };
    $('#origin-center').onclick = function () {
      S.origin = null; S.originKind = 'center';
      renderFilterCtl(); renderBadge(); apply();
      toast('지도 중심을 기준점으로 사용합니다.');
    };
    $('#origin-office').onclick = function () {
      S.origin = { lat: OFFICE.lat, lng: OFFICE.lng };
      S.originKind = 'office';
      S.map.setCenter(new kakao.maps.LatLng(OFFICE.lat, OFFICE.lng));
      renderFilterCtl(); renderBadge(); apply();
      toast(OFFICE.label + '를 기준점으로 설정했습니다.');
    };

    $('#detail-close').onclick = closeDetail;

    $('#admin-btn').onclick = function () {
      if (S.pw) {
        S.pw = '';
        localStorage.removeItem(PW_KEY);
        renderAdminBtn();
        toast('관리자 인증을 해제했습니다.');
      } else {
        askPassword().catch(function () {});
      }
    };
  }

  // ---------- 시작 ----------
  kakao.maps.load(function () {
    initMap();
    wire();
    renderFilterCtl();
    renderAdminBtn();
    load().catch(function (e) { toast('목록을 불러오지 못했습니다: ' + e.message, true); });
  });
})();
