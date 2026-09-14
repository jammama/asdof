// Package config 는 asdof-taker 의 설정을 정의하고, 디스크(JSON)에 원자적으로
// 읽고 쓴다. 비밀번호/토큰 같은 비밀값은 AES-256-GCM 으로 암호화해서 보관하며
// (§8 "비밀정보 취급"), 웹 UI 로는 절대 평문을 내보내지 않는다.
package config

import (
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

// Target 은 "이 회의실을 이 시각부터 이만큼" 으로 완전히 확정된 예약 대상이다.
// 확정돼 있으므로 발사 시점에 조회 없이 곧바로 POST 할 수 있다(§5.1).
type Target struct {
	RoomID        int    `json:"room_id"`  // wr_8
	Building      string `json:"building"` // wr_6
	Floor         string `json:"floor"`    // wr_7
	Start         string `json:"start"`    // wr_4, "HH:MM"
	DurationSlots int    `json:"duration_slots"`
}

// Fallback 은 지정 대상이 모두 실패했을 때 조회 결과에서 자리를 찾는 규칙이다(§5.2).
type Fallback struct {
	Enabled            bool   `json:"enabled"`
	Building           string `json:"building"`     // "" = 전체
	PreferFloor        string `json:"prefer_floor"` // "" = 층 무관
	Start              string `json:"start"`
	StartWindowMinutes int    `json:"start_window_minutes"`
	MinSlots           int    `json:"min_slots"`
	MaxSlots           int    `json:"max_slots"`
}

type Booking struct {
	DateOffsetDays int `json:"date_offset_days"`
	// GapSeconds 는 예약 한 건을 잡은 뒤 다음 건을 신청하기까지 두는 간격이다.
	// gnuboard 의 도배 방지(cf_delay_sec)가 같은 계정의 연속 등록을 막는다 —
	// "너무 빠른 시간내에 게시물을 연속해서 올릴 수 없습니다." 기본값은 30초라 여유를 얹는다.
	GapSeconds int `json:"gap_seconds"`
	// Count 는 하루에 확보할 예약 '건수'다. targets 를 위에서부터 시도해 이 개수를
	// 채우면 멈춘다. 사이트 정책상 팀당 하루 2건이 상한이다(§3.9).
	Count    int      `json:"count"`
	Subject  string   `json:"subject"`
	Manager  string   `json:"manager"`
	Contact  string   `json:"contact"`
	Content  string   `json:"content"`
	Targets  []Target `json:"targets"`
	Fallback Fallback `json:"fallback"`
}

type Retry struct {
	IntervalMS   int `json:"interval_ms"`
	MaxDurationS int `json:"max_duration_s"`
	MaxAttempts  int `json:"max_attempts"`
}

type Schedule struct {
	Enabled     bool   `json:"enabled"`
	FireAt      string `json:"fire_at"`  // "00:40:01.000" — 사이트 시계가 느려 실제 개방은 00:39 무렵
	Weekdays    []int  `json:"weekdays"` // 0=일 … 6=토. 비어 있으면 매일
	WarmupLeadS int    `json:"warmup_lead_s"`
	UIDPoolSize int    `json:"uid_pool_size"`
	Retry       Retry  `json:"retry"`
}

type Site struct {
	BaseURL     string `json:"base_url"`
	Username    string `json:"username"`
	PasswordEnc string `json:"password_enc"` // 암호문. 평문은 절대 저장/전송하지 않는다
	UserAgent   string `json:"user_agent"`
	TimeoutS    int    `json:"timeout_s"`
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
			BaseURL:   "https://www.jointips.or.kr",
			UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
			TimeoutS:  10,
		},
		Schedule: Schedule{
			Enabled: false, // 자격증명이 채워지기 전에는 켜지 않는다
			// 예약 창은 자정에 롤링된다(§6.4 의 추정이 맞았다 — 정오가 아니다).
			// 단, 그 자정은 **jointips 서버의 시계** 기준이고 그 시계가 실제보다 약 39분 느리다(실측).
			// 그래서 새 날짜가 실제로 열리는 시각은 우리 시계로 00:39 무렵이다. 여기에 여유를 얹었다.
			// 사이트가 시계를 고치면 00:00:01 근처로 되돌려야 하며, 화면 하단 각주가 그 신호다.
			FireAt:      "00:40:01.000",
			Weekdays:    []int{1, 2, 3, 4, 5},
			WarmupLeadS: 180,
			UIDPoolSize: 4,
			Retry:       Retry{IntervalMS: 250, MaxDurationS: 15, MaxAttempts: 40},
		},
		Booking: Booking{
			DateOffsetDays: 6,
			Count:          1,
			GapSeconds:     35,
			Subject:        "주간 정기 회의",
			Content:        "회의할 내용",
			Targets:        []Target{},
			Fallback: Fallback{
				Enabled:            true,
				Start:              "13:00",
				StartWindowMinutes: 60,
				MinSlots:           4,
				MaxSlots:           8,
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

// NextFire 는 now 이후의 가장 이른 발사 시각을 계산한다. 요일 필터를 존중한다.
func (s Schedule) NextFire(now time.Time, loc *time.Location) (time.Time, error) {
	off, err := ParseFireAt(s.FireAt)
	if err != nil {
		return time.Time{}, err
	}
	now = now.In(loc)
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

// AllowsWeekday 는 그 요일에 실행할지 여부다. 빈 목록이면 매일.
func (s Schedule) AllowsWeekday(wd int) bool {
	if len(s.Weekdays) == 0 {
		return true
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
	GridStartMin = 9 * 60     // 09:00
	GridEndMin   = 20*60 + 30 // 20:30 (마지막 슬롯의 시작)
	GridLastEnd  = 21 * 60    // 21:00 (예약 가능한 최대 종료시각)
	SlotMinutes  = 30
	GridSlots    = 24 // 09:00~20:30, 30분 단위
	MaxSlots     = 8  // 사이트 정책: 예약 1건당 최대 4시간

	// MaxBookingsPerDay 는 사이트가 팀 단위로 허용하는 하루 예약 건수다(§3.9).
	// 이보다 많이 시도하면 "2번만 예약 가능합니다" alert 로 거절당한다.
	MaxBookingsPerDay = 2
)

// ValidateTarget 은 문서 §5.0 의 1~3번 규칙을 검사한다.
func ValidateTarget(t Target, idx int) error {
	label := fmt.Sprintf("대상 #%d", idx+1)
	if t.RoomID <= 0 {
		return fmt.Errorf("%s: 회의실을 선택하세요", label)
	}
	if t.DurationSlots < 1 || t.DurationSlots > MaxSlots {
		return fmt.Errorf("%s: 예약 길이는 1~%d슬롯(최대 4시간)이어야 합니다 (현재 %d)", label, MaxSlots, t.DurationSlots)
	}
	start, err := ParseHHMM(t.Start)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if start%SlotMinutes != 0 {
		return fmt.Errorf("%s: 시작 시각은 :00 또는 :30 이어야 합니다 (현재 %s)", label, t.Start)
	}
	if start < GridStartMin || start > GridEndMin {
		return fmt.Errorf("%s: 시작 시각은 09:00~20:30 범위여야 합니다 (현재 %s)", label, t.Start)
	}
	if start+t.DurationSlots*SlotMinutes > GridLastEnd {
		return fmt.Errorf("%s: 종료 시각이 21:00 을 넘습니다 (%s + %d슬롯)", label, t.Start, t.DurationSlots)
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
	if c.Schedule.WarmupLeadS < 30 || c.Schedule.WarmupLeadS > 3600 {
		return fmt.Errorf("워밍업 선행 시간은 30~3600초여야 합니다")
	}
	if c.Schedule.UIDPoolSize < 1 || c.Schedule.UIDPoolSize > 20 {
		return fmt.Errorf("uid 풀 크기는 1~20 이어야 합니다")
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
		return fmt.Errorf("대상 날짜 오프셋은 0~7일이어야 합니다 (사이트의 7일 창 제한)")
	}
	if strings.TrimSpace(c.Booking.Subject) == "" {
		return fmt.Errorf("회의명을 입력하세요")
	}
	if c.Booking.GapSeconds < 0 || c.Booking.GapSeconds > 300 {
		return fmt.Errorf("연속 신청 간격은 0~300초여야 합니다")
	}
	if c.Booking.Count < 1 || c.Booking.Count > MaxBookingsPerDay {
		return fmt.Errorf("예약 건수는 1~%d 여야 합니다 (사이트가 팀당 하루 %d건까지만 받습니다)",
			MaxBookingsPerDay, MaxBookingsPerDay)
	}
	for i, t := range c.Booking.Targets {
		if err := ValidateTarget(t, i); err != nil {
			return err
		}
	}
	if fb := c.Booking.Fallback; fb.Enabled {
		if _, err := ParseHHMM(fb.Start); err != nil {
			return fmt.Errorf("폴백 시작 시각: %w", err)
		}
		if fb.MinSlots < 1 || fb.MinSlots > MaxSlots {
			return fmt.Errorf("폴백 최소 슬롯은 1~%d 여야 합니다", MaxSlots)
		}
		if fb.MaxSlots < fb.MinSlots || fb.MaxSlots > MaxSlots {
			return fmt.Errorf("폴백 최대 슬롯은 최소 슬롯 이상, %d 이하여야 합니다", MaxSlots)
		}
		if fb.StartWindowMinutes < 0 || fb.StartWindowMinutes > 12*60 {
			return fmt.Errorf("폴백 시작 허용 범위는 0~720분이어야 합니다")
		}
	}
	if len(c.Booking.Targets) == 0 && !c.Booking.Fallback.Enabled {
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

// ReadyToRun 은 실제 발사에 필요한 자격증명이 갖춰졌는지 본다.
func (c *Config) ReadyToRun() error {
	if strings.TrimSpace(c.Site.Username) == "" || c.Site.PasswordEnc == "" {
		return fmt.Errorf("jointips 아이디/비밀번호가 설정되지 않았습니다")
	}
	if strings.TrimSpace(c.Booking.Manager) == "" {
		return fmt.Errorf("담당자명을 입력하세요")
	}
	if strings.TrimSpace(c.Booking.Contact) == "" {
		return fmt.Errorf("연락처를 입력하세요")
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
		s.cfg = cfg
	}
	return s, nil
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
func (s *Store) Password() (string, error) { return s.vault.Decrypt(s.Get().Site.PasswordEnc) }
func (s *Store) NotionToken() (string, error) {
	return s.vault.Decrypt(s.Get().Notion.TokenEnc)
}

// Vault 는 Store 가 쓰는 암호화기다.
func (s *Store) Vault() *Vault { return s.vault }

func clone(c Config) Config {
	out := c
	out.Booking.Targets = append([]Target(nil), c.Booking.Targets...)
	out.Schedule.Weekdays = append([]int(nil), c.Schedule.Weekdays...)
	sort.Ints(out.Schedule.Weekdays)
	return out
}
