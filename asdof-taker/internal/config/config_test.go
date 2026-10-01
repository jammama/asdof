package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFireAt(t *testing.T) {
	d, err := ParseFireAt("12:00:00.050")
	if err != nil {
		t.Fatal(err)
	}
	if want := 12*time.Hour + 50*time.Millisecond; d != want {
		t.Errorf("= %v, want %v", d, want)
	}
	if _, err := ParseFireAt("25:00"); err == nil {
		t.Error("잘못된 시각을 통과시켰다")
	}
	if _, err := ParseFireAt("nope"); err == nil {
		t.Error("형식 오류를 통과시켰다")
	}
}

func TestNextFireRespectsWeekdays(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	s := Schedule{FireAt: "12:00:00.050", Weekdays: []int{1, 2, 3, 4, 5}} // 평일만
	// 2026-08-29 는 토요일 13시
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, loc)
	got, err := s.NextFire(now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekday() != time.Monday || got.Day() != 31 {
		t.Errorf("토요일 오후 → 다음은 월요일이어야 함, got %v", got)
	}

	// 같은 날 발사 시각 이전이면 오늘로 잡힌다.
	now = time.Date(2026, 8, 31, 9, 0, 0, 0, loc)
	got, _ = s.NextFire(now, loc)
	if got.Day() != 31 || got.Hour() != 12 {
		t.Errorf("오늘 12시여야 함, got %v", got)
	}
}

func TestValidateTarget(t *testing.T) {
	ok := Target{SpaceCd: "BLDG004_05_001", BldgCd: "BLDG004", Start: "13:00", DurationSlots: 6}
	if err := ValidateTarget(ok, 0); err != nil {
		t.Fatalf("정상 대상인데 거부됨: %v", err)
	}
	bad := []Target{
		{SpaceCd: "", Start: "13:00", DurationSlots: 4},              // 회의실 없음
		{SpaceCd: "S1", Start: "13:00", DurationSlots: MaxSlots + 1}, // 1회 3시간 초과
		{SpaceCd: "S1", Start: "13:15", DurationSlots: 4},            // 30분 격자 위반
		{SpaceCd: "S1", Start: "08:30", DurationSlots: 4},            // 운영 시작 전
		{SpaceCd: "S1", Start: "17:00", DurationSlots: 4},            // 18:00 초과
	}
	for i, b := range bad {
		if err := ValidateTarget(b, i); err == nil {
			t.Errorf("잘못된 대상 #%d 를 통과시켰다: %+v", i, b)
		}
	}
}

func TestNormalizeDatabaseID(t *testing.T) {
	want := "3bb0ae7cbd8d8051bc8afab6a2df9e55"
	for _, in := range []string{
		"3bb0ae7cbd8d8051bc8afab6a2df9e55",
		"3bb0ae7c-bd8d-8051-bc8a-fab6a2df9e55",
		"https://www.notion.so/myteam/3bb0ae7cbd8d8051bc8afab6a2df9e55?v=abc",
	} {
		if got := NormalizeDatabaseID(in); got != want {
			t.Errorf("NormalizeDatabaseID(%q) = %q", in, got)
		}
	}
	if got := NormalizeDatabaseID("짧음"); got != "" {
		t.Errorf("hex 가 없으면 빈 문자열이어야 함, got %q", got)
	}
}

func TestVaultRoundTrip(t *testing.T) {
	t.Setenv("TAKER_SECRET_KEY", "")
	v, err := OpenVault(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := v.Encrypt("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "hunter2" || enc == "" {
		t.Fatal("평문이 그대로 저장됐다")
	}
	got, err := v.Decrypt(enc)
	if err != nil || got != "hunter2" {
		t.Fatalf("복호화 실패: %q %v", got, err)
	}
	if s, _ := v.Encrypt(""); s != "" {
		t.Error("빈 값은 빈 값이어야 한다")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "s3cret") {
		t.Error("올바른 비밀번호를 거부했다")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("틀린 비밀번호를 통과시켰다")
	}
	if VerifyPassword("garbage", "s3cret") {
		t.Error("손상된 해시를 통과시켰다")
	}
}

func TestCookieSigning(t *testing.T) {
	key := []byte("test-key")
	tok := SignCookie(key, "admin|123")
	got, ok := VerifyCookie(key, tok)
	if !ok || got != "admin|123" {
		t.Fatalf("서명 검증 실패: %q %v", got, ok)
	}
	if _, ok := VerifyCookie([]byte("other-key"), tok); ok {
		t.Error("다른 키로 서명한 쿠키를 통과시켰다")
	}
	if _, ok := VerifyCookie(key, "admin|123.deadbeef"); ok {
		t.Error("위조된 서명을 통과시켰다")
	}
}

func TestStoreUpdateRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	v, _ := OpenVault(filepath.Join(dir, "secret.key"))
	s, err := Open(filepath.Join(dir, "config.json"), v)
	if err != nil {
		t.Fatal(err)
	}
	base := s.Get()
	if _, err := s.Update(func(c *Config) error {
		c.Booking.Targets = []Target{{SpaceCd: "S1", Start: "13:00", DurationSlots: 99}}
		return nil
	}); err == nil {
		t.Fatal("잘못된 설정이 저장됐다")
	}
	if len(s.Get().Booking.Targets) != len(base.Booking.Targets) {
		t.Error("검증 실패했는데 설정이 바뀌었다")
	}
}

// 기본 설정이 그대로 저장 가능해야 한다 — 첫 실행에서 아무것도 저장 못 하면 안 된다.
func TestDefaultConfigIsValid(t *testing.T) {
	d := Default()
	if err := d.Validate(); err != nil {
		t.Fatalf("기본 설정이 검증을 통과하지 못했다: %v", err)
	}
}

// 지정 스케줄: 캘린더에서 찍은 항목 중 아직 오지 않은 가장 이른 발사 시각을 골라야 한다.
func TestNextRunFromSchedule(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	ent := func(d string) ScheduleEntry { return ScheduleEntry{ID: "e" + d, Date: d, Enabled: true} }
	s := Schedule{Mode: ModeDates, FireAt: "00:00:01.000",
		Entries: []ScheduleEntry{ent("2026-09-30"), ent("2026-09-20"), ent("2026-09-25")}}

	// 9/20 발사 시각 직전 → 9/20
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, loc)
	got, es, err := s.NextRun(now, loc)
	if err != nil || got.Format("2006-01-02 15:04:05") != "2026-09-20 00:00:01" {
		t.Fatalf("NextRun = %v %v", got, err)
	}
	if len(es) != 1 || es[0].Date != "2026-09-20" {
		t.Fatalf("항목 = %+v, 9/20 한 건이어야 함", es)
	}
	// 직후 → 다음은 9/25 (목록이 정렬돼 있지 않아도 골라야 한다)
	now = time.Date(2026, 9, 20, 0, 0, 2, 0, loc)
	got, es, err = s.NextRun(now, loc)
	if err != nil || len(es) != 1 || es[0].Date != "2026-09-25" {
		t.Fatalf("NextRun = %v %+v %v", got, es, err)
	}
	// 꺼 둔 항목은 건너뛴다.
	off := s
	off.Entries = []ScheduleEntry{{ID: "x", Date: "2026-09-25", Enabled: false}, ent("2026-09-30")}
	_, es, err = off.NextRun(now, loc)
	if err != nil || len(es) != 1 || es[0].Date != "2026-09-30" {
		t.Fatalf("꺼 둔 항목을 골랐다: %+v %v", es, err)
	}
	// 전부 지났으면 이유를 알려줘야 한다 — 조용히 멈추면 안 잡힌 걸 모른다.
	now = time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	if _, _, err := s.NextRun(now, loc); err == nil {
		t.Error("날짜가 모두 지났는데 발사 시각을 돌려줬다")
	}
	// 항목이 하나도 없으면 명확히 거절한다.
	empty := Schedule{Mode: ModeDates, FireAt: "00:00:01.000"}
	if _, _, err := empty.NextRun(now, loc); err == nil {
		t.Error("지정 항목이 없는데 발사 시각을 돌려줬다")
	}
}

// 요일 모드는 항목이 있어도 무시한다 — 모드가 바뀌었는데 옛 날짜로 튀면 안 된다.
func TestWeeklyModeIgnoresEntries(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	s := Schedule{Mode: ModeWeekly, FireAt: "00:00:01.000", Weekdays: []int{1},
		Entries: []ScheduleEntry{{ID: "e1", Date: "2026-09-20", Enabled: true}}} // 9/20 은 일요일
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, loc)
	got, es, err := s.NextRun(now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 0 {
		t.Errorf("요일 모드인데 항목을 돌려줬다: %+v", es)
	}
	if got.Weekday() != time.Monday {
		t.Errorf("NextEntry = %s (%s), 월요일이어야 함", got.Format("2006-01-02"), got.Weekday())
	}
}

// 대상 날짜는 명시값이 우선, 없으면 오프셋으로 계산한다.
func TestEntryTarget(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	auto := ScheduleEntry{Date: "2026-09-16"}
	got, err := auto.Target(7, loc)
	if err != nil || got.Format("2006-01-02") != "2026-09-23" {
		t.Errorf("자동 = %v %v, want 2026-09-23", got, err)
	}
	fixed := ScheduleEntry{Date: "2026-09-16", TargetDate: "2026-09-18"}
	got, err = fixed.Target(7, loc)
	if err != nil || got.Format("2006-01-02") != "2026-09-18" {
		t.Errorf("명시 = %v %v, want 2026-09-18", got, err)
	}
}

// 대상 날짜가 예약 창(실행일 +0~7일) 밖이면 저장 시점에 막아야 한다.
func TestValidateRejectsEntryOutsideWindow(t *testing.T) {
	base := func() Config {
		c := Default()
		c.Site.Accounts = []Account{{ID: "a", Username: "a@x", PasswordEnc: "x", Enabled: true}}
		c.Booking.Targets = []Target{{SpaceCd: "S1", Start: "13:00", DurationSlots: 4}}
		c.Schedule.Enabled = true
		c.Schedule.Mode = ModeDates
		return c
	}
	c := base()
	c.Schedule.Entries = []ScheduleEntry{{ID: "e1", Date: "2026-09-16", TargetDate: "2026-09-30", Enabled: true}}
	if err := c.Validate(); err == nil {
		t.Error("창 밖(+14일) 대상 날짜를 통과시켰다")
	}
	c = base()
	c.Schedule.Entries = []ScheduleEntry{{ID: "e1", Date: "2026-09-16", TargetDate: "2026-09-15", Enabled: true}}
	if err := c.Validate(); err == nil {
		t.Error("실행일보다 이른 대상 날짜를 통과시켰다")
	}
	c = base()
	c.Schedule.Entries = []ScheduleEntry{{ID: "e1", Date: "2026-09-16", TargetDate: "2026-09-23", Enabled: true}}
	if err := c.Validate(); err != nil {
		t.Errorf("창 안(+7일)은 통과해야 한다: %v", err)
	}
	// 없는 계정을 예약자로 지목하면 막는다.
	c = base()
	c.Schedule.Entries = []ScheduleEntry{{ID: "e1", Date: "2026-09-16", AccountID: "없음", Enabled: true}}
	if err := c.Validate(); err == nil {
		t.Error("없는 계정을 예약자로 받았다")
	}
}

// 계정이 늘면 하루에 잡을 수 있는 건수 상한도 올라간다 (사이트 한도가 계정별이라서).
func TestMaxCountScalesWithAccounts(t *testing.T) {
	c := Default()
	if got := c.MaxCount(); got != MaxBookingsPerDay {
		t.Errorf("계정 0개: MaxCount = %d, want %d", got, MaxBookingsPerDay)
	}
	c.Site.Accounts = []Account{
		{ID: "a", Username: "a@x", PasswordEnc: "x", Enabled: true},
		{ID: "b", Username: "b@x", PasswordEnc: "x", Enabled: true},
		{ID: "c", Username: "c@x", PasswordEnc: "x", Enabled: false}, // 꺼진 건 안 센다
	}
	if got := c.MaxCount(); got != 2*MaxBookingsPerDay {
		t.Errorf("계정 2개: MaxCount = %d, want %d", got, 2*MaxBookingsPerDay)
	}
}

// 지정 스케줄을 켜 두고 날짜를 안 고르면 영영 발사하지 않는다 — 저장 시점에 막아야 한다.
func TestValidateRejectsEmptyDateSchedule(t *testing.T) {
	c := Default()
	c.Site.Accounts = []Account{{ID: "a", Username: "a@x", PasswordEnc: "x", Enabled: true}}
	c.Booking.Targets = []Target{{SpaceCd: "S1", Start: "13:00", DurationSlots: 4}}
	c.Schedule.Enabled = true
	c.Schedule.Mode = ModeDates
	if err := c.Validate(); err == nil {
		t.Error("날짜 없는 지정 스케줄을 통과시켰다")
	}
	c.Schedule.Entries = []ScheduleEntry{{ID: "e1", Date: "2026-09-21", Enabled: true}}
	if err := c.Validate(); err != nil {
		t.Errorf("날짜를 고르면 통과해야 한다: %v", err)
	}
}

// ★ 요일을 전부 끄고 저장하면 화면이 터졌다.
// clone 의 append([]int(nil), …) 이 빈 슬라이스를 nil 로 만들어 JSON 에 null 로 나갔고,
// 화면이 weekdays.length 에서 죽었다. 내려가는 값은 언제나 배열이어야 한다.
func TestEmptySlicesStayArraysInJSON(t *testing.T) {
	c := Default()
	c.Schedule.Weekdays = []int{}
	c.Booking.Targets = []Target{}
	c.Schedule.Entries = []ScheduleEntry{}
	c.Site.Accounts = []Account{}

	b, err := json.Marshal(clone(c))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"weekdays":null`, `"targets":null`, `"entries":null`, `"accounts":null`} {
		if strings.Contains(string(b), key) {
			t.Errorf("%s 로 나갔다 — 화면이 .length 에서 터진다\n%s", key, b)
		}
	}
	for _, key := range []string{`"weekdays":[]`, `"targets":[]`, `"entries":[]`, `"accounts":[]`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("%s 가 없다\n%s", key, b)
		}
	}
}

// clone 은 모든 슬라이스를 복사해야 한다 — 러너가 설정을 들고 있는 동안
// 웹에서 Update 가 같은 배열을 건드리면 값이 발밑에서 바뀐다.
func TestCloneDoesNotAliasSlices(t *testing.T) {
	c := Default()
	c.Site.Accounts = []Account{{ID: "a", Username: "a@x", Enabled: true}}
	c.Schedule.Entries = []ScheduleEntry{{ID: "e", Date: "2026-09-21", Enabled: true}}
	c.Booking.Targets = []Target{{SpaceCd: "S1", Start: "13:00", DurationSlots: 4}}

	cp := clone(c)
	cp.Site.Accounts[0].Username = "바뀜"
	cp.Schedule.Entries[0].Subject = "바뀜"
	cp.Booking.Targets[0].SpaceCd = "바뀜"

	if c.Site.Accounts[0].Username != "a@x" {
		t.Error("계정 배열이 원본과 같은 메모리를 본다")
	}
	if c.Schedule.Entries[0].Subject != "" {
		t.Error("지정 예약 배열이 원본과 같은 메모리를 본다")
	}
	if c.Booking.Targets[0].SpaceCd != "S1" {
		t.Error("지정 대상 배열이 원본과 같은 메모리를 본다")
	}
}

// 요일을 하나도 안 고르면 '매일'이 아니라 '실행 안 함'이다.
// 요일 반복을 그만두려고 전부 끈 사람이 오히려 매일 돌게 되던 함정을 막는다.
func TestEmptyWeekdaysMeansNeverRun(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	s := Schedule{Mode: ModeWeekly, FireAt: "00:00:01.000", Weekdays: []int{}}
	if s.AllowsWeekday(1) {
		t.Error("빈 요일 목록이 월요일을 허용했다")
	}
	_, err := s.NextFire(time.Date(2026, 9, 15, 12, 0, 0, 0, loc), loc)
	if err == nil {
		t.Fatal("요일이 없는데 발사 시각을 돌려줬다")
	}
	if !strings.Contains(err.Error(), "요일") {
		t.Errorf("이유가 불분명하다: %v", err)
	}
	// 7개를 다 고르면 매일이다.
	s.Weekdays = []int{0, 1, 2, 3, 4, 5, 6}
	if _, err := s.NextFire(time.Date(2026, 9, 15, 12, 0, 0, 0, loc), loc); err != nil {
		t.Errorf("7개를 다 골랐는데 실패: %v", err)
	}
}

// 요일을 다 꺼도 저장은 되어야 한다 — 그래야 요일 반복을 그만둘 수 있다.
func TestValidateAllowsEmptyWeekdays(t *testing.T) {
	c := Default()
	c.Site.Accounts = []Account{{ID: "a", Username: "a@x", PasswordEnc: "x", Enabled: true}}
	c.Booking.Targets = []Target{{SpaceCd: "S1", Start: "13:00", DurationSlots: 4}}
	c.Schedule.Enabled = true
	c.Schedule.Weekdays = []int{}
	if err := c.Validate(); err != nil {
		t.Errorf("요일을 다 꺼도 저장은 되어야 한다: %v", err)
	}
}
