package config

import (
	"path/filepath"
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
	ok := Target{RoomID: 41, Building: "3", Floor: "25", Start: "13:00", DurationSlots: 8}
	if err := ValidateTarget(ok, 0); err != nil {
		t.Fatalf("정상 대상인데 거부됨: %v", err)
	}
	bad := []Target{
		{RoomID: 0, Start: "13:00", DurationSlots: 4},  // 회의실 없음
		{RoomID: 41, Start: "13:00", DurationSlots: 9}, // 4시간 초과
		{RoomID: 41, Start: "13:15", DurationSlots: 4}, // 30분 격자 위반
		{RoomID: 41, Start: "08:30", DurationSlots: 4}, // 범위 밖
		{RoomID: 41, Start: "20:00", DurationSlots: 4}, // 21:00 초과
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
		c.Booking.Targets = []Target{{RoomID: 41, Start: "13:00", DurationSlots: 99}}
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
