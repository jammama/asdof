package runner

import (
	"context"
	"encoding/json"
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

// fakeSite 는 2026-09 개편 이후 jointips 의 예약 API 만 흉내 내는 최소 서버다.
// "우리 쪽 배선"(페이로드·판정·재조회 검증·재시도)이 맞는지를 본다.
type fakeSite struct {
	mu sync.Mutex
	// booked 는 슬롯 인덱스 → 잡은 사람("me" = 우리 계정).
	booked  map[int]string
	mine    []map[string]any
	submits []map[string]any
	rejectN int    // 앞의 N번은 message 로 거절한다
	rejMsg  string // 거절 사유 (비우면 자리 선점 메시지)
	dayOff  bool   // 운영 요일이 아님

	// perAccountLimit 이 >0 이면 계정별로 그만큼만 받고 그 뒤로는 한도 메시지를 돌려준다.
	// 사이트의 "1일 최대 N회" 를 흉내 낸다.
	perAccountLimit int
	accepted        map[string]int // 토큰 → 받아 준 건수
	tokens          map[string]string
}

const (
	fakeBldgCd  = "BLDG004"
	fakeSpaceCd = "BLDG004_05_001"
	fakeSlots   = 18 // 09:00~18:00, 30분 단위
)

func newFakeSite() *fakeSite {
	return &fakeSite{booked: map[int]string{}, accepted: map[string]int{}, tokens: map[string]string{}}
}

func slotIdx(hhmm string) int {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return -1
	}
	return (h*60 + m - 9*60) / 30
}

func slotTime(i int) string { return fmt.Sprintf("%02d:%02d", (9*60+i*30)/60, (9*60+i*30)%60) }

func (f *fakeSite) handler() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, data any) {
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data})
	}
	fail := func(w http.ResponseWriter, msg string) {
		json.NewEncoder(w).Encode(map[string]any{"code": 400, "message": msg})
	}

	mux.HandleFunc("POST /api/cms/member/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Email, Password string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Password != "s3cret" {
			fail(w, "아이디 또는 비밀번호가 올바르지 않습니다.")
			return
		}
		f.mu.Lock()
		tok := "tok-" + in.Email
		f.tokens[tok] = in.Email
		f.mu.Unlock()
		ok(w, map[string]any{"memberToken": tok, "userNm": "회원 " + in.Email, "email": in.Email})
	})

	mux.HandleFunc("GET /api/cms/public/reservation/policy/bookable-days", func(w http.ResponseWriter, r *http.Request) {
		days := map[string]string{"MON": "Y", "TUE": "Y", "WED": "Y", "THU": "Y", "FRI": "Y", "SAT": "Y", "SUN": "Y"}
		if f.dayOff {
			for k := range days {
				days[k] = "N"
			}
		}
		ok(w, days)
	})

	mux.HandleFunc("GET /api/cms/public/reservation/buildings", func(w http.ResponseWriter, r *http.Request) {
		ok(w, []map[string]any{{"bldgCd": fakeBldgCd, "bldgNm": "현승빌딩(S3)", "regionCd": "SEOUL"}})
	})

	mux.HandleFunc("GET /api/cms/public/reservation/spaces", func(w http.ResponseWriter, r *http.Request) {
		ok(w, []map[string]any{{
			"spaceCd": fakeSpaceCd, "bldgCd": fakeBldgCd, "bldgNm": "현승빌딩(S3)",
			"spaceNm": "5층 회의실A", "floorNo": "5", "capacity": 4,
			"openTime": "09:00", "closeTime": "18:00", "slotUnit": 30,
			"reserveStatus": "Y", "useYn": "Y",
			"dayMonYn": "Y", "dayTueYn": "Y", "dayWedYn": "Y", "dayThuYn": "Y",
			"dayFriYn": "Y", "daySatYn": "Y", "daySunYn": "Y",
		}})
	})

	mux.HandleFunc("GET /api/cms/public/reservation/slots-batch", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok(w, []map[string]any{f.slotsPayload()})
	})
	mux.HandleFunc("GET /api/cms/public/reservation/slots", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok(w, f.slotsPayload())
	})

	mux.HandleFunc("POST /api/cms/member/reservation", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		in["_token"] = tok
		f.submits = append(f.submits, in)

		if f.perAccountLimit > 0 && f.accepted[tok] >= f.perAccountLimit {
			fail(w, "해당 일자에 이미 "+fmt.Sprint(f.perAccountLimit)+"회 예약하셨습니다. (1일 최대 "+fmt.Sprint(f.perAccountLimit)+"회)")
			return
		}
		if f.rejectN > 0 {
			f.rejectN--
			msg := f.rejMsg
			if msg == "" {
				msg = "해당 시간에 이미 예약이 있습니다."
			}
			fail(w, msg)
			return
		}
		times, _ := in["slotTimes"].([]any)
		if len(times) == 0 {
			fail(w, "시간을 선택해주세요.")
			return
		}
		idx := make([]int, 0, len(times))
		for _, t := range times {
			i := slotIdx(fmt.Sprint(t))
			if i < 0 || i >= fakeSlots {
				fail(w, "시간 설정이 올바르지 않습니다.")
				return
			}
			if _, taken := f.booked[i]; taken {
				fail(w, "해당 시간에 이미 예약이 있습니다.")
				return
			}
			idx = append(idx, i)
		}
		for _, i := range idx {
			f.booked[i] = "me"
		}
		start := slotTime(idx[0])
		end := slotTime(idx[len(idx)-1] + 1)
		f.accepted[tok]++
		id := fmt.Sprintf("res-%d", len(f.mine)+1)
		f.mine = append(f.mine, map[string]any{
			"_token":    tok,
			"reserveId": id, "bldgCd": fakeBldgCd, "bldgNm": "현승빌딩(S3)",
			"spaceCd": fakeSpaceCd, "spaceNm": "5층 회의실A",
			"reserveDate": in["reserveDate"], "startTime": start, "endTime": end,
			"title": in["title"], "status": "PENDING",
		})
		ok(w, map[string]any{"reserveId": id})
	})

	mux.HandleFunc("GET /api/cms/member/reservation/my", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		// 내 예약은 계정별이다 — 다른 계정이 잡은 건 보이면 안 된다.
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		out := []map[string]any{}
		for _, m := range f.mine {
			if m["_token"] == tok {
				out = append(out, m)
			}
		}
		ok(w, out)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	return mux
}

func (f *fakeSite) slotsPayload() map[string]any {
	slots := make([]map[string]any, 0, fakeSlots)
	for i := 0; i < fakeSlots; i++ {
		status := "AVAILABLE"
		if _, taken := f.booked[i]; taken {
			status = "UNAVAILABLE"
		}
		slots = append(slots, map[string]any{
			"slotTime": slotTime(i), "endTime": slotTime(i + 1), "status": status,
		})
	}
	return map[string]any{
		"spaceCd": fakeSpaceCd, "spaceNm": "5층 회의실A", "bldgCd": fakeBldgCd,
		"openTime": "09:00", "closeTime": "18:00", "slotUnit": 30,
		"dayBookable": !f.dayOff, "dailyLimitHour": 5.0, "dailyLimitCount": 2,
		"singleLimitHour": 3.0, "slots": slots,
	}
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
		c.Site.Accounts = []config.Account{
			{ID: "acc-1", Username: "tester@example.com", PasswordEnc: enc, Enabled: true},
		}
		c.Site.TimeoutS = 5
		c.Booking.Manager = "홍길동"
		c.Booking.Subject = "주간 정기 회의"
		c.Booking.Targets = []config.Target{
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Label: "현승빌딩(S3) 5층 회의실A",
				Start: "13:00", DurationSlots: 6},
		}
		c.Booking.Count = 1
		c.Booking.GapSeconds = 0
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

func dumpLogs(r Run) string {
	var b strings.Builder
	for _, l := range r.Logs {
		fmt.Fprintf(&b, "  [%s] %s\n", l.Level, l.Msg)
	}
	return b.String()
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
	if run.Bookings[0].Room != "현승 5A" {
		t.Errorf("회의실 표기 = %q, want 현승 5A", run.Bookings[0].Room)
	}
	if run.Bookings[0].TimeRange != "13:00~16:00" {
		t.Errorf("시간 = %q, want 13:00~16:00", run.Bookings[0].TimeRange)
	}

	// ★ 실제 보낸 페이로드 — 새 API 는 연속된 slotTimes 배열을 받는다.
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 1 {
		t.Fatalf("신청 %d회, 1회여야 함", len(site.submits))
	}
	s := site.submits[0]
	if s["spaceCd"] != fakeSpaceCd || s["bldgCd"] != fakeBldgCd {
		t.Errorf("공간 = %v/%v", s["bldgCd"], s["spaceCd"])
	}
	if s["category"] != "COMMON" || s["title"] != "주간 정기 회의" {
		t.Errorf("category/title = %v/%v", s["category"], s["title"])
	}
	if s["purposeCd"] != config.DefaultPurposeCd {
		t.Errorf("purposeCd = %v, want %s", s["purposeCd"], config.DefaultPurposeCd)
	}
	times, _ := s["slotTimes"].([]any)
	var got []string
	for _, v := range times {
		got = append(got, fmt.Sprint(v))
	}
	want := "13:00,13:30,14:00,14:30,15:00,15:30"
	if strings.Join(got, ",") != want {
		t.Errorf("slotTimes = %v, want %s", got, want)
	}
	// 날짜는 YYYY-MM-DD 하나로 통일됐다 (예전의 YYYY.MM.DD 는 사라졌다).
	if d, _ := s["reserveDate"].(string); len(d) != 10 || strings.Count(d, "-") != 2 {
		t.Errorf("reserveDate = %q, YYYY-MM-DD 여야 함", d)
	}
}

func TestRunRetriesOnRejection(t *testing.T) {
	site := newFakeSite()
	site.rejectN = 2 // 앞 두 번은 자리 선점으로 거절
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
}

// 한도 메시지는 오늘 안에 풀리지 않는다 — 두들기지 말고 즉시 멈춰야 한다.
func TestStopsImmediatelyOnDailyLimit(t *testing.T) {
	site := newFakeSite()
	site.rejectN = 99
	site.rejMsg = "해당 일자에 이미 2회 예약하셨습니다. (1일 최대 2회)"
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusFailed {
		t.Fatalf("status = %s, 실패여야 함", run.Status)
	}
	if run.Attempts != 1 {
		t.Errorf("시도 %d회 — 한도 메시지를 받으면 한 번에 멈춰야 한다\n%s", run.Attempts, dumpLogs(run))
	}
}

// 자리 선점은 다시 쏴 볼 가치가 있지만, 재시도 상한은 지켜야 한다.
func TestGivesUpAfterRetryBudget(t *testing.T) {
	site := newFakeSite()
	site.rejectN = 99
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
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
		t.Fatalf("드라이런인데 신청을 %d회 보냈다", len(site.submits))
	}
	// 미리보기에 실제로 보낼 슬롯 목록이 나와야 한다.
	if !strings.Contains(dumpLogs(run), "13:00,13:30,14:00") {
		t.Errorf("드라이런 출력에 slotTimes 가 없다\n%s", dumpLogs(run))
	}
}

func TestLoginFailureStopsImmediately(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	store, rn := setup(t, srv.URL, nil)
	store.Update(func(c *config.Config) error {
		enc, _ := store.Vault().Encrypt("wrong")
		c.Site.Accounts[0].PasswordEnc = enc
		return nil
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusFailed || run.Message != "로그인 실패" {
		t.Fatalf("status=%s msg=%q, 로그인 실패여야 함\n%s", run.Status, run.Message, dumpLogs(run))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 0 {
		t.Error("로그인 실패인데 예약을 신청했다")
	}
}

// 운영 요일이 아니면 쏘기 전에 멈춰야 한다 — 신청해 봐야 전부 거절이다.
func TestSkipsClosedWeekday(t *testing.T) {
	site := newFakeSite()
	site.dayOff = true
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusFailed || run.Message != "운영 요일 아님" {
		t.Fatalf("status=%s msg=%q\n%s", run.Status, run.Message, dumpLogs(run))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 0 {
		t.Error("운영하지 않는 요일인데 신청했다")
	}
}

func TestFallbackFindsFreeSlot(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	// 지정 대상(13:00 부터 6슬롯) 구간을 남이 먼저 잡은 상태로 만든다.
	for i := slotIdx("13:00"); i < slotIdx("16:00"); i++ {
		site.booked[i] = "남"
	}
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Fallback = config.Fallback{
			Enabled: true, BldgCd: fakeBldgCd, PreferFloor: "5",
			Start: "09:00", StartWindowMinutes: 240, MinSlots: 2, MaxSlots: 4,
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("폴백으로도 못 잡았다: %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	// 09:00 부터 4슬롯이 비어 있으므로 거기 잡혀야 한다.
	if len(run.Bookings) != 1 || run.Bookings[0].TimeRange != "09:00~11:00" {
		t.Errorf("예약 = %+v, want 09:00~11:00 1건", run.Bookings)
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
	// 목표 시각과의 차이는 통상 ±20ms. CI 지터를 감안해 100ms 로 본다.
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

// ★ 하루 2건을 각각 잡아야 한다 — targets 는 우선순위 목록이자 '확보할 자리' 목록이다.
// 1일 한도가 5시간이라 2시간씩 두 건으로 잡는다.
func TestBooksTwoReservations(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.Targets = []config.Target{
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "14:00", DurationSlots: 4},
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4},
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
	for _, want := range []string{"14:00~16:00", "09:00~11:00"} {
		if !got[want] {
			t.Errorf("%s 예약이 없다: %+v", want, run.Bookings)
		}
	}
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
	for i := slotIdx("09:00"); i < slotIdx("11:00"); i++ { // 09:00~11:00 을 남이 선점
		site.booked[i] = "남"
	}
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.Targets = []config.Target{
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "14:00", DurationSlots: 4},
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4},
		}
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusPartial {
		t.Fatalf("status = %s, partial 이어야 함 (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if len(run.Bookings) != 1 || run.Bookings[0].TimeRange != "14:00~16:00" {
		t.Errorf("확보한 예약 = %+v, want 14:00~16:00 1건", run.Bookings)
	}
	if run.Want != 2 {
		t.Errorf("Want = %d, want 2", run.Want)
	}
}

// 건 사이 간격은 지켜야 한다 — 두 건을 같은 순간에 쏘지 않는다.
func TestKeepsGapBetweenBookings(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Count = 2
		c.Booking.GapSeconds = 1
		c.Booking.Targets = []config.Target{
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "14:00", DurationSlots: 4},
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4},
		}
	})

	started := time.Now()
	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess || len(run.Bookings) != 2 {
		t.Fatalf("2건을 확보하지 못했다: %s %+v\n%s", run.Status, run.Bookings, dumpLogs(run))
	}
	if time.Since(started) < time.Second {
		t.Error("간격을 지키지 않고 연달아 쐈다")
	}
	if !strings.Contains(dumpLogs(run), "연속 신청 간격") {
		t.Errorf("간격 대기 로그가 없다\n%s", dumpLogs(run))
	}
}

// 신청이 200 을 받아도 내 예약 목록에 없으면 성공으로 치지 않는다.
func TestDoesNotTrustSubmitAlone(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	// 신청은 받아 주되 목록에는 남기지 않는 사이트 — 판정 근거가 목록임을 확인한다.
	h := site.handler()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cms/member/reservation/my", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": []any{}})
	})
	mux.Handle("/", h)
	srv.Config.Handler = mux
	_, rn := setup(t, srv.URL, nil)

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status == StatusSuccess {
		t.Fatalf("목록에 없는데 성공으로 판정했다\n%s", dumpLogs(run))
	}
	if !strings.Contains(dumpLogs(run), "확인되지 않음") {
		t.Errorf("재조회 실패 로그가 없다\n%s", dumpLogs(run))
	}
}

// 예약 창 판정은 우리 시계가 아니라 '사이트 시계' 기준이어야 한다.
// 자정 직후에 쏘기 때문에, 두 시계가 자정 경계를 사이에 두고 갈리면 아직 열리지도 않은
// 날짜를 잡으러 가게 된다.
func TestCheckWindow(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	// 우리 시계로는 9/15 00:00:01 이지만, 사이트가 2초 느려 그쪽은 아직 9/14 다.
	base := time.Date(2026, 9, 15, 0, 0, 1, 0, loc)
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, loc) }

	cases := []struct {
		name    string
		target  time.Time
		offset  time.Duration
		wantErr bool
	}{
		{"오늘", day(15), 0, false},
		{"창 경계 +7", day(22), 0, false},
		{"창 밖 +8", day(23), 0, true},
		{"지난 날짜", day(14), 0, true},
		// ★ 이 가드의 존재 이유. 대상 날짜는 '발사 시각 + offset' 으로 만들어지므로
		// 우리 시계 기준으로는 언제나 창 안이다. 창을 벗어나는 건 오직 사이트 시계가
		// 우리와 자정 경계를 사이에 두고 갈릴 때뿐이다 — 자정 직후에 쏘는 봇의 고유 위험.
		// 사이트가 2초 느리면 그쪽은 아직 9/14 라, 우리가 노리는 9/22(+7)는 그쪽 기준 +8 이다.
		{"자정 직전 사이트 시계 — 경계", day(21), -2 * time.Second, false},
		{"자정 직전 사이트 시계 — 창 밖", day(22), -2 * time.Second, true},
		// 개편 전 사이트는 39분 느렸다. 그때 자정에 쐈다면 매번 창 밖을 노렸을 것이다.
		{"39분 느린 시계", day(22), -39 * time.Minute, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := &Live{run: Run{}}
			err := checkWindow(run, base, c.target, c.offset, true)
			if (err != nil) != c.wantErr {
				t.Errorf("checkWindow(%s, offset %s) = %v, wantErr=%v",
					c.target.Format("01-02"), c.offset, err, c.wantErr)
			}
		})
	}
}

// ★ 계정을 여러 개 두는 이유: 사이트 한도가 계정별이라 계정을 늘리면 그만큼 더 잡을 수 있다.
// 첫 계정이 한도에 차면 같은 자리를 다음 계정으로 다시 노려야 한다.
func TestRotatesToNextAccountOnLimit(t *testing.T) {
	site := newFakeSite()
	site.perAccountLimit = 1 // 계정당 1건만 받는다
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	store, rn := setup(t, srv.URL, nil)
	enc, _ := store.Vault().Encrypt("s3cret")
	store.Update(func(c *config.Config) error {
		c.Site.Accounts = []config.Account{
			{ID: "a1", Label: "계정A", Username: "a@example.com", PasswordEnc: enc, Enabled: true},
			{ID: "a2", Label: "계정B", Username: "b@example.com", PasswordEnc: enc, Enabled: true},
		}
		c.Booking.Count = 2
		c.Booking.Targets = []config.Target{
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "14:00", DurationSlots: 4},
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4},
		}
		return nil
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess || len(run.Bookings) != 2 {
		t.Fatalf("2건을 확보하지 못했다: %s %+v\n%s", run.Status, run.Bookings, dumpLogs(run))
	}
	// 두 건이 서로 다른 계정으로 잡혀야 한다.
	accs := map[string]bool{}
	for _, b := range run.Bookings {
		accs[b.Account] = true
	}
	if len(accs) != 2 {
		t.Errorf("계정이 갈리지 않았다: %+v", run.Bookings)
	}
	if !strings.Contains(dumpLogs(run), "다음 계정으로 넘어갑니다") {
		t.Errorf("계정 전환 로그가 없다\n%s", dumpLogs(run))
	}
}

// 계정이 하나 죽어도 나머지로 계속 간다.
func TestSkipsBrokenAccount(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	store, rn := setup(t, srv.URL, nil)
	good, _ := store.Vault().Encrypt("s3cret")
	bad, _ := store.Vault().Encrypt("nope")
	store.Update(func(c *config.Config) error {
		c.Site.Accounts = []config.Account{
			{ID: "a1", Label: "깨진계정", Username: "a@example.com", PasswordEnc: bad, Enabled: true},
			{ID: "a2", Label: "멀쩡계정", Username: "b@example.com", PasswordEnc: good, Enabled: true},
		}
		return nil
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess || len(run.Bookings) != 1 {
		t.Fatalf("멀쩡한 계정으로 잡았어야 한다: %s %+v\n%s", run.Status, run.Bookings, dumpLogs(run))
	}
	if run.Bookings[0].Account != "멀쩡계정" {
		t.Errorf("계정 = %q", run.Bookings[0].Account)
	}
}

// 꺼 둔 계정은 쓰지 않는다.
func TestIgnoresDisabledAccount(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	store, rn := setup(t, srv.URL, nil)
	enc, _ := store.Vault().Encrypt("s3cret")
	store.Update(func(c *config.Config) error {
		c.Site.Accounts = []config.Account{
			{ID: "a1", Label: "꺼짐", Username: "a@example.com", PasswordEnc: enc, Enabled: false},
			{ID: "a2", Label: "켜짐", Username: "b@example.com", PasswordEnc: enc, Enabled: true},
		}
		return nil
	})

	rn.Start(context.Background(), Options{Mode: "manual"})
	run := waitDone(t, rn)
	if len(run.Bookings) != 1 || run.Bookings[0].Account != "켜짐" {
		t.Fatalf("켜 둔 계정만 써야 한다: %+v\n%s", run.Bookings, dumpLogs(run))
	}
	if strings.Contains(dumpLogs(run), "a@example.com") {
		t.Errorf("꺼 둔 계정으로 로그인했다\n%s", dumpLogs(run))
	}
}

// ★ 지정 예약 항목의 값이 「예약 설정」을 덮어써야 한다 — 비운 필드만 상속된다.
func TestScheduleEntryOverridesBooking(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	store, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Subject = "기본 회의"
		c.Booking.PurposeCd = "A"
		c.Booking.DateOffsetDays = 7
	})
	enc, _ := store.Vault().Encrypt("s3cret")
	store.Update(func(c *config.Config) error {
		c.Site.Accounts = []config.Account{
			{ID: "a1", Label: "계정A", Username: "a@example.com", PasswordEnc: enc, Enabled: true},
			{ID: "a2", Label: "계정B", Username: "b@example.com", PasswordEnc: enc, Enabled: true},
		}
		return nil
	})

	// 실행일은 오늘, 대상 날짜는 +3일(오프셋 7 을 덮어쓴다), 예약자는 두 번째 계정.
	today := time.Now()
	entry := &config.ScheduleEntry{
		ID: "e1", Enabled: true,
		Date:       today.Format("2006-01-02"),
		TargetDate: today.AddDate(0, 0, 3).Format("2006-01-02"),
		Subject:    "이사회",
		PurposeCd:  "C",
		AccountID:  "a2",
	}
	rn.Start(context.Background(), Options{Mode: "manual", Entries: []config.ScheduleEntry{*entry}})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if run.TargetDate != entry.TargetDate {
		t.Errorf("대상 날짜 = %q, want %q (항목이 오프셋을 덮어써야 한다)", run.TargetDate, entry.TargetDate)
	}
	if run.Bookings[0].Account != "계정B" {
		t.Errorf("예약자 = %q, want 계정B", run.Bookings[0].Account)
	}

	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 1 {
		t.Fatalf("신청 %d회", len(site.submits))
	}
	s := site.submits[0]
	if s["title"] != "이사회" {
		t.Errorf("title = %v, want 이사회", s["title"])
	}
	if s["purposeCd"] != "C" {
		t.Errorf("purposeCd = %v, want C", s["purposeCd"])
	}
	if s["reserveDate"] != entry.TargetDate {
		t.Errorf("reserveDate = %v, want %s", s["reserveDate"], entry.TargetDate)
	}
	// 예약자를 못박았으면 다른 계정은 로그인조차 하지 않아야 한다.
	if strings.Contains(dumpLogs(run), "a@example.com") {
		t.Errorf("지정하지 않은 계정으로 로그인했다\n%s", dumpLogs(run))
	}
}

// 비운 필드는 「예약 설정」을 그대로 쓴다 — 날짜만 찍어 둔 항목이 예전처럼 동작해야 한다.
func TestScheduleEntryInheritsBlanks(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Subject = "주간 정기 회의"
		c.Booking.DateOffsetDays = 7
	})

	today := time.Now()
	entry := &config.ScheduleEntry{ID: "e1", Date: today.Format("2006-01-02"), Enabled: true}
	rn.Start(context.Background(), Options{Mode: "manual", Entries: []config.ScheduleEntry{*entry}})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	want := today.AddDate(0, 0, 7).Format("2006-01-02")
	if run.TargetDate != want {
		t.Errorf("대상 날짜 = %q, want %q (오프셋 상속)", run.TargetDate, want)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if site.submits[0]["title"] != "주간 정기 회의" {
		t.Errorf("title = %v, 「예약 설정」을 상속해야 한다", site.submits[0]["title"])
	}
}

// 지정한 예약자 계정이 사라졌으면 다른 사람 이름으로 잡히지 않게 멈춰야 한다.
func TestStopsWhenPinnedAccountMissing(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, nil)

	entry := &config.ScheduleEntry{
		ID: "e1", Date: time.Now().Format("2006-01-02"), Enabled: true, AccountID: "지워진계정",
	}
	rn.Start(context.Background(), Options{Mode: "manual", Entries: []config.ScheduleEntry{*entry}})
	run := waitDone(t, rn)
	if run.Status != StatusFailed {
		t.Fatalf("status = %s — 지정 계정이 없으면 멈춰야 한다\n%s", run.Status, dumpLogs(run))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.submits) != 0 {
		t.Error("지정 계정이 없는데 신청했다")
	}
}

// ★ 같은 실행일에 여러 건 — 항목 하나가 예약 하나다.
func TestMultipleEntriesOnSameDate(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.DateOffsetDays = 7
	})

	today := time.Now().Format("2006-01-02")
	mk := func(id, subj, start string) config.ScheduleEntry {
		return config.ScheduleEntry{
			ID: id, Date: today, Enabled: true, Subject: subj,
			Targets: []config.Target{{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: start, DurationSlots: 4}},
		}
	}
	rn.Start(context.Background(), Options{Mode: "manual",
		Entries: []config.ScheduleEntry{mk("e1", "오전 회의", "09:00"), mk("e2", "오후 회의", "14:00")}})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	if run.Want != 2 || len(run.Bookings) != 2 {
		t.Fatalf("want=%d 확보=%d, 2건이어야 함\n%s", run.Want, len(run.Bookings), dumpLogs(run))
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	titles := map[string]bool{}
	for _, s := range site.submits {
		titles[fmt.Sprint(s["title"])] = true
	}
	// 건마다 회의명이 달라야 한다 — 항목별 설정이 각자 실렸다는 뜻이다.
	if !titles["오전 회의"] || !titles["오후 회의"] {
		t.Errorf("회의명이 건마다 실리지 않았다: %v", titles)
	}
}

// ★ 항목별 회의실 후보 — 앞 후보가 막히면 다음 후보로 같은 '한 건'을 잡는다.
func TestEntryTargetsAreCandidatesForOneBooking(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	// 09:00~11:00 은 남이 선점 → 첫 후보는 실패하고 둘째 후보로 잡혀야 한다.
	for i := slotIdx("09:00"); i < slotIdx("11:00"); i++ {
		site.booked[i] = "남"
	}
	_, rn := setup(t, srv.URL, nil)

	entry := config.ScheduleEntry{
		ID: "e1", Date: time.Now().Format("2006-01-02"), Enabled: true, Subject: "회의",
		Targets: []config.Target{
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4},
			{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "14:00", DurationSlots: 4},
		},
	}
	rn.Start(context.Background(), Options{Mode: "manual", Entries: []config.ScheduleEntry{entry}})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess {
		t.Fatalf("status = %s (%s)\n%s", run.Status, run.Message, dumpLogs(run))
	}
	// 후보가 2개여도 잡히는 건 '한 건'이다.
	if len(run.Bookings) != 1 {
		t.Fatalf("예약 %d건 — 후보 목록은 한 건을 위한 것이다: %+v", len(run.Bookings), run.Bookings)
	}
	if run.Bookings[0].TimeRange != "14:00~16:00" {
		t.Errorf("시간 = %q, want 14:00~16:00 (첫 후보가 막혔으니 둘째)", run.Bookings[0].TimeRange)
	}
}

// ★ 항목별 폴백 — 후보가 다 막히면 같은 건물의 다른 자리를 잡는다.
func TestEntryFallbackFindsAnotherRoom(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	for i := slotIdx("09:00"); i < slotIdx("11:00"); i++ {
		site.booked[i] = "남"
	}
	_, rn := setup(t, srv.URL, nil)

	entry := config.ScheduleEntry{
		ID: "e1", Date: time.Now().Format("2006-01-02"), Enabled: true,
		Targets: []config.Target{{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4}},
		Fallback: &config.Fallback{
			Enabled: true, BldgCd: fakeBldgCd,
			Start: "11:00", StartWindowMinutes: 240, MinSlots: 2, MaxSlots: 4,
		},
	}
	rn.Start(context.Background(), Options{Mode: "manual", Entries: []config.ScheduleEntry{entry}})
	run := waitDone(t, rn)
	if run.Status != StatusSuccess || len(run.Bookings) != 1 {
		t.Fatalf("폴백으로 못 잡았다: %s %+v\n%s", run.Status, run.Bookings, dumpLogs(run))
	}
	if !strings.Contains(dumpLogs(run), "폴백 후보") {
		t.Errorf("폴백이 돌지 않았다\n%s", dumpLogs(run))
	}
}

// 항목이 폴백을 명시적으로 꺼 두면 「예약 설정」의 폴백을 상속하지 않는다.
func TestEntryCanDisableFallback(t *testing.T) {
	site := newFakeSite()
	srv := httptest.NewServer(site.handler())
	defer srv.Close()
	for i := 0; i < fakeSlots; i++ {
		site.booked[i] = "남" // 전부 선점 → 폴백이 돌면 잡을 자리가 없다
	}
	_, rn := setup(t, srv.URL, func(c *config.Config) {
		c.Booking.Fallback = config.Fallback{Enabled: true, Start: "09:00",
			StartWindowMinutes: 480, MinSlots: 2, MaxSlots: 4}
	})
	entry := config.ScheduleEntry{
		ID: "e1", Date: time.Now().Format("2006-01-02"), Enabled: true,
		Targets:  []config.Target{{SpaceCd: fakeSpaceCd, BldgCd: fakeBldgCd, Start: "09:00", DurationSlots: 4}},
		Fallback: &config.Fallback{Enabled: false},
	}
	rn.Start(context.Background(), Options{Mode: "manual", Entries: []config.ScheduleEntry{entry}})
	run := waitDone(t, rn)
	if strings.Contains(dumpLogs(run), "폴백 후보") || strings.Contains(dumpLogs(run), "폴백 조건") {
		t.Errorf("폴백을 껐는데 돌았다\n%s", dumpLogs(run))
	}
}
