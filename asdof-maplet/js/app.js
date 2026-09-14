/* asdof-maplet: redtable 크롤링 데이터 카카오맵 뷰어 */
(function () {
  'use strict';

  var state = {
    all: [],          // 전체 레코드 (파생필드 sido/gungu 포함)
    view: [],         // 검색/정렬 적용된 목록
    sortKey: 'name',
    sortAsc: true,
    activeId: null,
    map: null,
    clusterer: null,
    markers: {},      // store_id -> kakao Marker
    overlay: null,    // 열려있는 커스텀 오버레이
    boundsSet: null,  // '표시된 곳만 검색' 스냅샷 ({store_id: true} | null)
    filters: {
      instant: false,
      sidos: {},      // {시도명: true}
      cats: {},       // {카테고리: true}
      dates: [],      // ['YYYY-MM-DD', ...] 다중
      hours: {},      // {시(정수): true} 다중
      priceLo: 0,     // 만원 단위 (0 = 0원)
      priceHi: 11,    // 11 = 제한없음
    },
  };

  var CATEGORIES = ['식사', '고기', '일식', '중식', '양식', '고급식', '디저트'];
  var DAY_NAMES = ['일', '월', '화', '수', '목', '금', '토'];
  var HOUR_RANGE = [10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23];

  function toMin(t) {
    if (!t) return null;
    var p = String(t).split(':');
    return (+p[0]) * 60 + (+p[1] || 0);
  }

  // 특정 요일 영업정보(h)에서 hourNum시(정수) 예약 가능한가
  function hourOpen(h, hourNum) {
    if (!h) return false;
    var openM = toMin(h.open), rcM = toMin(h.reserve_close || h.close);
    if (openM == null || rcM == null) return false;
    var t = hourNum * 60;
    if (t < openM || t > rcM) return false;
    var bs = toMin(h.break_start), be = toMin(h.break_end);
    if (bs != null && be != null && t >= bs && t < be) return false;
    return true;
  }

  // 해당 날짜(dstr)에 예약 가능한 매장인가 (기간·휴무·요일 확인)
  function dateReservable(r, dstr) {
    if (r.reserve_from && dstr < r.reserve_from) return false;
    if (r.reserve_to && dstr > r.reserve_to) return false;
    if ((r.holidays || []).indexOf(dstr) !== -1) return false;
    var wd = DAY_NAMES[new Date(dstr + 'T00:00:00').getDay()];
    return (r.available_days || []).indexOf(wd) !== -1;
  }

  // 축약 표기 정규화 (서울 -> 서울특별시, 전남 -> 전라남도 ...)
  var SIDO_ALIAS = {
    '서울': '서울특별시', '부산': '부산광역시', '대구': '대구광역시', '인천': '인천광역시',
    '광주': '광주광역시', '대전': '대전광역시', '울산': '울산광역시', '세종': '세종특별자치시',
    '경기': '경기도', '강원': '강원특별자치도', '강원도': '강원특별자치도',
    '충북': '충청북도', '충남': '충청남도', '전북': '전북특별자치도', '전라북도': '전북특별자치도',
    '전남': '전라남도', '경북': '경상북도', '경남': '경상남도', '제주': '제주특별자치도',
  };

  // 주소에서 시도/군구 추출 ("서울특별시 중구 명동길 55" -> 서울특별시 / 중구)
  function splitRegion(addr) {
    var parts = (addr || '').trim().split(/\s+/);
    var sido = parts[0] || '';
    return { sido: SIDO_ALIAS[sido] || sido, gungu: parts[1] || '' };
  }

  function fmtPrice(n) {
    return n == null ? '' : n.toLocaleString('ko-KR') + '원';
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (ch) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
    });
  }

  // ---------- 필터 ----------
  function matchesFilters(r) {
    var f = state.filters;
    if (f.instant && r.instant_reservation !== true) return false;
    var sidoKeys = Object.keys(f.sidos);
    if (sidoKeys.length && !f.sidos[r.sido]) return false;
    var catKeys = Object.keys(f.cats);
    if (catKeys.length) {
      var ok = (r.categories || []).some(function (c) { return f.cats[c]; });
      if (!ok) return false;
    }
    if (f.priceLo > 0 || f.priceHi < 11) {
      if (r.price_tier == null) return false;
      if (r.price_tier < f.priceLo) return false;
      if (f.priceHi < 11 && r.price_tier >= f.priceHi) return false;
    }
    // 예약 일자/시간: 선택된 (날짜×시간) 슬롯 중 하나라도 가능하면 통과
    var dates = f.dates || [];
    var hours = Object.keys(f.hours).map(Number);
    if (dates.length || hours.length) {
      if (!r.business_hours) return false; // 예약정보 없는 매장 제외
      var slot = false;
      if (dates.length) {
        for (var i = 0; i < dates.length && !slot; i++) {
          if (!dateReservable(r, dates[i])) continue;
          if (!hours.length) { slot = true; break; }
          var wd = DAY_NAMES[new Date(dates[i] + 'T00:00:00').getDay()];
          var h = r.business_hours[wd];
          if (hours.some(function (x) { return hourOpen(h, x); })) slot = true;
        }
      } else {
        // 시간만 선택: 예약 가능한 요일 중 해당 시간이 열린 곳
        (r.available_days || []).forEach(function (wd) {
          if (hours.some(function (x) { return hourOpen(r.business_hours[wd], x); })) slot = true;
        });
      }
      if (!slot) return false;
    }
    return true;
  }

  function activeFilterCount() {
    var f = state.filters, n = 0;
    if (f.instant) n++;
    n += Object.keys(f.sidos).length;
    n += Object.keys(f.cats).length;
    if ((f.dates || []).length) n++;
    if (Object.keys(f.hours).length) n++;
    if (f.priceLo > 0 || f.priceHi < 11) n++;
    return n;
  }

  // ---------- 목록 ----------
  function applyFilterSort() {
    var q = document.getElementById('search').value.trim().toLowerCase();
    var list = state.all.filter(function (r) {
      r._matchedMenu = null;
      if (!matchesFilters(r)) return false;
      if (!q) return true;
      if ((r.name + ' ' + r.sido + ' ' + r.gungu + ' ' + (r.address || ''))
        .toLowerCase().indexOf(q) !== -1) return true;
      // 메뉴명 검색
      var hit = (r.menus || []).find(function (m) {
        return m.menu.toLowerCase().indexOf(q) !== -1;
      });
      if (hit) { r._matchedMenu = hit.menu; return true; }
      return false;
    });
    var k = state.sortKey, asc = state.sortAsc ? 1 : -1;
    list.sort(function (a, b) {
      var av = a[k] || '', bv = b[k] || '';
      if (av === bv) return (a.name || '').localeCompare(b.name || '', 'ko');
      return av.localeCompare(bv, 'ko') * asc;
    });
    state.baseView = list; // 마커는 필터/검색 결과 전체 유지
    if (state.boundsSet) {
      list = list.filter(function (r) { return state.boundsSet[r.store_id]; });
    }
    state.view = list;
    renderList();
    refreshMarkers();
    var btn = document.getElementById('filter-btn');
    var n = activeFilterCount();
    btn.textContent = n ? '필터 ' + n : '필터';
    btn.classList.toggle('on', n > 0);
  }

  // 필터/검색 결과만 지도에 표시 (표시된곳만 보기와 무관하게 전체 결과 유지)
  function refreshMarkers() {
    if (!state.clusterer) return;
    state.clusterer.clear();
    var markers = [];
    (state.baseView || []).forEach(function (r) {
      var mk = state.markers[r.store_id];
      if (mk) markers.push(mk);
    });
    state.clusterer.addMarkers(markers);
    // 선택된 매장이 필터로 사라졌으면 오버레이도 닫기
    if (state.activeId && !(state.baseView || []).some(function (r) { return r.store_id === state.activeId; })) {
      closeOverlay();
    }
  }

  function renderList() {
    var tbody = document.querySelector('#list tbody');
    var html = state.view.map(function (r) {
      var badge = r.instant_reservation ? '<span class="badge-instant">바로예약</span>' : '';
      var menuHit = r._matchedMenu
        ? '<div class="menu-hit">' + esc(r._matchedMenu) + '</div>' : '';
      return '<tr data-id="' + r.store_id + '"' +
        (r.store_id === state.activeId ? ' class="active"' : '') + '>' +
        '<td class="col-name">' + esc(r.name) + badge + menuHit + '</td>' +
        '<td class="col-sido">' + esc(r.sido) + '</td>' +
        '<td class="col-gungu">' + esc(r.gungu) + '</td></tr>';
    }).join('');
    tbody.innerHTML = html;
    document.getElementById('count').textContent =
      state.view.length + '/' + state.all.length + '곳';
  }

  function updateSortArrows() {
    document.querySelectorAll('#list thead th').forEach(function (th) {
      var span = th.querySelector('.arrow');
      span.textContent = th.dataset.key === state.sortKey ? (state.sortAsc ? '▲' : '▼') : '';
    });
  }

  // ---------- 상세 패널 ----------
  function menusTableHtml(r, withTags) {
    var rows = (r.menus || []).map(function (m) {
      return '<tr><td>' + esc(m.menu) +
        (m.represent ? '<span class="rep">대표</span>' : '') +
        (withTags && m.tags ? '<span class="tags">' + esc(m.tags) + '</span>' : '') +
        '</td><td class="price">' + fmtPrice(m.price_krw) + '</td></tr>';
    }).join('');
    return '<table class="menu-table"><tbody>' +
      (rows || '<tr><td>메뉴 정보 없음</td></tr>') + '</tbody></table>';
  }

  // 예약 가능 요일/시간 블록 (크롤링 데이터)
  function reserveBlockHtml(r) {
    if (!r.business_hours) return '';
    var ORDER = ['월', '화', '수', '목', '금', '토', '일'];
    var avail = {};
    (r.available_days || []).forEach(function (d) { avail[d] = true; });
    var rows = ORDER.map(function (d) {
      var h = r.business_hours[d];
      if (!avail[d] || !h) {
        return '<tr class="off"><td>' + d + '</td><td>휴무</td></tr>';
      }
      var t = h.open + '~' + h.close;
      if (h.reserve_close) t += ' <span class="rc">(예약 ' + h.reserve_close + '까지)</span>';
      if (h.break_start && h.break_end) {
        t += '<span class="brk">브레이크 ' + h.break_start + '~' + h.break_end + '</span>';
      }
      return '<tr><td>' + d + '</td><td>' + t + '</td></tr>';
    }).join('');
    var window = (r.reserve_from && r.reserve_to)
      ? '<div class="resv-window">예약 가능 기간 ' + esc(r.reserve_from) + ' ~ ' + esc(r.reserve_to) + '</div>' : '';
    var hol = (r.holidays && r.holidays.length)
      ? '<div class="resv-hol">휴무일 ' + r.holidays.map(esc).join(', ') + '</div>' : '';
    return '<h3>예약 가능 요일 · 시간</h3>' +
      '<table class="reserve-table"><tbody>' + rows + '</tbody></table>' +
      window + hol;
  }

  function showDetail(r) {
    state.selectedStore = r;
    var body = document.getElementById('detail-body');
    var resv = r.instant_reservation === true ? '<span class="badge-instant">바로예약 가능</span>'
      : r.instant_reservation === false ? '예약 확정 별도 필요' : '정보 없음';
    body.innerHTML =
      '<h2>' + esc(r.name) + '</h2>' +
      (r.name_en ? '<div class="en-name">' + esc(r.name_en) + '</div>' : '') +
      '<a class="naver-link" href="https://search.naver.com/search.naver?query=' +
      encodeURIComponent(r.name) + '" target="_blank" rel="noopener">N 네이버검색 ↗</a>' +
      '<div class="meta">' +
      '<div><b>주소</b> ' + esc(r.address || '-') + '</div>' +
      (r.address_jibun ? '<div><b>지번</b> ' + esc(r.address_jibun) + '</div>' : '') +
      '<div><b>전화</b> ' + (r.phone ? '<a href="tel:' + esc(r.phone) + '" style="color:#4c8dff">' + esc(r.phone) + '</a>' : '-') + '</div>' +
      '<div><b>운영시간</b> ' + esc(r.hours || '-') + '</div>' +
      '<div><b>분류</b> ' + esc((r.categories || []).join(', ') || r.category || '-') +
      (r.price_tier != null ? ' · 1인 약 ' + (r.price_tier === 0 ? '1만원 미만' : r.price_tier + '만원대') : '') + '</div>' +
      '<div><b>바로예약</b> ' + resv + '</div>' +
      '</div>' +
      (r.description ? '<div class="desc">' + esc(r.description) + '</div>' : '') +
      reserveBlockHtml(r) +
      '<h3>메뉴 (' + (r.menus || []).length + ')</h3>' +
      menusTableHtml(r, true) +
      (r.reservation_note ? '<div class="resv-note">※ ' + esc(r.reservation_note) + '</div>' : '') +
      '<div class="resv-note"><a href="' + esc(r.url) + '" target="_blank" style="color:#4c8dff">원본 페이지 보기 ↗</a></div>';
    document.getElementById('detail').classList.add('open');
    document.getElementById('detail').scrollTop = 0;

    // 모바일용 메뉴 패널도 함께 갱신
    document.getElementById('mini-menu-name').textContent = r.name;
    document.getElementById('mini-menu-body').innerHTML = menusTableHtml(r, false);
    document.getElementById('mini-menu-body').scrollTop = 0;
    document.getElementById('app').classList.add('selected');
  }

  // 선택 해제 (데스크톱 ×, 모바일 메뉴 패널 ×)
  function clearSelection() {
    document.getElementById('detail').classList.remove('open', 'expanded');
    document.getElementById('app').classList.remove('selected');
    state.activeId = null;
    renderList();
    closeOverlay();
  }

  function hideDetail() {
    var detail = document.getElementById('detail');
    if (detail.classList.contains('expanded')) {
      // 모바일 전체화면 상세 → 지도+목록+메뉴 패널 화면으로 복귀 (선택 유지)
      detail.classList.remove('expanded');
    } else {
      clearSelection();
    }
  }

  // ---------- 지도 ----------
  function focusStore(r, opts) {
    opts = opts || {};
    state.activeId = r.store_id;
    renderList();
    var row = document.querySelector('#list tbody tr.active');
    if (row && !opts.fromList) row.scrollIntoView({ block: 'nearest' });

    if (r.lat && r.lng) {
      var pos = new kakao.maps.LatLng(r.lat, r.lng);
      // 목록에서 선택한 경우에만 지도를 이동 (줌 레벨은 유지, 마커 클릭 시엔 이동 없음)
      if (opts.fromList) state.map.panTo(pos);
      openOverlay(r, pos);
    }
    showDetail(r);
  }

  function openOverlay(r, pos) {
    closeOverlay();
    var el = document.createElement('div');
    el.className = 'iw';
    el.innerHTML = esc(r.name) + '<br><small>' + esc(r.gungu) + ' · 메뉴 ' +
      (r.menus || []).length + '개</small>';
    el.onclick = function () { showDetail(r); };
    state.overlay = new kakao.maps.CustomOverlay({
      position: pos, content: el, yAnchor: 1.35, zIndex: 10,
    });
    state.overlay.setMap(state.map);
  }

  function closeOverlay() {
    if (state.overlay) { state.overlay.setMap(null); state.overlay = null; }
  }

  function initMap() {
    state.map = new kakao.maps.Map(document.getElementById('map'), {
      center: new kakao.maps.LatLng(37.52, 126.97), // 서울 용산구 남쪽 한강
      level: 9, // 시·군 단위 배율
    });
    state.map.addControl(new kakao.maps.ZoomControl(), kakao.maps.ControlPosition.RIGHT);
    state.clusterer = new kakao.maps.MarkerClusterer({
      map: state.map, averageCenter: true, minLevel: 7, disableClickZoom: false,
    });

    var markers = [];
    state.all.forEach(function (r) {
      if (!r.lat || !r.lng) return;
      var marker = new kakao.maps.Marker({
        position: new kakao.maps.LatLng(r.lat, r.lng),
        title: r.name,
      });
      kakao.maps.event.addListener(marker, 'click', function () { focusStore(r); });
      state.markers[r.store_id] = marker;
      markers.push(marker);
    });
    state.clusterer.addMarkers(markers);

    kakao.maps.event.addListener(state.map, 'click', closeOverlay);

    // 표시된 곳만 검색: 누르는 순간 지도에 보이는 매장을 스냅샷으로 고정
    document.getElementById('bounds-btn').addEventListener('click', function () {
      if (state.boundsSet) {
        state.boundsSet = null; // 해제 → 전체 목록
      } else {
        var bounds = state.map.getBounds();
        var set = {};
        state.all.forEach(function (r) {
          if (r.lat && r.lng && bounds.contain(new kakao.maps.LatLng(r.lat, r.lng))) {
            set[r.store_id] = true;
          }
        });
        state.boundsSet = set;
      }
      this.classList.toggle('on', !!state.boundsSet);
      applyFilterSort();
    });
  }

  // 좌표 없는 매장은 카카오 지오코더로 보정
  function geocodeMissing(cb) {
    var missing = state.all.filter(function (r) { return !r.lat || !r.lng; });
    if (!missing.length) return cb();
    var geocoder = new kakao.maps.services.Geocoder();
    var left = missing.length;
    missing.forEach(function (r) {
      geocoder.addressSearch(r.address || '', function (res, status) {
        if (status === kakao.maps.services.Status.OK && res[0]) {
          r.lat = parseFloat(res[0].y);
          r.lng = parseFloat(res[0].x);
        }
        if (--left === 0) cb();
      });
    });
  }

  // ---------- 필터 UI ----------
  function initFilters() {
    // 시도 칩 (데이터에서 추출, 많은 순)
    var counts = {};
    state.all.forEach(function (r) { counts[r.sido] = (counts[r.sido] || 0) + 1; });
    var sidos = Object.keys(counts).sort(function (a, b) { return counts[b] - counts[a]; });
    var sidoWrap = document.getElementById('f-sido');
    sidoWrap.innerHTML = sidos.map(function (s) {
      return '<button class="chip" data-sido="' + esc(s) + '">' + esc(s) +
        '<small>' + counts[s] + '</small></button>';
    }).join('');

    var catCounts = {};
    state.all.forEach(function (r) {
      (r.categories || []).forEach(function (c) { catCounts[c] = (catCounts[c] || 0) + 1; });
    });
    var catWrap = document.getElementById('f-cat');
    catWrap.innerHTML = CATEGORIES.map(function (c) {
      return '<button class="chip" data-cat="' + c + '">' + c +
        '<small>' + (catCounts[c] || 0) + '</small></button>';
    }).join('');

    document.getElementById('filter-btn').addEventListener('click', function () {
      document.getElementById('filter-panel').classList.toggle('hidden');
    });
    document.getElementById('f-instant').addEventListener('change', function (e) {
      state.filters.instant = e.target.checked;
      applyFilterSort();
    });
    sidoWrap.addEventListener('click', function (e) {
      var chip = e.target.closest('.chip');
      if (!chip) return;
      var s = chip.dataset.sido;
      if (state.filters.sidos[s]) delete state.filters.sidos[s];
      else state.filters.sidos[s] = true;
      chip.classList.toggle('on');
      applyFilterSort();
    });
    catWrap.addEventListener('click', function (e) {
      var chip = e.target.closest('.chip');
      if (!chip) return;
      var c = chip.dataset.cat;
      if (state.filters.cats[c]) delete state.filters.cats[c];
      else state.filters.cats[c] = true;
      chip.classList.toggle('on');
      applyFilterSort();
    });

    // 예약 시간 칩
    var hourWrap = document.getElementById('f-hours');
    hourWrap.innerHTML = HOUR_RANGE.map(function (hh) {
      return '<button class="chip" data-hour="' + hh + '">' + hh + '시</button>';
    }).join('');
    hourWrap.addEventListener('click', function (e) {
      var chip = e.target.closest('.chip');
      if (!chip) return;
      var hh = +chip.dataset.hour;
      if (state.filters.hours[hh]) delete state.filters.hours[hh];
      else state.filters.hours[hh] = true;
      chip.classList.toggle('on');
      applyFilterSort();
    });

    // 예약 일자 추가/삭제
    var dateInput = document.getElementById('f-date-input');
    var todayStr = new Date().toISOString().slice(0, 10);
    dateInput.min = todayStr;
    dateInput.value = todayStr;
    document.getElementById('f-date-add').addEventListener('click', function () {
      var v = dateInput.value;
      if (!v) return;
      if (state.filters.dates.indexOf(v) === -1) {
        state.filters.dates.push(v);
        state.filters.dates.sort();
        renderDateChips();
        applyFilterSort();
      }
    });
    document.getElementById('f-dates').addEventListener('click', function (e) {
      var chip = e.target.closest('.chip');
      if (!chip) return;
      var d = chip.dataset.date;
      state.filters.dates = state.filters.dates.filter(function (x) { return x !== d; });
      renderDateChips();
      applyFilterSort();
    });

    var lo = document.getElementById('f-price-lo');
    var hi = document.getElementById('f-price-hi');
    function onPrice() {
      var l = +lo.value, h = +hi.value;
      if (l >= h) { // 겹침 방지
        if (this === lo) { l = h - 1; lo.value = l; }
        else { h = l + 1; hi.value = h; }
      }
      state.filters.priceLo = l;
      state.filters.priceHi = h;
      updatePriceLabel();
      applyFilterSort();
    }
    lo.addEventListener('input', onPrice);
    hi.addEventListener('input', onPrice);
    updatePriceLabel();

    document.getElementById('f-reset').addEventListener('click', function () {
      state.filters = { instant: false, sidos: {}, cats: {}, dates: [], hours: {}, priceLo: 0, priceHi: 11 };
      document.getElementById('f-instant').checked = false;
      document.querySelectorAll('#filter-panel .chip.on').forEach(function (c) {
        c.classList.remove('on');
      });
      lo.value = 0; hi.value = 11;
      renderDateChips();
      updatePriceLabel();
      applyFilterSort();
    });
  }

  // 선택된 예약 일자 칩 렌더 ("8/12(수) ×")
  function renderDateChips() {
    var wrap = document.getElementById('f-dates');
    wrap.innerHTML = state.filters.dates.map(function (d) {
      var dt = new Date(d + 'T00:00:00');
      var label = (dt.getMonth() + 1) + '/' + dt.getDate() + '(' + DAY_NAMES[dt.getDay()] + ')';
      return '<button class="chip on" data-date="' + d + '">' + label + ' ×</button>';
    }).join('');
  }

  function updatePriceLabel() {
    var f = state.filters;
    var label = document.getElementById('f-price-label');
    if (f.priceLo === 0 && f.priceHi === 11) {
      label.textContent = '전체';
    } else {
      var loTxt = f.priceLo === 0 ? '0원' : f.priceLo + '만원';
      var hiTxt = f.priceHi === 11 ? '제한없음' : f.priceHi + '만원';
      label.textContent = loTxt + ' ~ ' + hiTxt;
    }
    var fill = document.getElementById('range-fill');
    fill.style.left = (f.priceLo / 11 * 100) + '%';
    fill.style.width = ((f.priceHi - f.priceLo) / 11 * 100) + '%';
  }

  // ---------- 이벤트 ----------
  function bindEvents() {
    document.querySelectorAll('#list thead th').forEach(function (th) {
      th.addEventListener('click', function () {
        var k = th.dataset.key;
        if (state.sortKey === k) state.sortAsc = !state.sortAsc;
        else { state.sortKey = k; state.sortAsc = true; }
        updateSortArrows();
        applyFilterSort();
      });
    });
    document.querySelector('#list tbody').addEventListener('click', function (e) {
      var tr = e.target.closest('tr');
      if (!tr) return;
      var r = state.all.find(function (x) { return x.store_id === +tr.dataset.id; });
      if (r) focusStore(r, { fromList: true });
    });
    document.getElementById('search').addEventListener('input', applyFilterSort);
    document.getElementById('detail-close').addEventListener('click', hideDetail);
    document.getElementById('mini-menu-close').addEventListener('click', clearSelection);
    document.getElementById('mini-detail-btn').addEventListener('click', function () {
      document.getElementById('detail').classList.add('expanded');
      document.getElementById('detail').scrollTop = 0;
    });
    document.getElementById('mini-naver-btn').addEventListener('click', function () {
      if (!state.selectedStore) return;
      window.open('https://search.naver.com/search.naver?query=' +
        encodeURIComponent(state.selectedStore.name), '_blank');
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { hideDetail(); closeOverlay(); }
    });
  }

  // ---------- 부트스트랩 ----------
  fetch('data/restaurants.json')
    .then(function (res) { return res.json(); })
    .then(function (data) {
      data.forEach(function (r) {
        var region = splitRegion(r.address);
        r.sido = region.sido;
        r.gungu = region.gungu;
      });
      state.all = data;
      kakao.maps.load(function () {
        geocodeMissing(function () {
          initMap();
          bindEvents();
          initFilters();
          updateSortArrows();
          applyFilterSort();
        });
      });
    })
    .catch(function (err) {
      document.getElementById('count').textContent = '데이터 로드 실패';
      console.error(err);
    });
})();
