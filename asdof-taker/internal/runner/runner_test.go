package runner

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"asdof-taker/internal/config"
)

// fakeSite 는 jointips 의 예약 흐름만 흉내 내는 최소 서버다.
// 실제 사이트의 write_update.php 응답 형태는 자격증명이 없어 확인하지 못했으므로(§3.6),
// 이 테스트는 "우리 쪽 배선"(폼 필드·판정·재조회 검증·재시도)이 맞는지를 본다.
type fakeSite struct {
	mu       sync.Mutex
	uidSeq   int
	booked   map[int]string // idx → 회의명
	seenUIDs map[string]bool
	submits  []map[string]string
	rejectN  int // 앞의 N번은 alert 로 거절한다

	// gnuboard 의 도배 방지(cf_delay_sec) 흉내. 마지막 등록 뒤 floodWindow 안에 들어온
	// 신청은 "너무 빠른 시간내에…" 로 거절한다.
	floodWindow time.Duration
	lastPost    time.Time
}

func newFakeSite() *fakeSite {
	return &fakeSite{booked: map[int]string{}, seenUIDs: map[string]bool{}}
}

func (f *fakeSite) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/bbs/login_check.php", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("mb_id") != "tester" || r.FormValue("mb_password") != "s3cret" {
			fmt.Fprint(w, `<script>alert('회원아이디 또는 비밀번호가 틀립니다.');</script>`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "sess-1", Path: "/"})
		fmt.Fprint(w, `<script>location.href="/";</script>`)
	})

	mux.HandleFunc("/bbs/write.php", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("PHPSESSID"); err != nil || c.Value == "" {
			fmt.Fprint(w, `<form id="login"></form>`) // 로그인 안 됨 → uid 없음
			return
		}
		f.mu.Lock()
		f.uidSeq++
		uid := fmt.Sprintf("2608251157%04d", f.uidSeq)
		f.mu.Unlock()
		fmt.Fprintf(w, `<form><input type="hidden" name="uid" value="%s"><input name="wr_3"></form>`, uid)
	})

	mux.HandleFunc("/ajax_common.php", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("std") != "location_select_new" {
			fmt.Fprint(w, "NO")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		fmt.Fprint(w, f.rowsHTML())
	})

	mux.HandleFunc("/bbs/write_update.php", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()

		fields := map[string]string{}
		for k := range r.Form {
			fields[k] = r.FormValue(k)
		}
		f.submits = append(f.submits, fields)

		uid := r.FormValue("uid")
		if uid == "" || f.seenUIDs[uid] {
			fmt.Fprint(w, `<script>alert('이미 처리된 요청입니다.');</script>`)
			return
		}
		f.seenUIDs[uid] = true

		if f.rejectN > 0 {
			f.rejectN--
			fmt.Fprint(w, `<script>alert('해당 시간에 이미 예약이 있습니다.');</script>`)
			return
		}
		if f.floodWindow > 0 && !f.lastPost.IsZero() && time.Since(f.lastPost) < f.floodWindow {
			fmt.Fprint(w, `<script>alert('너무 빠른 시간내에 게시물을 연속해서 올릴 수 없습니다.');</script>`)
			return
		}
		start, end := r.FormValue("wr_4"), r.FormValue("wr_5")
		si, ei := slotIdx(start), slotIdx(end)
		if si < 0 || ei <= si {
			fmt.Fprint(w, `<script>alert('시간 설정이 올바르지 않습니다.');</script>`)
			return
		}
		for i := si; i < ei; i++ {
			if _, taken := f.booked[i]; taken {
				fmt.Fprint(w, `<script>alert('해당 시간에 이미 예약이 있습니다.');</script>`)
				return
			}
		}
		for i := si; i < ei; i++ {
			f.booked[i] = r.FormValue("wr_subject")
		}
		f.lastPost = time.Now()
		fmt.Fprint(w, `<script>location.href="/bbs/board.php?bo_table=campus_share_apply";</script>`)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	return mux
}

func slotIdx(hhmm string) int {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return -1
	}
	return (h*60 + m - 9*60) / 30
}

func (f *fakeSite) rowsHTML() string {
	var b strings.Builder
	b.WriteString(`<tr data-wr8="41" data-wr6="3" data-wr7="25"><td>현승빌딩 4층</td><td>회의실 A</td><td></td><td>4명</td>`)
	for i := 0; i < 24; i++ {
		tm := fmt.Sprintf("%02d:%02d", (9*60+i*30)/60, (9*60+i*30)%60)
		if subj, taken := f.booked[i]; taken {
			fmt.Fprintf(&b, `<td class="selected tooltip2" data-time="%s" idx="%d" title="%s" share_id="9001"></td>`, tm, i, subj)
			continue
		}
		fmt.Fprintf(&b, `<td class="available tooltip2" data-time="%s" idx="%d" title="%s"></td>`, tm, i, tm)
	}
	b.WriteString(`<td class="selected" data-time="21:00" style="display:none;"></td></tr>`)
	return b.String()
}

// ── 테스트 준비 ────────────────────────────────────────────────────────────

func setup(t *testing.T, base string, mutate func(*config.Config)) (*config.Store, *Runner) {
	t.Helper()
	t.Setenv("TAKER_SECRET_KEY", "")
	dir := t.TempDir()
	vault, err := config.OpenVault(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(filepath.Join(dir, "config.json"), vault)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := vault.Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(func(c *config.Config) error {
		c.Site.BaseURL = base
		c.Site.Username = "tester"
		c.Site.PasswordEnc = enc
		c.Site.TimeoutS = 5
		c.Booking.Manager = "홍길동"
		c.Booking.Contact = "010-0000-0000"
		c.Booking.Subject = "주간 정기 회의"
		c.Booking.Targets = []config.Target{
			{RoomID: 41, Building: "3", Floor: "25", Start: "13:00", DurationSlots: 8},
		}
		c.Booking.Count = 1
		c.Booking.GapSeconds = 0 // 테스트는 도배 방지 대기 없이 (아래 전용 테스트에서만 켠다)
		c.Booking.Fallback.Enabled = false
		c.Runtime.DryRun = false
		c.Schedule.Retry = config.Retry{IntervalMS: 50, MaxDurationS: 10, MaxAttempts: 10}
		if mutate != nil {
			mutate(c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store, New(store, slog.New(slog.DiscardHandler))
}

func waitDone(t *testing.T, rn *Runner) Run {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if rn.Current() == nil {
			if h := rn.History(1); len(h) == 1 {
				return h[0]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("실행이 끝나지 않았습니다")
	return Run{}
}

// ── 테스트 ────────────────────────────────────────────────────────────────

func TestRunBooksSuccessfully(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	if _, err := rn.Start(context.Background(), Options{Mode: "manual"}); err != nil {
		t.Fatal(err)
	}
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if len(run.Bookings) != 1 {
		t.Fatalf("예약 %d건, 1건이어야 함", len(run.Bookings))
	}
	if run.Bookings[0].Room != "현승 4A" {
		t.Errorf("회의실 표기 = %q, want 현승 4A", run.Bookings[0].Room)
	}
	if run.Bookings[0].TimeRange != "13:00~17:00" {
		t.Errorf("시간 = %q, want 13:00~17:00", run.Bookings[0].TimeRange)
	}

	// ★ 실제 전송된 폼에서 wr_5 가 배타적 종료시각인지 확인한다(§3.5).
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 1 {
		t.Fatalf("신청 %d회, 1회여야 함", len(site.submits))
	}
	f := site.submits[0]
	for k, want := range map[string]string{
		"wr_4": "13:00", "wr_5": "17:00", "wr_8": "41", "wr_6": "3", "wr_7": "25",
		"wr_1": "T", "wr_2": "회의실이용신청", "wr_19": "승인", "wr_22": "입주사전용",
		"bo_table": "campus_share_apply", "wr_id": "0", "wr_17": "홍길동",
	} {
		if f[k] != want {
			t.Errorf("%s = %q, want %q", k, f[k], want)
		}
	}
	if f["wr_3"] == "" || !strings.Contains(f["wr_3"], ".") {
		t.Errorf("wr_3 = %q, YYYY.MM.DD 형식이어야 함", f["wr_3"])
	}
}

func TestRunRetriesWithFreshUIDs(t *testing.T) {
	site := newFakeSite()
	site.rejectN = 2 // 앞 두 번은 거절
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("재시도 후에도 실패: %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if run.Attempts != 3 {
		t.Errorf("시도 %d회, 3회여야 함", run.Attempts)
	}

	// 매 시도가 서로 다른 uid 를 써야 한다(§3.2 uid 풀).
	site.mu.Lock()
	defer site.mu.Unlock()
	seen := map[string]bool{}
	for i, f := range site.submits {
		if f["uid"] == "" {
			t.Fatalf("시도 %d 에 uid 가 없다", i+1)
		}
		if seen[f["uid"]] {
			t.Errorf("uid %q 를 재사용했다", f["uid"])
		}
		seen[f["uid"]] = true
	}
}

func TestRunStopsOnQuotaAlert(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	// 이미 그 시간대가 차 있으면 겹침 alert 가 나온다 → 여기서는 한도 초과 메시지를 흉내 낸다.
	site.rejectN = 99
	_, rn := setup(t, srv.URL, nil)
	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusFailed {
		t.Fatalf("status = %s, 실패여야 함", run.Status)
	}
	if run.Attempts > 10 {
		t.Errorf("시도 %d회 — 재시도 상한(10)을 넘었다", run.Attempts)
	}
}

func TestDryRunSendsNoWrite(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	rn.Start(context.Background(), Options{Mode: "manual", DryRun: true})
	run := waitDone(t, rn)
	if run.Status != StatusSkipped {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 0 {
		t.Fatalf("드라이런인데 write_update.php 를 %d회 호출했다", len(site.submits))
	}
	// 페이로드 미리보기에 wr_5 가 계산돼 나와야 한다(§11 1단계).
	if !strings.Contains(dumpLogs(run), "wr_5=17:00") {
		t.Errorf("드라이런 출력에 wr_5 가 없다\n%s", dumpLogs(run))
	}
}

func TestLoginFailureStopsImmediately(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	store, rn := setup(t, srv.URL, nil)
	store.Update(func(c *config.Config) error {
		enc, _ := store.Vault().Encrypt("wrong")
		c.Site.PasswordEnc = enc
		return nil
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusFailed || run.Message != "로그인 실패" {
		t.Fatalf("status=%s msg=%q, 로그인 실패여야 함", run.Status, run.Message)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 0 {
		t.Error("로그인 실패인데 예약을 신청했다")
	}
}

func TestFallbackFindsFreeSlot(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	// 지정 대상(13:00 8슬롯) 구간을 남이 먼저 잡은 상태로 만든다.
	for i := 8; i < 16; i++ {
		site.booked[i] = "[남] 다른 회의"
	}
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Fallback = config.Fallback{
			Enabled: true, Building: "3", PreferFloor: "25",
			Start: "13:00", StartWindowMinutes: 240, MinSlots: 2, MaxSlots: 8,
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("폴백으로도 못 잡았다: %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	// 17:00 부터 20:30 까지 8슬롯이 비어 있으므로 거기 잡혀야 한다.
	if len(run.Bookings) != 1 || run.Bookings[0].TimeRange != "17:00~21:00" {
		t.Errorf("예약 = %+v, want 17:00~21:00 1건", run.Bookings)
	}
}

func TestPreciseFireTiming(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	fire := time.Now().Add(1200 * time.Millisecond)
	rn.Start(context.Background(), Options{Mode: "scheduled", FireAt: &fire})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)", run.Status, run.Message)
	}
	// §13-7: 목표 시각과의 차이가 통상 ±20ms 이내. CI 지터를 감안해 100ms 로 본다.
	if run.OffsetMS < 0 || run.OffsetMS > 100 {
		t.Errorf("발사 오차 %dms — 목표 직후에 발사돼야 한다", run.OffsetMS)
	}
}

func TestConcurrentStartIsRejected(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	fire := time.Now().Add(2 * time.Second)
	if _, err := rn.Start(context.Background(), Options{Mode: "scheduled", FireAt: &fire}); err != nil {
		t.Fatal(err)
	}
	if _, err := rn.Start(context.Background(), Options{Mode: "manual"}); err == nil {
		t.Error("동시에 두 번 실행됐다")
	}
	rn.Cancel()
	waitDone(t, rn)
}

func dumpLogs(r Run) string {
	var b strings.Builder
	for _, l := range r.Logs {
		fmt.Fprintf(&b, "  [%s] %s\n", l.Level, l.Msg)
	}
	return b.String()
}

// ★ 하루 2건을 각각 잡아야 한다 — targets 는 우선순위 목록이자 '확보할 자리' 목록이다.
func TestBooksTwoReservations(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.Targets = []config.Target{
			{RoomID: 41, Building: "3", Floor: "25", Start: "14:00", DurationSlots: 8},
			{RoomID: 41, Building: "3", Floor: "25", Start: "09:00", DurationSlots: 8},
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if len(run.Bookings) != 2 {
		t.Fatalf("예약 %d건, 2건이어야 함: %+v\n%s", len(run.Bookings), run.Bookings, dumpLogs(run))
	}
	got := map[string]bool{}
	for _, b := range run.Bookings {
		got[b.TimeRange] = true
	}
	for _, want := range []string{"14:00~18:00", "09:00~13:00"} {
		if !got[want] {
			t.Errorf("%s 예약이 없다: %+v", want, run.Bookings)
		}
	}
	// 두 건이 서로 다른 uid 로, 각각 한 번씩만 나갔어야 한다.
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 2 {
		t.Errorf("신청 %d회, 2회여야 함", len(site.submits))
	}
}

// 한 건만 가능한 상황이면 부분 성공으로 남기고, 잡은 건은 살린다.
func TestPartialWhenSecondSlotTaken(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	for i := 0; i < 8; i++ { // 09:00~13:00 을 남이 선점
		site.booked[i] = "[남] 다른 회의"
	}
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.Targets = []config.Target{
			{RoomID: 41, Building: "3", Floor: "25", Start: "14:00", DurationSlots: 8},
			{RoomID: 41, Building: "3", Floor: "25", Start: "09:00", DurationSlots: 8},
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusPartial {
		t.Fatalf("status = %s, partial 이어야 함 (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if len(run.Bookings) != 1 || run.Bookings[0].TimeRange != "14:00~18:00" {
		t.Errorf("확보한 예약 = %+v, want 14:00~18:00 1건", run.Bookings)
	}
	if run.Want != 2 {
		t.Errorf("Want = %d, want 2", run.Want)
	}
}

// ★ 도배 방지: 한 건 잡은 뒤 사이트가 연속 등록을 막으면, 두들기지 말고 기다렸다가 두 번째를 잡아야 한다.
// (2026-08-28 실운영에서 이걸 몰라 39번 헛방을 치고 1/2건만 확보했다.)
func TestWaitsOutFloodProtection(t *testing.T) {
	site := newFakeSite()
	site.floodWindow = 1500 * time.Millisecond
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.GapSeconds = 2 // 도배 방지 창(1.5초)보다 넉넉히
		c.Booking.Targets = []config.Target{
			{RoomID: 41, Building: "3", Floor: "25", Start: "14:00", DurationSlots: 8},
			{RoomID: 41, Building: "3", Floor: "25", Start: "09:00", DurationSlots: 8},
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess || len(run.Bookings) != 2 {
		t.Fatalf("2건을 확보하지 못했다: %s %+v\n%s", run.Status, run.Bookings, dumpLogs(run))
	}

	// 핵심: 도배 방지에 걸린 채로 재시도를 난사하지 않았어야 한다.
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 2 {
		t.Errorf("신청 %d회 — 미리 기다렸다면 정확히 2회여야 한다", len(site.submits))
	}
	if !strings.Contains(dumpLogs(run), "도배 방지 대기") {
		t.Errorf("대기 로그가 없다\n%s", dumpLogs(run))
	}
}

// 도배 방지가 계속 걸리면 무한히 매달리지 않고 확보한 것만 남기고 끝나야 한다.
func TestGivesUpWhenFloodNeverClears(t *testing.T) {
	site := newFakeSite()
	site.floodWindow = time.Hour // 절대 안 풀림
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.GapSeconds = 1
		c.Booking.Targets = []config.Target{
			{RoomID: 41, Building: "3", Floor: "25", Start: "14:00", DurationSlots: 8},
			{RoomID: 41, Building: "3", Floor: "25", Start: "09:00", DurationSlots: 8},
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusPartial || len(run.Bookings) != 1 {
		t.Fatalf("첫 건만 남기고 끝나야 한다: %s %+v\n%s", run.Status, run.Bookings, dumpLogs(run))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) > 6 {
		t.Errorf("신청 %d회 — 포기 한도(도배 대기 3회)를 넘겨 매달렸다", len(site.submits))
	}
}
