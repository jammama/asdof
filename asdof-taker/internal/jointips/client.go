// Package jointips 는 jointips.or.kr 공용공간(회의실) 예약의 HTTP 프로토콜을 구현한다.
//
// 2026-09 사이트 개편으로 프로토콜이 통째로 바뀌었다. 예전(그누보드)과의 차이:
//
//   - 폼 POST(/bbs/write_update.php) → JSON REST(/api/cms/…)
//   - 쿠키 세션 → Bearer 토큰(memberToken). 응답 헤더 X-Member-Token 으로 연장된다.
//   - 중복 제출 방지 uid → 없음. 미리 확보해 둘 토큰이 사라져 발사 경로가 한 번 더 짧아졌다.
//   - HTML 표 파싱 → 슬롯 JSON(status: AVAILABLE|UNAVAILABLE|MY_CONFLICT)
//   - 회의실 = 빌딩/층/room_id 3종 → 공간 코드 하나(spaceCd, 예: "BLDG004_04_001")
//
// 조회 계열(/api/cms/public/…)은 토큰 없이도 응답하지만, 본인 사용량(myUsedHour 등)은
// 토큰이 있어야 채워진다. 그래서 조회에도 토큰이 있으면 함께 보낸다.
package jointips

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// SiteID 는 멀티사이트 테넌트 라우팅 키다. 페이지가 window.__SITE_ID__ 로 주입하는 값.
	SiteID = 4
	// Category 는 공용공간. 장소대관(RENTAL)과 섞이지 않게 모든 호출에 붙인다.
	Category = "COMMON"
	// DefaultRegion 은 현재 유일한 지역 코드다(REGION 공통코드).
	DefaultRegion = "SEOUL"
)

// Client 는 로그인 세션 하나다. 토큰을 들고 다닌다.
type Client struct {
	base string
	ua   string
	hc   *http.Client

	mu     sync.Mutex
	token  string
	member Member
}

// New 는 클라이언트를 만든다. keep-alive 를 넉넉히 유지해 발사 시점에
// TLS 핸드셰이크가 남아 있지 않게 한다(§2).
func New(baseURL, userAgent string, timeout time.Duration) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("사이트 주소가 비어 있습니다")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 4
	tr.IdleConnTimeout = 10 * time.Minute
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	}
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		ua:   userAgent,
		hc:   &http.Client{Timeout: timeout, Transport: tr},
	}, nil
}

// ── 인증 ───────────────────────────────────────────────────────────────────

// Member 는 로그인한 회원이다.
type Member struct {
	Email  string `json:"email"`
	UserNm string `json:"userNm"`
}

// Login 은 회원 로그인 후 토큰을 보관한다.
//
// 실패해도 재시도하지 않는다 — 계정 잠금과 불필요한 부하를 피한다.
func (c *Client) Login(ctx context.Context, email, password string) error {
	var out struct {
		MemberToken string `json:"memberToken"`
		UserNm      string `json:"userNm"`
		Email       string `json:"email"`
		PwExpired   bool   `json:"pwExpired"`
		MustChange  bool   `json:"mustChange"`
	}
	body := map[string]any{"siteId": SiteID, "email": email, "password": password}
	if err := c.do(ctx, http.MethodPost, "/api/cms/member/login", body, &out); err != nil {
		return err
	}
	// 비밀번호 90일 만료/임시비번이면 토큰을 주지 않고 pwExpired 만 온다.
	if out.MemberToken == "" {
		if out.MustChange {
			return fmt.Errorf("임시 비밀번호로는 로그인할 수 없습니다 — 사이트에서 비밀번호를 먼저 바꾸세요")
		}
		if out.PwExpired {
			return fmt.Errorf("비밀번호를 바꾼 지 90일이 지났습니다 — 사이트에서 비밀번호를 갱신하세요")
		}
		return fmt.Errorf("로그인 응답에 토큰이 없습니다")
	}
	c.mu.Lock()
	c.token = out.MemberToken
	c.member = Member{Email: out.Email, UserNm: out.UserNm}
	c.mu.Unlock()
	return nil
}

// Member 는 로그인한 회원 정보다. 로그인 전에는 빈 값.
func (c *Client) Member() Member {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.member
}

// LoggedIn 은 토큰을 들고 있는지다.
func (c *Client) LoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token != ""
}

// Ping 은 세션을 연장한다(응답 헤더의 새 토큰은 do 가 알아서 갈아끼운다).
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/cms/member/ping", nil, nil)
}

// ── 마스터 조회 ────────────────────────────────────────────────────────────

// Code 는 공통코드 한 줄이다(REGION, USE_PURPOSE).
type Code struct {
	DetailCd string `json:"detailCd"`
	CodeNm   string `json:"codeNm"`
	SortOrd  int    `json:"sortOrd"`
}

// Building 은 건물(장소)이다.
type Building struct {
	BldgCd   string `json:"bldgCd"`
	BldgNm   string `json:"bldgNm"`
	RegionCd string `json:"regionCd"`
	RegionNm string `json:"regionNm"`
	Addr     string `json:"addr"`
}

// Space 는 예약 대상 공간(회의실) 하나다.
type Space struct {
	SpaceCd   string `json:"spaceCd"`
	BldgCd    string `json:"bldgCd"`
	BldgNm    string `json:"bldgNm"`
	SpaceNm   string `json:"spaceNm"`
	FloorNo   string `json:"floorNo"`
	Capacity  int    `json:"capacity"`
	OpenTime  string `json:"openTime"`
	CloseTime string `json:"closeTime"`
	SlotUnit  int    `json:"slotUnit"`
	TvYn      string `json:"tvYn"`
	// ReserveStatus 가 "Y" 가 아니면 신청을 받지 않는다(유의사항 1번).
	ReserveStatus string `json:"reserveStatus"`
	UseYn         string `json:"useYn"`
	DayMonYn      string `json:"dayMonYn"`
	DayTueYn      string `json:"dayTueYn"`
	DayWedYn      string `json:"dayWedYn"`
	DayThuYn      string `json:"dayThuYn"`
	DayFriYn      string `json:"dayFriYn"`
	DaySatYn      string `json:"daySatYn"`
	DaySunYn      string `json:"daySunYn"`
}

// Bookable 은 신청을 받는 공간인지다.
func (s Space) Bookable() bool { return s.ReserveStatus == "Y" && s.UseYn != "N" }

// OpenOn 은 그 요일(0=일)에 운영하는지다.
func (s Space) OpenOn(weekday int) bool {
	switch weekday {
	case 0:
		return s.DaySunYn == "Y"
	case 1:
		return s.DayMonYn == "Y"
	case 2:
		return s.DayTueYn == "Y"
	case 3:
		return s.DayWedYn == "Y"
	case 4:
		return s.DayThuYn == "Y"
	case 5:
		return s.DayFriYn == "Y"
	case 6:
		return s.DaySatYn == "Y"
	}
	return false
}

// Regions 는 지역 공통코드다.
func (c *Client) Regions(ctx context.Context) ([]Code, error) {
	var out []Code
	err := c.do(ctx, http.MethodGet, c.q("/api/code/detail-list",
		"groupCd", "REGION", "langCd", "ko", "useYn", "Y"), nil, &out)
	return out, err
}

// Purposes 는 이용목적 공통코드다(USE_PURPOSE).
func (c *Client) Purposes(ctx context.Context) ([]Code, error) {
	var out []Code
	err := c.do(ctx, http.MethodGet, c.q("/api/code/detail-list",
		"groupCd", "USE_PURPOSE", "langCd", "ko", "useYn", "Y"), nil, &out)
	return out, err
}

// Buildings 는 지역의 건물 목록이다.
func (c *Client) Buildings(ctx context.Context, regionCd string) ([]Building, error) {
	if regionCd == "" {
		regionCd = DefaultRegion
	}
	var out []Building
	err := c.do(ctx, http.MethodGet, c.q("/api/cms/public/reservation/buildings",
		"category", Category, "regionCd", regionCd), nil, &out)
	return out, err
}

// Spaces 는 건물의 공간 목록이다.
func (c *Client) Spaces(ctx context.Context, bldgCd string) ([]Space, error) {
	var out []Space
	err := c.do(ctx, http.MethodGet, c.q("/api/cms/public/reservation/spaces",
		"bldgCd", bldgCd, "category", Category), nil, &out)
	return out, err
}

// BookableDays 는 카테고리 전체의 운영 요일이다: {"MON":"Y",…,"SUN":"N"}.
func (c *Client) BookableDays(ctx context.Context) (map[string]string, error) {
	var out map[string]string
	err := c.do(ctx, http.MethodGet, c.q("/api/cms/public/reservation/policy/bookable-days",
		"category", Category), nil, &out)
	return out, err
}

// ── 슬롯 조회 ──────────────────────────────────────────────────────────────

// 슬롯 상태. UNAVAILABLE 만 선택 불가다 — MY_CONFLICT(같은 시간 다른 장소에 내 예약)는
// 정책이 바뀌어 허용된다.
const (
	SlotAvailable   = "AVAILABLE"
	SlotUnavailable = "UNAVAILABLE"
	SlotMyConflict  = "MY_CONFLICT"
)

// Slot 은 30분(slotUnit) 한 칸이다.
type Slot struct {
	SlotTime string `json:"slotTime"` // "09:00"
	EndTime  string `json:"endTime"`  // "09:30"
	Status   string `json:"status"`
}

// Free 는 신청 가능한 칸인지다.
func (s Slot) Free() bool { return s.Status != SlotUnavailable }

// SpaceSlots 는 공간 하나의 하루치 슬롯과 그날의 내 사용량이다.
//
// 한도 필드(dailyLimit*/singleLimit*/my*)는 토큰이 있을 때만 채워진다.
type SpaceSlots struct {
	SpaceCd     string `json:"spaceCd"`
	SpaceNm     string `json:"spaceNm"`
	BldgCd      string `json:"bldgCd"`
	OpenTime    string `json:"openTime"`
	CloseTime   string `json:"closeTime"`
	SlotUnit    int    `json:"slotUnit"`
	DayBookable bool   `json:"dayBookable"`

	DailyLimitHour  float64 `json:"dailyLimitHour"`
	DailyLimitCount int     `json:"dailyLimitCount"`
	SingleLimitHour float64 `json:"singleLimitHour"`
	MyUsedHour      float64 `json:"myUsedHour"`
	MyDailyCount    int     `json:"myDailyCount"`

	Slots []Slot `json:"slots"`
}

// Slots 는 공간 하나의 하루치 슬롯이다. 한도 필드까지 받으려면 로그인 상태여야 한다.
func (c *Client) Slots(ctx context.Context, spaceCd, date string) (*SpaceSlots, error) {
	var out SpaceSlots
	err := c.do(ctx, http.MethodGet, c.q("/api/cms/public/reservation/slots",
		"spaceCd", spaceCd, "date", date), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SlotsBatch 는 여러 공간의 하루치 슬롯을 한 번에 받는다(현황 조회용).
// 공간당 1회씩 부르면 왕복이 N배가 되므로 조회는 반드시 이쪽을 쓴다.
func (c *Client) SlotsBatch(ctx context.Context, spaceCds []string, date string) ([]SpaceSlots, error) {
	if len(spaceCds) == 0 {
		return nil, nil
	}
	var out []SpaceSlots
	err := c.do(ctx, http.MethodGet, c.q("/api/cms/public/reservation/slots-batch",
		"spaceCds", strings.Join(spaceCds, ","), "date", date), nil, &out)
	return out, err
}

// ── 신청 ───────────────────────────────────────────────────────────────────

// Reservation 은 신청 한 건이다.
type Reservation struct {
	BldgCd      string   // "BLDG004"
	SpaceCd     string   // "BLDG004_04_001"
	ReserveDate string   // "2026-09-16"
	SlotTimes   []string // ["13:00","13:30",…] — 연속이어야 한다
	Title       string   // 회의명
	PurposeCd   string   // USE_PURPOSE 코드
	PurposeEtc  string   // purposeCd 가 "기타" 일 때만
}

// SubmitResult 는 신청 1회의 결과다. 확정 판정은 재조회(2차)로 한다.
type SubmitResult struct {
	Status    int
	Code      int    // API 봉투의 code (200 = 성공)
	Message   string // 실패 사유. 사이트가 한도/중복을 여기로 알려준다.
	ReserveID string
	Elapsed   time.Duration
}

// OK 는 신청이 받아들여졌는지다.
func (r *SubmitResult) OK() bool { return r != nil && r.Code == 200 }

// Submit 은 예약을 신청한다.
//
// 실패를 에러가 아니라 SubmitResult.Message 로 돌려준다 — 한도 초과나 슬롯 선점은
// 재시도 루프가 판단해야 할 '정상적인 거절'이지 전송 오류가 아니기 때문이다.
// 네트워크 오류만 error 로 나간다.
func (c *Client) Submit(ctx context.Context, r Reservation) (*SubmitResult, error) {
	if len(r.SlotTimes) == 0 {
		return nil, fmt.Errorf("선택한 시간이 없습니다")
	}
	body := map[string]any{
		"siteId":      SiteID,
		"category":    Category,
		"bldgCd":      r.BldgCd,
		"spaceCd":     r.SpaceCd,
		"reserveDate": r.ReserveDate,
		"slotTimes":   r.SlotTimes,
		"title":       r.Title,
		"purposeCd":   r.PurposeCd,
		"purposeEtc":  r.PurposeEtc,
	}
	started := time.Now()
	// reserveId 는 UUID 문자열이다(숫자가 아니다).
	var data struct {
		ReserveID string `json:"reserveId"`
	}
	env, err := c.call(ctx, http.MethodPost, "/api/cms/member/reservation", body, &data)
	elapsed := time.Since(started)
	if err != nil {
		return nil, err
	}
	return &SubmitResult{
		Status:    env.status,
		Code:      env.Code,
		Message:   strings.TrimSpace(env.Message),
		ReserveID: data.ReserveID,
		Elapsed:   elapsed,
	}, nil
}

// MyReservation 은 내 예약 한 건이다.
type MyReservation struct {
	ReserveID   string `json:"reserveId"`
	BldgCd      string `json:"bldgCd"`
	BldgNm      string `json:"bldgNm"`
	SpaceCd     string `json:"spaceCd"`
	SpaceNm     string `json:"spaceNm"`
	ReserveDate string `json:"reserveDate"`
	StartTime   string `json:"startTime"`
	EndTime     string `json:"endTime"`
	Title       string `json:"title"`
	Status      string `json:"status"` // PENDING|APPROVED|REJECTED|CANCELED
	CancelKind  string `json:"cancelKind"`
}

// Live 는 살아 있는 예약(신청/승인)인지다. 취소·반려는 자리를 차지하지 않는다.
func (m MyReservation) Live() bool { return m.Status == "PENDING" || m.Status == "APPROVED" }

// MyReservations 는 내 예약 목록이다. from/to 는 "YYYY-MM-DD", 비우면 전체.
func (c *Client) MyReservations(ctx context.Context, from, to string) ([]MyReservation, error) {
	kv := []string{"category", Category}
	if from != "" {
		kv = append(kv, "dateFrom", from)
	}
	if to != "" {
		kv = append(kv, "dateTo", to)
	}
	var out []MyReservation
	err := c.do(ctx, http.MethodGet, c.q("/api/cms/member/reservation/my", kv...), nil, &out)
	return out, err
}

// ── 서버 시계 ──────────────────────────────────────────────────────────────

// ServerTime 은 응답의 Date 헤더를 돌려준다(1초 해상도).
func (c *Client) ServerTime(ctx context.Context) (time.Time, error) {
	t, _, err := c.dateSample(ctx)
	return t, err
}

func (c *Client) dateSample(ctx context.Context) (time.Time, time.Time, error) {
	req, err := c.newRequest(ctx, http.MethodHead, c.base+"/", nil)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	t0 := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	mid := t0.Add(time.Since(t0) / 2)
	defer drain(resp)
	d, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("Date 헤더를 읽을 수 없음")
	}
	return d, mid, nil
}

// ClockOffset 은 대상 사이트의 시계가 우리보다 얼마나 앞서(+)/뒤쳐져(-) 있는지 잰다.
//
// 예약 창(오늘~+7일)은 사이트 서버가 자기 시계로 계산하므로, 새 날짜가 열리는 순간은
// 우리 자정이 아니라 '그쪽 자정'이다. 발사 시각은 이 실측값을 보고 정한다.
//
// Date 헤더는 1초 해상도라, 값이 바뀌는 '전환 순간'을 포착해 정밀도를 왕복지연 수준으로 끌어올린다.
// 전환을 못 잡으면 마지막 샘플로 거칠게(±0.5초) 추정한다.
func (c *Client) ClockOffset(ctx context.Context) (time.Duration, error) {
	prev, prevMid, err := c.dateSample(ctx)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
		d, mid, err := c.dateSample(ctx)
		if err != nil {
			continue
		}
		if d.After(prev) {
			boundary := prevMid.Add(mid.Sub(prevMid) / 2)
			return d.Sub(boundary), nil
		}
		prev, prevMid = d, mid
	}
	return prev.Add(500 * time.Millisecond).Sub(prevMid), nil
}

// ── 내부 ───────────────────────────────────────────────────────────────────

// q 는 경로에 siteId 와 주어진 key/value 쌍을 붙인다.
func (c *Client) q(path string, kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	v.Set("siteId", fmt.Sprint(SiteID))
	return path + "?" + v.Encode()
}

// envelope 는 모든 API 가 공유하는 응답 봉투다.
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	status  int
}

// do 는 call 의 얇은 껍데기다 — code != 200 을 에러로 바꾼다.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	env, err := c.call(ctx, method, path, in, out)
	if err != nil {
		return err
	}
	if env.Code != 200 {
		if msg := strings.TrimSpace(env.Message); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("jointips API code %d (HTTP %d)", env.Code, env.status)
	}
	return nil
}

// call 은 요청을 보내고 봉투를 돌려준다. out 이 있고 code==200 이면 data 를 언마샬한다.
func (c *Client) call(ctx context.Context, method, path string, in, out any) (*envelope, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	// 서버가 슬라이딩 세션으로 새 토큰을 내려주면 갈아끼운다.
	if nt := resp.Header.Get("X-Member-Token"); nt != "" {
		c.mu.Lock()
		c.token = nt
		c.mu.Unlock()
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
		return nil, fmt.Errorf("세션이 만료됐습니다 (로그인 필요)")
	}
	env := envelope{status: resp.StatusCode}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("응답을 읽을 수 없습니다 (HTTP %d): %s", resp.StatusCode, head(string(raw), 200))
	}
	if out != nil && env.Code == 200 && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return nil, fmt.Errorf("응답 data 해석 실패: %w", err)
		}
	}
	return &env, nil
}

func (c *Client) newRequest(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9")
	req.Header.Set("Referer", c.base+"/")
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return req, nil
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// Raw 는 임의의 경로를 같은 인증으로 호출한다. 조사 도구 전용이다.
func (c *Client) Raw(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}
