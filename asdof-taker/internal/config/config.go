// Package config 는 asdof-taker 의 설정을 정의하고, 디스크(JSON)에 원자적으로
// 읽고 쓴다. 비밀번호/토큰 같은 비밀값은 AES-256-GCM 으로 암호화해서 보관하며
// (§8 "비밀정보 취급"), 웹 UI 로는 절대 평문을 내보내지 않는다.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── 설정 타입 ──────────────────────────────────────────────────────────────

// Target 은 "이 공간을 이 시각부터 이만큼" 으로 완전히 확정된 예약 대상이다.
// 확정돼 있으므로 발사 시점에 조회 없이 곧바로 신청할 수 있다.
//
// 2026-09 사이트 개편으로 회의실 지정 방식이 바뀌었다: 빌딩·층·room_id 세 값 대신
// 공간 코드 하나(spaceCd)면 된다. bldgCd 는 신청 페이로드에 함께 실린다.
type Target struct {
	SpaceCd       string `json:"space_cd"` // "BLDG004_05_001"
	BldgCd        string `json:"bldg_cd"`  // "BLDG004"
	Label         string `json:"label"`    // 화면 표시용 캐시 ("현승빌딩(S3) 5층 회의실A")
	Start         string `json:"start"`    // "HH:MM"
	DurationSlots int    `json:"duration_slots"`
}

// Fallback 은 지정 대상이 모두 실패했을 때 조회 결과에서 자리를 찾는 규칙이다.
type Fallback struct {
	Enabled            bool   `json:"enabled"`
	BldgCd             string `json:"bldg_cd"`      // "" = 전체
	PreferFloor        string `json:"prefer_floor"` // "" = 층 무관. 공간의 floorNo 와 비교
	Start              string `json:"start"`
	StartWindowMinutes int    `json:"start_window_minutes"`
	MinSlots           int    `json:"min_slots"`
	MaxSlots           int    `json:"max_slots"`
}

type Booking struct {
	DateOffsetDays int `json:"date_offset_days"`
	// GapSeconds 는 예약 한 건을 잡은 뒤 다음 건을 신청하기까지 두는 간격이다.
	// 예전 그누보드의 도배 방지(cf_delay_sec)는 사라졌지만, 같은 계정으로 연달아
	// 쏘는 것을 굳이 서두를 이유가 없어 짧게 남겨 둔다.
	GapSeconds int `json:"gap_seconds"`
	// Count 는 하루에 확보할 예약 '건수'다. targets 를 위에서부터 시도해 이 개수를
	// 채우면 멈춘다. 사이트 정책상 계정당 하루 2건이 상한이다.
	Count   int    `json:"count"`
	Subject string `json:"subject"` // 신청의 title
	Manager string `json:"manager"` // Notion 기록용. 신청 페이로드에는 들어가지 않는다
	// PurposeCd 는 이용목적 공통코드(USE_PURPOSE)다. "A"=업무 회의.
	PurposeCd  string   `json:"purpose_cd"`
	PurposeEtc string   `json:"purpose_etc"` // purposeCd 가 '기타' 일 때만
	Targets    []Target `json:"targets"`
	Fallback   Fallback `json:"fallback"`
}

type Retry struct {
	IntervalMS   int `json:"interval_ms"`
	MaxDurationS int `json:"max_duration_s"`
	MaxAttempts  int `json:"max_attempts"`
}

// 스케줄 모드.
const (
	// ModeWeekly 는 고른 요일마다 반복 실행한다(기존 동작).
	ModeWeekly = "weekly"
	// ModeDates 는 캘린더에서 찍은 날짜에만 실행한다(지정 스케줄 예약).
	ModeDates = "dates"
)

type Schedule struct {
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`    // weekly | dates. 비어 있으면 weekly
	FireAt  string `json:"fire_at"` // "00:00:01.000"
	// Weekdays 는 ModeWeekly 에서만 쓴다. 0=일 … 6=토. **비어 있으면 실행하지 않는다**.
	Weekdays []int `json:"weekdays"`
	// Entries 는 ModeDates 에서 실행할 예약들이다. 실행일 오름차순으로 정규화한다.
	Entries     []ScheduleEntry `json:"entries"`
	WarmupLeadS int             `json:"warmup_lead_s"`
	Retry       Retry           `json:"retry"`

	// LegacyDates 는 항목별 설정이 생기기 전의 날짜 목록이다. Migrate 가 Entries 로 옮긴다.
	LegacyDates []string `json:"dates,omitempty"`
}

// ScheduleEntry 는 지정 스케줄의 예약 **한 건**이다.
//
// 한 건 = 회의실 하나를 잡는 것. Targets 는 그 하나를 잡기 위한 **후보 목록**이고
// 위에서부터 시도해 하나가 잡히면 그 건은 끝난다(여러 개를 잡는 게 아니다).
// 후보가 다 실패하면 Fallback 이 같은 건물에서 빈 자리를 찾는다.
//
// 같은 실행일에 여러 건을 넣을 수 있다 — 그날 예약 두 개가 필요하면 항목을 두 개 만든다.
//
// 비어 있는 필드는 「예약 설정」의 값을 그대로 쓴다 — 날짜마다 다른 것만 채우면 된다.
type ScheduleEntry struct {
	ID   string `json:"id"`   // 화면이 항목을 지목할 불변 키
	Date string `json:"date"` // 실행일 "2026-09-21"
	// TargetDate 는 실제로 잡을 날짜다. 비우면 Date + booking.date_offset_days.
	// 사이트 예약 창이 오늘~+7일이라 Date 와의 차이는 0~7일이어야 한다.
	TargetDate string `json:"target_date"`
	Subject    string `json:"subject"`    // 회의명. 비우면 booking.subject
	Manager    string `json:"manager"`    // 담당자명. 비우면 booking.manager
	PurposeCd  string `json:"purpose_cd"` // 이용목적. 비우면 booking.purpose_cd
	// AccountID 는 이 건을 맡을 계정이다. 비우면 등록된 계정을 앞에서부터 쓴다.
	AccountID string `json:"account_id"`
	// Targets 는 이 한 건을 잡기 위한 회의실·시간 후보다. 비우면 booking.targets.
	Targets []Target `json:"targets"`
	// Fallback 은 후보가 다 실패했을 때의 탐색 규칙이다.
	// nil 이면 booking.fallback 을 그대로 쓴다(끄려면 enabled=false 로 넣는다).
	Fallback *Fallback `json:"fallback"`
	Enabled  bool      `json:"enabled"`
}

// EffectiveTargets 는 이 건이 실제로 시도할 후보다.
func (e ScheduleEntry) EffectiveTargets(b Booking) []Target {
	if len(e.Targets) > 0 {
		return e.Targets
	}
	return b.Targets
}

// EffectiveFallback 은 이 건에 적용할 폴백 규칙이다.
func (e ScheduleEntry) EffectiveFallback(b Booking) Fallback {
	if e.Fallback != nil {
		return *e.Fallback
	}
	return b.Fallback
}

// Target 은 이 항목이 실제로 잡을 날짜다. 명시값이 없으면 오프셋으로 계산한다.
func (e ScheduleEntry) Target(offsetDays int, loc *time.Location) (time.Time, error) {
	if d := strings.TrimSpace(e.TargetDate); d != "" {
		return time.ParseInLocation("2006-01-02", d, loc)
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(e.Date), loc)
	if err != nil {
		return time.Time{}, err
	}
	return day.AddDate(0, 0, offsetDays), nil
}

// UsesDates 는 지정 스케줄 모드인지다.
func (s Schedule) UsesDates() bool { return s.Mode == ModeDates }

// Account 는 로그인 계정 하나다.
//
// 사이트 한도가 **계정별**(1일 2회·5시간)이라, 계정을 여러 개 두면 그만큼 더 잡을 수 있다.
// 순서가 곧 우선순위다 — 앞 계정을 한도까지 쓰고 다음으로 넘어간다.
type Account struct {
	ID          string `json:"id"`    // 화면이 계정을 지목할 때 쓰는 불변 키
	Label       string `json:"label"` // 사람이 알아보는 이름 (비우면 아이디로 표시)
	Username    string `json:"username"`
	PasswordEnc string `json:"password_enc"` // 암호문. 평문은 절대 저장/전송하지 않는다
	Enabled     bool   `json:"enabled"`
}

// Name 은 화면·로그에 쓸 이름이다.
func (a Account) Name() string {
	if strings.TrimSpace(a.Label) != "" {
		return a.Label
	}
	return a.Username
}

// Ready 는 이 계정으로 실제 로그인할 수 있는지다.
func (a Account) Ready() bool {
	return a.Enabled && strings.TrimSpace(a.Username) != "" && a.PasswordEnc != ""
}

type Site struct {
	BaseURL string `json:"base_url"`
	// Accounts 는 로그인 계정 목록이다. 개편 전에는 단일 계정(username/password_enc)이었고,
	// Migrate 가 그 값을 accounts[0] 으로 옮긴다.
	Accounts  []Account `json:"accounts"`
	UserAgent string    `json:"user_agent"`
	TimeoutS  int       `json:"timeout_s"`

	// 아래 둘은 옛 단일 계정 필드다. 읽기 전용(마이그레이션 입력)으로만 남겨 둔다.
	LegacyUsername    string `json:"username,omitempty"`
	LegacyPasswordEnc string `json:"password_enc,omitempty"`
}

// ReadyAccounts 는 실제로 쓸 수 있는 계정들을 순서대로 돌려준다.
func (s Site) ReadyAccounts() []Account {
	out := make([]Account, 0, len(s.Accounts))
	for _, a := range s.Accounts {
		if a.Ready() {
			out = append(out, a)
		}
	}
	return out
}

// Account 는 id 로 계정을 찾는다.
func (s Site) Account(id string) (Account, bool) {
	for _, a := range s.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

type Notion struct {
	Enabled       bool   `json:"enabled"`
	TokenEnc      string `json:"token_enc"`
	DatabaseID    string `json:"database_id"`
	RecordFailure bool   `json:"record_failure"`
}

type Runtime struct {
	DryRun   bool   `json:"dry_run"`
	Timezone string `json:"timezone"`
	LogLevel string `json:"log_level"`
}

type Admin struct {
	PasswordHash string `json:"password_hash"` // pbkdf2$iter$salt$hash (§web)
}

type Config struct {
	Site     Site     `json:"site"`
	Schedule Schedule `json:"schedule"`
	Booking  Booking  `json:"booking"`
	Notion   Notion   `json:"notion"`
	Runtime  Runtime  `json:"runtime"`
	Admin    Admin    `json:"admin"`
}

// Default 는 문서 §8 의 예시값을 기준으로 한 초기 설정이다.
// 비밀값과 사람 이름/연락처는 비워 두고 웹 UI 에서 채우게 한다.
func Default() Config {
	return Config{
		Site: Site{
			BaseURL:   CanonicalBaseURL,
			Accounts:  []Account{},
			UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
			TimeoutS:  10,
		},
		Schedule: Schedule{
			Enabled: false, // 자격증명이 채워지기 전에는 켜지 않는다
			// 예약 창(오늘~+7일)은 자정에 롤링되고, 그 자정은 **jointips 서버의 시계** 기준이다.
			// 개편 전 서버는 40분 가까이 느렸지만 새 서버는 거의 정확하다(실측 ±0.1초).
			// 그래서 기본값을 자정 직후로 되돌렸다. 화면 하단 각주가 실측값을 계속 보여준다.
			FireAt:      "00:00:01.000",
			Mode:        ModeWeekly,
			Weekdays:    []int{1, 2, 3, 4, 5},
			WarmupLeadS: 180,
			Retry:       Retry{IntervalMS: 250, MaxDurationS: 15, MaxAttempts: 40},
		},
		Booking: Booking{
			DateOffsetDays: 7,
			Count:          1,
			GapSeconds:     2,
			Subject:        "주간 정기 회의",
			PurposeCd:      DefaultPurposeCd,
			Targets:        []Target{},
			Fallback: Fallback{
				Enabled:            true,
				Start:              "13:00",
				StartWindowMinutes: 60,
				MinSlots:           2,
				MaxSlots:           MaxSlots,
			},
		},
		Notion:  Notion{RecordFailure: false},
		Runtime: Runtime{DryRun: true, Timezone: "Asia/Seoul", LogLevel: "info"},
	}
}

// ── 파생 값 ────────────────────────────────────────────────────────────────

func (s Schedule) WarmupLead() time.Duration { return time.Duration(s.WarmupLeadS) * time.Second }
func (r Retry) Interval() time.Duration      { return time.Duration(r.IntervalMS) * time.Millisecond }
func (r Retry) MaxDuration() time.Duration   { return time.Duration(r.MaxDurationS) * time.Second }
func (s Site) Timeout() time.Duration        { return time.Duration(s.TimeoutS) * time.Second }

// Location 은 설정된 타임존을 돌려준다. 잘못됐으면 Asia/Seoul 로 떨어진다.
func (r Runtime) Location() *time.Location {
	if loc, err := time.LoadLocation(r.Timezone); err == nil {
		return loc
	}
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.FixedZone("KST", 9*3600)
	}
	return loc
}

// ParseFireAt 은 "00:40:01.000" 같은 발사 시각을 자정으로부터의 오프셋으로 바꾼다.
func ParseFireAt(s string) (time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("발사 시각 형식이 잘못됨: %q (예: 00:40:01.000)", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, fmt.Errorf("발사 시각의 시(hour)가 잘못됨: %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("발사 시각의 분(minute)이 잘못됨: %q", s)
	}
	var secs float64
	if len(parts) == 3 {
		secs, err = strconv.ParseFloat(parts[2], 64)
		if err != nil || secs < 0 || secs >= 60 {
			return 0, fmt.Errorf("발사 시각의 초(second)가 잘못됨: %q", s)
		}
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(secs*float64(time.Second))
	return d, nil
}

// NextFire 는 now 이후의 가장 이른 발사 시각을 계산한다.
// 요일 반복 모드는 요일 필터를, 지정 스케줄 모드는 찍어 둔 날짜 목록을 본다.
func (s Schedule) NextFire(now time.Time, loc *time.Location) (time.Time, error) {
	off, err := ParseFireAt(s.FireAt)
	if err != nil {
		return time.Time{}, err
	}
	now = now.In(loc)
	if s.UsesDates() {
		t, _, err := s.nextEntry(now, loc, off)
		return t, err
	}
	// 요일을 하나도 안 골랐으면 돌 일이 없다. 14일을 훑고 나서 얼버무리지 말고 바로 알려준다.
	if len(s.Weekdays) == 0 {
		return time.Time{}, fmt.Errorf("고른 실행 요일이 없습니다 — 요일을 고르거나 지정 스케줄을 쓰세요")
	}
	for i := 0; i < 14; i++ {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, i)
		t := day.Add(off)
		if !t.After(now) {
			continue
		}
		if s.AllowsWeekday(int(t.Weekday())) {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("앞으로 14일 안에 실행 요일이 없음")
}

// NextRun 은 다음 발사 시각과 그때 실행할 **모든** 항목을 돌려준다.
//
// 같은 실행일에 여러 건을 넣을 수 있으므로 한 번 발사에 여러 건이 딸려 온다.
// 요일 반복 모드에서는 목록이 비어 있다(「예약 설정」의 값을 그대로 쓴다).
func (s Schedule) NextRun(now time.Time, loc *time.Location) (time.Time, []ScheduleEntry, error) {
	off, err := ParseFireAt(s.FireAt)
	if err != nil {
		return time.Time{}, nil, err
	}
	if !s.UsesDates() {
		t, err := s.NextFire(now, loc)
		return t, nil, err
	}
	t, first, err := s.nextEntry(now.In(loc), loc, off)
	if err != nil {
		return time.Time{}, nil, err
	}
	return t, s.EntriesOn(first.Date), nil
}

// nextEntry 는 아직 오지 않은 가장 이른 항목을 찾는다.
// Entries 가 정렬돼 있다고 가정하지 않는다 — 손으로 고친 설정 파일도 받아야 한다.
func (s Schedule) nextEntry(now time.Time, loc *time.Location, off time.Duration) (time.Time, *ScheduleEntry, error) {
	live := 0
	var best time.Time
	var bestEntry ScheduleEntry
	for _, e := range s.Entries {
		if !e.Enabled {
			continue
		}
		live++
		day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(e.Date), loc)
		if err != nil {
			continue
		}
		t := day.Add(off)
		if !t.After(now) {
			continue
		}
		if best.IsZero() || t.Before(best) {
			best, bestEntry = t, e
		}
	}
	switch {
	case live == 0:
		return time.Time{}, nil, fmt.Errorf("지정한 실행 날짜가 없습니다 — 캘린더에서 날짜를 고르세요")
	case best.IsZero():
		return time.Time{}, nil, fmt.Errorf("지정한 실행 날짜가 모두 지났습니다 — 캘린더에서 새로 고르세요")
	}
	return best, &bestEntry, nil
}

// EntriesOn 은 그 실행일에 켜져 있는 항목 전부다(설정 순서를 지킨다).
func (s Schedule) EntriesOn(date string) []ScheduleEntry {
	var out []ScheduleEntry
	for _, e := range s.Entries {
		if e.Enabled && e.Date == date {
			out = append(out, e)
		}
	}
	return out
}

// LiveEntries 는 켜져 있는 항목들이다.
func (s Schedule) LiveEntries() []ScheduleEntry {
	out := make([]ScheduleEntry, 0, len(s.Entries))
	for _, e := range s.Entries {
		if e.Enabled {
			out = append(out, e)
		}
	}
	return out
}

// SortEntries 는 실행일 오름차순으로 세운다. YYYY-MM-DD 는 사전순 = 시간순이다.
func (s *Schedule) SortEntries() {
	sort.SliceStable(s.Entries, func(i, j int) bool { return s.Entries[i].Date < s.Entries[j].Date })
}

// NewEntryID 는 지정 예약 항목의 불변 키를 만든다.
func NewEntryID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("ent-%d", time.Now().UnixNano())
	}
	return "ent-" + hex.EncodeToString(b)
}

// AllowsWeekday 는 그 요일에 실행할지 여부다.
//
// 빈 목록은 **실행 안 함**이다. 예전에는 '매일'로 봤는데, 요일 반복을 그만두려고
// 전부 끈 사람이 오히려 매일 돌게 되는 함정이었다. 매일 돌리려면 7개를 다 고르면 된다.
func (s Schedule) AllowsWeekday(wd int) bool {
	if len(s.Weekdays) == 0 {
		return false
	}
	for _, d := range s.Weekdays {
		if d == wd {
			return true
		}
	}
	return false
}

// ── 검증 (§5.0) ────────────────────────────────────────────────────────────

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

// ParseHHMM 은 "HH:MM" 을 자정으로부터의 분으로 바꾼다.
func ParseHHMM(s string) (int, error) {
	if !hhmm.MatchString(s) {
		return 0, fmt.Errorf("시각 형식이 잘못됨: %q (HH:MM)", s)
	}
	h, _ := strconv.Atoi(s[0:2])
	m, _ := strconv.Atoi(s[3:5])
	return h*60 + m, nil
}

// FormatHHMM 은 분을 "HH:MM" 으로 되돌린다.
func FormatHHMM(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

const (
	// 격자 범위. 공간마다 openTime/closeTime 이 있지만(현재는 모두 09:00~18:00),
	// 화면 기본값과 입력 검증은 이 값을 쓴다. 실제 가능 여부는 언제나 슬롯 조회가 결정한다.
	GridStartMin = 9 * 60     // 09:00
	GridEndMin   = 17*60 + 30 // 17:30 (마지막 슬롯의 시작)
	GridLastEnd  = 18 * 60    // 18:00 (예약 가능한 최대 종료시각)
	SlotMinutes  = 30
	GridSlots    = 18 // 09:00~17:30, 30분 단위

	// 사이트 유의사항(2026-09 개편):
	//   · 계정별 1회 최대 3시간  → MaxSlots
	//   · 계정별 1일 최대 2회·5시간 → MaxBookingsPerDay / MaxDailySlots
	// 슬롯 조회 응답의 singleLimitHour/dailyLimitHour 가 진짜 값이고, 여기 상수는
	// 저장 시점에 미리 걸러 주기 위한 사본이다.
	MaxSlots          = 6  // 3시간
	MaxDailySlots     = 10 // 5시간
	MaxBookingsPerDay = 2

	// CanonicalBaseURL — www 를 붙이면 301 로 튕기고, 301 은 POST 를 GET 으로 바꿔
	// 로그인이 조용히 실패한다. 반드시 www 없는 주소를 쓴다.
	CanonicalBaseURL = "https://jointips.or.kr"

	// DefaultPurposeCd 는 USE_PURPOSE 공통코드의 "업무 회의" 다.
	DefaultPurposeCd = "A"

	// BookingWindowDays 는 사이트가 여는 예약 창의 길이다: 오늘 ~ 오늘+7일(양끝 포함).
	BookingWindowDays = 7

	// MaxAccounts 는 화면·설정이 다루는 계정 수 상한이다. 기술적 제약이 아니라
	// 실수로 수십 개를 넣어 사이트를 두드리는 일을 막기 위한 안전선이다.
	MaxAccounts = 8
)

// MaxCount 는 지금 설정으로 하루에 잡을 수 있는 예약 건수 상한이다.
// 사이트 한도가 계정별이라 계정을 늘리면 그만큼 올라간다.
func (c *Config) MaxCount() int {
	n := len(c.Site.ReadyAccounts())
	if n < 1 {
		n = 1
	}
	return n * MaxBookingsPerDay
}

// ValidateTarget 은 문서 §5.0 의 1~3번 규칙을 검사한다.
func ValidateTarget(t Target, idx int) error {
	label := fmt.Sprintf("대상 #%d", idx+1)
	if strings.TrimSpace(t.SpaceCd) == "" {
		return fmt.Errorf("%s: 회의실을 선택하세요", label)
	}
	if t.DurationSlots < 1 || t.DurationSlots > MaxSlots {
		return fmt.Errorf("%s: 예약 길이는 1~%d슬롯(최대 %.0f시간)이어야 합니다 (현재 %d)",
			label, MaxSlots, float64(MaxSlots)/2, t.DurationSlots)
	}
	start, err := ParseHHMM(t.Start)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if start%SlotMinutes != 0 {
		return fmt.Errorf("%s: 시작 시각은 :00 또는 :30 이어야 합니다 (현재 %s)", label, t.Start)
	}
	if start < GridStartMin || start > GridEndMin {
		return fmt.Errorf("%s: 시작 시각은 %s~%s 범위여야 합니다 (현재 %s)",
			label, FormatHHMM(GridStartMin), FormatHHMM(GridEndMin), t.Start)
	}
	if start+t.DurationSlots*SlotMinutes > GridLastEnd {
		return fmt.Errorf("%s: 종료 시각이 %s 을 넘습니다 (%s + %d슬롯)",
			label, FormatHHMM(GridLastEnd), t.Start, t.DurationSlots)
	}
	return nil
}

// Validate 는 저장 직전과 실행 직전에 모두 호출된다.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Site.BaseURL) == "" {
		return fmt.Errorf("사이트 주소(base_url)가 비어 있습니다")
	}
	if !strings.HasPrefix(c.Site.BaseURL, "http://") && !strings.HasPrefix(c.Site.BaseURL, "https://") {
		return fmt.Errorf("사이트 주소는 http:// 또는 https:// 로 시작해야 합니다")
	}
	if c.Site.TimeoutS < 1 || c.Site.TimeoutS > 120 {
		return fmt.Errorf("HTTP 타임아웃은 1~120초여야 합니다")
	}
	if _, err := ParseFireAt(c.Schedule.FireAt); err != nil {
		return err
	}
	for _, d := range c.Schedule.Weekdays {
		if d < 0 || d > 6 {
			return fmt.Errorf("요일 값이 잘못됨: %d", d)
		}
	}
	switch c.Schedule.Mode {
	case "", ModeWeekly, ModeDates:
	default:
		return fmt.Errorf("스케줄 모드가 잘못됨: %q", c.Schedule.Mode)
	}
	loc := c.Runtime.Location()
	// 같은 실행일에 여러 건을 넣는 건 정상이다(그날 예약 두 개). 대신 같은 건물·시각을
	// 두 건이 함께 노리면 뒤엣것이 반드시 실패하므로 그것만 막는다.
	seenSlot := map[string]bool{}
	for i, e := range c.Schedule.Entries {
		label := fmt.Sprintf("지정 예약 #%d", i+1)
		if strings.TrimSpace(e.ID) == "" {
			return fmt.Errorf("%s 에 id 가 없습니다", label)
		}
		day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(e.Date), loc)
		if err != nil {
			return fmt.Errorf("%s: 실행일 형식이 잘못됐습니다: %q (YYYY-MM-DD)", label, e.Date)
		}
		for j, t := range e.Targets {
			if err := ValidateTarget(t, j); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
		if fb := e.Fallback; fb != nil && fb.Enabled {
			if err := validateFallback(*fb); err != nil {
				return fmt.Errorf("%s 폴백: %w", label, err)
			}
		}
		// 대상 날짜는 실행일로부터 0~7일 뒤여야 한다. 사이트가 그만큼만 열기 때문에
		// 그 밖이면 발사해 봐야 전부 거절이다.
		target, err := e.Target(c.Booking.DateOffsetDays, loc)
		if err != nil {
			return fmt.Errorf("%s: 대상 날짜 형식이 잘못됐습니다: %q (YYYY-MM-DD)", label, e.TargetDate)
		}
		if gap := int(target.Sub(day).Hours() / 24); gap < 0 || gap > BookingWindowDays {
			return fmt.Errorf("%s: 대상 날짜(%s)가 실행일(%s) 기준 %d일 뒤입니다 — 사이트는 0~%d일만 엽니다",
				label, target.Format("2006-01-02"), e.Date, gap, BookingWindowDays)
		}
		if e.AccountID != "" {
			if _, ok := c.Site.Account(e.AccountID); !ok {
				return fmt.Errorf("%s: 지정한 예약자 계정이 없습니다", label)
			}
		}
		if !e.Enabled {
			continue
		}
		// 같은 날·같은 공간·같은 시각을 두 건이 노리면 뒤엣것은 반드시 선점 실패다.
		for _, t := range e.Targets {
			k := target.Format("2006-01-02") + "|" + t.SpaceCd + "|" + t.Start
			if seenSlot[k] {
				return fmt.Errorf("%s: %s %s 를 다른 건과 똑같이 노리고 있습니다 — 한쪽을 바꾸세요",
					label, t.SpaceCd, t.Start)
			}
			seenSlot[k] = true
		}
	}
	// 지정 스케줄을 켜 두고 쓸 항목이 없으면 영영 발사하지 않는다 — 저장 시점에 막는다.
	if c.Schedule.Enabled && c.Schedule.UsesDates() && len(c.Schedule.LiveEntries()) == 0 {
		return fmt.Errorf("지정 스케줄이 켜져 있지만 쓸 수 있는 예약이 없습니다 — 캘린더에서 날짜를 고르세요")
	}
	ids, users := map[string]bool{}, map[string]bool{}
	for i, a := range c.Site.Accounts {
		if strings.TrimSpace(a.ID) == "" {
			return fmt.Errorf("계정 #%d 에 id 가 없습니다", i+1)
		}
		if ids[a.ID] {
			return fmt.Errorf("계정 id 가 중복됩니다: %s", a.ID)
		}
		ids[a.ID] = true
		u := strings.ToLower(strings.TrimSpace(a.Username))
		if a.Enabled && u == "" {
			return fmt.Errorf("계정 #%d: 아이디를 입력하세요", i+1)
		}
		// 같은 아이디를 두 번 넣으면 좌석이 둘로 보이지만 한도는 하나다 —
		// 잡을 수 있는 건수를 부풀려 계산하게 되므로 저장 시점에 막는다.
		if u != "" && users[u] {
			return fmt.Errorf("같은 아이디가 두 번 들어 있습니다: %s (한도는 계정별이라 중복은 의미가 없습니다)", a.Username)
		}
		users[u] = true
	}
	if len(c.Site.Accounts) > MaxAccounts {
		return fmt.Errorf("계정은 최대 %d개까지입니다", MaxAccounts)
	}
	if c.Schedule.WarmupLeadS < 30 || c.Schedule.WarmupLeadS > 3600 {
		return fmt.Errorf("워밍업 선행 시간은 30~3600초여야 합니다")
	}
	if c.Schedule.Retry.IntervalMS < 50 || c.Schedule.Retry.IntervalMS > 5000 {
		return fmt.Errorf("재시도 간격은 50~5000ms 여야 합니다")
	}
	if c.Schedule.Retry.MaxDurationS < 1 || c.Schedule.Retry.MaxDurationS > 120 {
		return fmt.Errorf("재시도 총 시간은 1~120초여야 합니다")
	}
	if c.Schedule.Retry.MaxAttempts < 1 || c.Schedule.Retry.MaxAttempts > 200 {
		return fmt.Errorf("재시도 최대 횟수는 1~200 이어야 합니다")
	}
	if c.Booking.DateOffsetDays < 0 || c.Booking.DateOffsetDays > 7 {
		return fmt.Errorf("대상 날짜 오프셋은 0~7일이어야 합니다 (사이트는 오늘~+7일만 엽니다)")
	}
	if strings.TrimSpace(c.Booking.Subject) == "" {
		return fmt.Errorf("회의명을 입력하세요")
	}
	if c.Booking.GapSeconds < 0 || c.Booking.GapSeconds > 300 {
		return fmt.Errorf("연속 신청 간격은 0~300초여야 합니다")
	}
	// 한도가 계정별이므로 상한도 계정 수에 비례한다. 계정이 아직 없으면 1계정으로 친다.
	if n := c.MaxCount(); c.Booking.Count < 1 || c.Booking.Count > n {
		return fmt.Errorf("예약 건수는 1~%d 여야 합니다 (계정 %d개 × 하루 %d건)",
			n, max(1, len(c.Site.ReadyAccounts())), MaxBookingsPerDay)
	}
	for i, t := range c.Booking.Targets {
		if err := ValidateTarget(t, i); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Booking.PurposeCd) == "" {
		return fmt.Errorf("이용목적을 선택하세요")
	}
	// 위에서부터 Count 건을 잡으므로, 그 합이 하루 한도를 넘으면 마지막 건은 반드시 거절된다.
	// 발사 시점이 아니라 저장 시점에 걸러 준다.
	if n, cap := plannedSlots(c.Booking), MaxDailySlots*max(1, len(c.Site.ReadyAccounts())); n > cap {
		return fmt.Errorf("예약 %d건의 합이 %.1f시간으로 하루 한도 %.0f시간(계정 %d개)을 넘습니다 — 길이를 줄이거나 계정을 늘리세요",
			c.Booking.Count, float64(n)/2, float64(cap)/2, max(1, len(c.Site.ReadyAccounts())))
	}
	if fb := c.Booking.Fallback; fb.Enabled {
		if err := validateFallback(fb); err != nil {
			return err
		}
	}
	// 지정 스케줄은 건마다 후보를 따로 들 수 있으므로, 「예약 설정」이 비어 있어도
	// 모든 건이 자기 후보를 가지고 있으면 문제가 없다.
	if len(c.Booking.Targets) == 0 && !c.Booking.Fallback.Enabled && !c.everyEntrySelfContained() {
		return fmt.Errorf("예약 대상이 하나도 없고 폴백도 꺼져 있습니다")
	}
	if !c.Booking.Fallback.Enabled && len(c.Booking.Targets) < c.Booking.Count {
		return fmt.Errorf("예약 건수가 %d건인데 지정 대상은 %d개뿐입니다 — 대상을 더 넣거나 폴백을 켜세요",
			c.Booking.Count, len(c.Booking.Targets))
	}
	if c.Notion.Enabled && NormalizeDatabaseID(c.Notion.DatabaseID) == "" {
		return fmt.Errorf("Notion 이 켜져 있지만 데이터베이스 ID 가 올바르지 않습니다 (32자리 hex)")
	}
	if _, err := time.LoadLocation(c.Runtime.Timezone); err != nil {
		return fmt.Errorf("타임존이 잘못됨: %q", c.Runtime.Timezone)
	}
	return nil
}

// NewAccountID 는 계정을 지목할 불변 키를 만든다. 아이디가 바뀌어도 화면의 참조가 살아 있게
// 사용자 입력과 무관한 값을 쓴다.
func NewAccountID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("acc-%d", time.Now().UnixNano())
	}
	return "acc-" + hex.EncodeToString(b)
}

// validateFallback 은 폴백 규칙 하나를 검사한다(「예약 설정」과 지정 예약이 함께 쓴다).
func validateFallback(fb Fallback) error {
	if _, err := ParseHHMM(fb.Start); err != nil {
		return fmt.Errorf("폴백 시작 시각: %w", err)
	}
	if fb.MinSlots < 1 || fb.MinSlots > MaxSlots {
		return fmt.Errorf("폴백 최소 슬롯은 1~%d(최대 %.0f시간) 여야 합니다", MaxSlots, float64(MaxSlots)/2)
	}
	if fb.MaxSlots < fb.MinSlots || fb.MaxSlots > MaxSlots {
		return fmt.Errorf("폴백 최대 슬롯은 최소 슬롯 이상, %d 이하여야 합니다", MaxSlots)
	}
	if fb.StartWindowMinutes < 0 || fb.StartWindowMinutes > 12*60 {
		return fmt.Errorf("폴백 시작 허용 범위는 0~720분이어야 합니다")
	}
	return nil
}

// everyEntrySelfContained 는 켜져 있는 지정 예약이 모두 자기 후보(또는 자기 폴백)를
// 들고 있는지다. 그렇다면 「예약 설정」의 지정 대상이 비어 있어도 상관없다.
func (c *Config) everyEntrySelfContained() bool {
	live := c.Schedule.LiveEntries()
	if !c.Schedule.UsesDates() || len(live) == 0 {
		return false
	}
	for _, e := range live {
		if len(e.Targets) == 0 && (e.Fallback == nil || !e.Fallback.Enabled) {
			return false
		}
	}
	return true
}

// plannedSlots 는 Count 건을 잡았을 때 쓰게 될 슬롯 합계를 어림한다.
// 지정 대상이 모자라면 남는 건수는 폴백의 최소 길이로 친다.
func plannedSlots(b Booking) int {
	total, n := 0, 0
	for _, t := range b.Targets {
		if n >= b.Count {
			break
		}
		total += t.DurationSlots
		n++
	}
	if n < b.Count && b.Fallback.Enabled {
		total += (b.Count - n) * b.Fallback.MinSlots
	}
	return total
}

// ReadyToRun 은 실제 발사에 필요한 자격증명이 갖춰졌는지 본다.
func (c *Config) ReadyToRun() error {
	if len(c.Site.ReadyAccounts()) == 0 {
		return fmt.Errorf("쓸 수 있는 jointips 계정이 없습니다 (아이디·비밀번호를 저장하고 켜 두세요)")
	}
	if strings.TrimSpace(c.Booking.Manager) == "" {
		return fmt.Errorf("담당자명을 입력하세요")
	}
	return nil
}

var hex32 = regexp.MustCompile(`[0-9a-fA-F]{32}`)

// NormalizeDatabaseID 는 Notion DB 링크를 붙여넣어도 32자리 hex 를 뽑아낸다(§7.1).
func NormalizeDatabaseID(s string) string {
	compact := strings.ReplaceAll(s, "-", "")
	m := hex32.FindString(compact)
	return strings.ToLower(m)
}

// ── 저장소 ─────────────────────────────────────────────────────────────────

// Store 는 설정 파일 하나를 감싼 뮤텍스 보호 저장소다.
type Store struct {
	path  string
	vault *Vault
	mu    sync.RWMutex
	cfg   Config
	notes []string
}

// Open 은 설정 파일을 읽고(없으면 기본값으로 만들고) Store 를 돌려준다.
func Open(path string, vault *Vault) (*Store, error) {
	s := &Store{path: path, vault: vault, cfg: Default()}
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := s.save(s.cfg); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, fmt.Errorf("설정 파일 읽기 실패: %w", err)
	default:
		cfg := Default()
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("설정 파일이 올바른 JSON 이 아닙니다 (%s): %w", path, err)
		}
		if notes := Migrate(&cfg); len(notes) > 0 {
			s.notes = notes
			if err := s.save(cfg); err != nil {
				return nil, err
			}
		}
		s.cfg = cfg
	}
	return s, nil
}

// Notes 는 기동 시 설정을 손본 내역이다. 화면과 로그가 한 번 보여 준다.
func (s *Store) Notes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.notes...)
}

// Migrate 는 2026-09 사이트 개편 전에 저장된 설정을 새 구조로 옮긴다.
//
// 손댄 항목을 문자열로 돌려주고, 그 목록이 비어 있지 않으면 호출자가 다시 저장한다.
// 옛 회의실 지정(빌딩/층/room_id)은 새 공간 코드로 기계적으로 바꿀 방법이 없다 —
// 매핑 표가 사이트에만 있고 공개되지 않는다. 잘못 짐작해 엉뚱한 방을 잡느니
// 비워 두고 사람이 다시 고르게 한다.
func Migrate(c *Config) []string {
	var notes []string

	// www 는 301 로 튕기고, 301 은 POST 를 GET 으로 바꿔 로그인이 조용히 실패한다.
	if u := strings.TrimRight(strings.TrimSpace(c.Site.BaseURL), "/"); u == "https://www.jointips.or.kr" || u == "http://www.jointips.or.kr" {
		c.Site.BaseURL = CanonicalBaseURL
		notes = append(notes, "사이트 주소를 "+CanonicalBaseURL+" 로 바꿨습니다 (www 는 301 로 튕겨 로그인이 실패합니다)")
	}
	// 옛 발사 시각은 서버 시계가 39분 느린 것을 보정한 값이었다. 새 서버는 정확하다.
	if c.Schedule.FireAt == "00:40:01.000" {
		c.Schedule.FireAt = "00:00:01.000"
		notes = append(notes, "발사 시각을 00:00:01.000 으로 되돌렸습니다 (새 서버는 시계가 거의 정확합니다)")
	}
	if n := len(c.Booking.Targets); n > 0 {
		kept := c.Booking.Targets[:0]
		for _, t := range c.Booking.Targets {
			if strings.TrimSpace(t.SpaceCd) != "" {
				kept = append(kept, t)
			}
		}
		c.Booking.Targets = kept
		if dropped := n - len(kept); dropped > 0 {
			notes = append(notes, fmt.Sprintf("옛 형식의 지정 대상 %d개를 비웠습니다 — 「예약 설정」에서 회의실을 다시 고르세요", dropped))
		}
	}
	if c.Booking.Fallback.Enabled && c.Booking.Fallback.BldgCd == "" && c.Booking.Fallback.PreferFloor != "" {
		// 옛 층 코드("27")는 새 floorNo("5")와 체계가 다르다. 남겨 두면 폴백이 영영 빈손이 된다.
		c.Booking.Fallback.PreferFloor = ""
		notes = append(notes, "폴백의 선호 층을 비웠습니다 (층 표기 체계가 바뀌었습니다)")
	}
	if strings.TrimSpace(c.Booking.PurposeCd) == "" {
		c.Booking.PurposeCd = DefaultPurposeCd
		notes = append(notes, "이용목적 기본값을 '업무 회의'로 채웠습니다")
	}
	// 단일 계정 → 계정 목록. 옛 필드는 옮기고 비운다.
	if len(c.Site.Accounts) == 0 && strings.TrimSpace(c.Site.LegacyUsername) != "" {
		c.Site.Accounts = []Account{{
			ID:          NewAccountID(),
			Label:       "",
			Username:    strings.TrimSpace(c.Site.LegacyUsername),
			PasswordEnc: c.Site.LegacyPasswordEnc,
			Enabled:     true,
		}}
		notes = append(notes, "기존 계정을 계정 목록으로 옮겼습니다 — 이제 계정을 여러 개 둘 수 있습니다")
	}
	c.Site.LegacyUsername, c.Site.LegacyPasswordEnc = "", ""
	// id 가 없는 계정(손으로 고친 설정)에 id 를 채워 준다.
	for i := range c.Site.Accounts {
		if strings.TrimSpace(c.Site.Accounts[i].ID) == "" {
			c.Site.Accounts[i].ID = NewAccountID()
		}
	}
	if c.Schedule.Mode == "" {
		c.Schedule.Mode = ModeWeekly
	}
	// 날짜 목록 → 항목별 설정. 옛 목록은 실행일만 있던 것이라 나머지는 비워 둔다
	// (비면 「예약 설정」의 값을 그대로 쓰므로 동작이 달라지지 않는다).
	if len(c.Schedule.Entries) == 0 && len(c.Schedule.LegacyDates) > 0 {
		for _, d := range c.Schedule.LegacyDates {
			c.Schedule.Entries = append(c.Schedule.Entries, ScheduleEntry{
				ID: NewEntryID(), Date: strings.TrimSpace(d), Enabled: true,
			})
		}
		notes = append(notes, fmt.Sprintf("지정 스케줄 %d건을 항목별 설정으로 옮겼습니다 — 이제 예약마다 회의명·담당자·예약자를 따로 정할 수 있습니다", len(c.Schedule.LegacyDates)))
	}
	c.Schedule.LegacyDates = nil
	for i := range c.Schedule.Entries {
		if strings.TrimSpace(c.Schedule.Entries[i].ID) == "" {
			c.Schedule.Entries[i].ID = NewEntryID()
		}
	}
	c.Schedule.SortEntries()
	// 1회 3시간·1일 5시간으로 줄었다. 예전 설정(4시간×2건)은 그대로 두면 반드시 거절당한다.
	for i := range c.Booking.Targets {
		if c.Booking.Targets[i].DurationSlots > MaxSlots {
			c.Booking.Targets[i].DurationSlots = MaxSlots
			notes = append(notes, fmt.Sprintf("대상 #%d 길이를 %.0f시간으로 줄였습니다 (1회 한도)", i+1, float64(MaxSlots)/2))
		}
	}
	if c.Booking.Fallback.MaxSlots > MaxSlots {
		c.Booking.Fallback.MaxSlots = MaxSlots
		notes = append(notes, fmt.Sprintf("폴백 최대 길이를 %.0f시간으로 줄였습니다 (1회 한도)", float64(MaxSlots)/2))
	}
	if c.Booking.Fallback.MinSlots > c.Booking.Fallback.MaxSlots {
		c.Booking.Fallback.MinSlots = c.Booking.Fallback.MaxSlots
	}
	if c.Booking.GapSeconds > 30 {
		c.Booking.GapSeconds = 2
		notes = append(notes, "연속 신청 간격을 2초로 줄였습니다 (옛 도배 방지 제한이 사라졌습니다)")
	}
	return notes
}

// Get 은 설정의 복사본을 돌려준다(슬라이스까지 깊은 복사).
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cfg)
}

// Update 는 fn 으로 설정을 고치고 검증한 뒤 저장한다. 검증 실패 시 아무것도 바뀌지 않는다.
func (s *Store) Update(fn func(*Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.cfg)
	if err := fn(&next); err != nil {
		return Config{}, err
	}
	if err := next.Validate(); err != nil {
		return Config{}, err
	}
	if err := s.save(next); err != nil {
		return Config{}, err
	}
	s.cfg = next
	return clone(next), nil
}

func (s *Store) save(cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("설정 저장 실패: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// Password / Token 은 필요할 때만 복호화한다. 호출부는 결과를 로그에 남기지 않는다.
// AccountPassword 는 계정 하나의 비밀번호를 푼다.
func (s *Store) AccountPassword(id string) (string, error) {
	a, ok := s.Get().Site.Account(id)
	if !ok {
		return "", fmt.Errorf("계정을 찾을 수 없습니다: %s", id)
	}
	return s.vault.Decrypt(a.PasswordEnc)
}
func (s *Store) NotionToken() (string, error) {
	return s.vault.Decrypt(s.Get().Notion.TokenEnc)
}

// Vault 는 Store 가 쓰는 암호화기다.
func (s *Store) Vault() *Vault { return s.vault }

// clone 은 Get/Update 가 돌려주는 사본을 만든다.
//
// 슬라이스를 하나도 빠뜨리면 안 된다 — 호출자(러너)는 설정을 실행 내내 들고 있는데,
// 그 사이 웹에서 Update 가 같은 배열을 건드리면 값이 발밑에서 바뀐다.
//
// append([]T(nil), …) 는 빈 슬라이스를 **nil 로** 만든다. 그러면 JSON 에 null 로 나가고
// 화면이 .length 에서 터진다(실제로 요일을 전부 끄고 저장했을 때 그랬다).
// 그래서 길이 0 짜리를 명시적으로 만들어 항상 [] 로 나가게 한다.
func clone(c Config) Config {
	out := c
	out.Booking.Targets = append(make([]Target, 0, len(c.Booking.Targets)), c.Booking.Targets...)
	out.Site.Accounts = append(make([]Account, 0, len(c.Site.Accounts)), c.Site.Accounts...)
	out.Schedule.Entries = make([]ScheduleEntry, 0, len(c.Schedule.Entries))
	for _, e := range c.Schedule.Entries {
		// 항목 안의 후보 배열과 폴백 포인터까지 끊어 줘야 한다 — 얕게 베끼면
		// 사본을 고친 게 원본에 그대로 들어간다.
		e.Targets = append(make([]Target, 0, len(e.Targets)), e.Targets...)
		if e.Fallback != nil {
			fb := *e.Fallback
			e.Fallback = &fb
		}
		out.Schedule.Entries = append(out.Schedule.Entries, e)
	}
	out.Schedule.Weekdays = append(make([]int, 0, len(c.Schedule.Weekdays)), c.Schedule.Weekdays...)
	sort.Ints(out.Schedule.Weekdays)
	return out
}
