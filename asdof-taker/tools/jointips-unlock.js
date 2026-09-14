/* jointips 회의실 예약 — 조회 날짜 제한 해제 (콘솔용)
 *
 * 무엇을 하는가
 *   예약 페이지의 searchLoc() 안에 있는 '오늘~+6일' 검사 한 블록만 제거한 함수로 교체한다.
 *   나머지 동작(빈 슬롯 AJAX 조회 → 시간표 렌더 → 툴팁 → 예약목록)은 원본 그대로다.
 *
 * 왜 이걸로 되는가
 *   그 검사는 페이지 JS 안에만 있다. 경계 상수("2026-08-25" / "2026-09-01")가 페이지 전체에서
 *   searchLoc() 한 곳에만 등장하고, 실제 신청 경로인 fwrite_submit() 에는 날짜 검사가 없다.
 *   서버(write_update.php)가 날짜를 검증하는지는 별개이며 아직 확인되지 않았다 —
 *   신청까지 통과하는지는 직접 눌러 봐야 안다.
 *
 * 쓰는 법
 *   1. https://www.jointips.or.kr 로그인 → 회의실 예약(입주사전용) 페이지
 *   2. F12 → Console 에 이 파일 전체를 붙여넣고 Enter
 *   3. jt("2026-09-30")        ← 날짜만. 건물은 현재 선택된 것 사용
 *      jt("2026-09-30", 3)     ← 현승빌딩으로 지정 (1 명우 · 2 해성 · 3 현승 · 4 회성)
 *      jt.reset()              ← 원래 함수로 되돌리기
 *   그 뒤엔 평소처럼 시간표에서 칸을 클릭해 신청하면 된다.
 *
 * 크롬 확장으로 옮길 때
 *   content script 는 기본적으로 격리된 세계(isolated world)에서 돌아 페이지의 searchLoc 을
 *   덮어쓸 수 없다. manifest v3 에서 "world": "MAIN" 으로 선언하거나, 이 파일을 <script> 태그로
 *   페이지에 주입해야 한다. 아래 IIFE 본문은 그대로 옮겨도 동작하도록 써 두었다.
 */
(function () {
  "use strict";

  var $ = window.jQuery;
  if (!$) return console.error("[jt] jQuery 를 찾을 수 없습니다. 예약 페이지에서 실행하세요.");
  if (typeof window.searchLoc !== "function")
    return console.error("[jt] searchLoc() 이 없습니다. 회의실 예약 페이지가 맞는지 확인하세요.");

  // 두 번 붙여넣어도 원본을 잃지 않도록, 이미 잡아둔 원본이 있으면 그걸 유지한다.
  var orig = window.jt && window.jt.__orig ? window.jt.__orig : window.searchLoc;

  // 원본 searchLoc() 에서 날짜 범위 검사만 뺀 판.
  function searchLocUnlocked() {
    var pWR_2 = $(':radio[name="wr_6_v"]:checked').val();
    var pWR_3 = $("#wr_3").val();
    var pWR_4 = "회의공간";

    if (pWR_3 == "") { alert("날짜를 선택해 주세요."); return false; }
    /* ── 제거된 원본 블록 ──────────────────────────────────
       if (!(pWR_3.split('.').join('-') <  "<오늘+7>"
          && pWR_3.split('.').join('-') >= "<오늘>"))
       { alert("회의실 예약의 경우, 오늘로부터 7일 이내로만 예약할 수 있습니다."); return false; }
       ─────────────────────────────────────────────────── */
    if ($(':radio[name="wr_6_v"]:checked').length == 0) { alert("건물을 선택해 주세요."); return false; }

    var pStr = $.ajax({
      url: "/ajax_common.php", async: false, type: "POST",
      data: ({ std: "location_select_new", wr_2: pWR_2, wr_3: pWR_3, wr_4: pWR_4 })
    }).responseText;

    $("#tbodyTimeTable").html(pStr);
    try { $(".tooltip2").tooltip(); } catch (e) {}
    $("#spBldgNm").html($(':radio[name="wr_6_v"]:checked').parent().text());
    $(".cssSrchResult").show();
    $(".cssApplyInfo").hide();
    if (typeof window.GetBooking_List === "function") window.GetBooking_List();
    return true;
  }

  window.searchLoc = searchLocUnlocked;

  // 날짜 입력칸이 달력 위젯에 묶여 먼 날짜를 못 고르는 경우를 대비해 경계를 푼다.
  function freeDatepicker() {
    var $d = $(".cssDate");
    $d.removeAttr("readonly").removeAttr("min").removeAttr("max");
    try { $d.datepicker("option", { minDate: null, maxDate: null }); } catch (e) {}   // jQuery UI
    try { $d.datepicker("setStartDate", null); } catch (e) {}                          // bootstrap-datepicker
    try { $d.datepicker("setEndDate", null); } catch (e) {}
  }
  freeDatepicker();

  // jt("2026-09-30" | "2026.09.30", 건물코드?) — 날짜를 넣고 조회까지 한 번에.
  function jt(date, building) {
    if (!date) return console.warn('[jt] 예: jt("2026-09-30") 또는 jt("2026-09-30", 3)');
    var d = String(date).trim().replace(/-/g, ".");           // 입력칸은 점 구분을 쓴다
    if (!/^\d{4}\.\d{2}\.\d{2}$/.test(d))
      return console.error("[jt] 날짜 형식이 잘못됐습니다:", date, "(YYYY-MM-DD 또는 YYYY.MM.DD)");

    if (building != null) {
      var $r = $(':radio[name="wr_6_v"][value="' + building + '"]');
      if (!$r.length)
        return console.error("[jt] 건물 코드가 없습니다:", building, "(1 명우 · 2 해성 · 3 현승 · 4 회성)");
      $r.prop("checked", true).trigger("click");
    }
    if ($(':radio[name="wr_6_v"]:checked').length === 0)
      return console.error('[jt] 건물을 먼저 고르거나 두 번째 인자로 넘기세요. 예: jt("2026-09-30", 3)');

    freeDatepicker();
    $("#wr_3").val(d).trigger("change");
    var ok = window.searchLoc();
    console.log(ok === false ? "[jt] 조회 실패"
      : "[jt] 조회함 → " + d + " (건물 " + $(':radio[name="wr_6_v"]:checked').val() + ")");
    return ok;
  }

  jt.__orig = orig;
  jt.reset = function () {
    window.searchLoc = orig;
    console.log("[jt] 원래 searchLoc() 으로 되돌렸습니다.");
  };
  window.jt = jt;

  console.log(
    "%c[jt] 조회 날짜 제한 해제됨%c\n" +
    '  jt("2026-09-30")      날짜만 (건물은 현재 선택된 것)\n' +
    '  jt("2026-09-30", 3)   건물 지정 — 1 명우 · 2 해성 · 3 현승 · 4 회성\n' +
    "  jt.reset()            원상복구\n" +
    "  ※ 조회만 뚫린 상태입니다. 신청까지 서버가 받아주는지는 별개이며 확인되지 않았습니다.",
    "color:#2f6f4e;font-weight:700", "color:inherit");
})();
