package jointips

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	BoardTable = "campus_share_apply"
	ApplyKind  = "입주사전용" // wr_22
	SpaceKind  = "회의공간"  // 조회의 wr_4
)

// Client 는 하나의 로그인 세션이다. 쿠키 자를 반드시 들고 다닌다(§3.0).
type Client struct {
	base string
	ua   string
	hc   *http.Client

	mu   sync.Mutex
	uids []string // 미리 확보해 둔 uid 풀(§3.2)
}

// New 는 쿠키 자를 붙인 클라이언트를 만든다. keep-alive 를 유지해
// 발사 시점에 TLS 핸드셰이크가 남아 있지 않게 한다(§2).
func New(baseURL, userAgent string, timeout time.Duration) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
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
		hc:   &http.Client{Timeout: timeout, Transport: tr, Jar: jar},
	}, nil
}

// ServerTime 은 응답의 Date 헤더를 돌려준다(1초 해상도).
func (c *Client) ServerTime(ctx context.Context) (time.Time, error) {
	t, _, err := c.dateSample(ctx)
	return t, err
}

// dateSample 은 Date 헤더와, 그 값이 관측된 로컬 시각(왕복의 중간점)을 함께 돌려준다.
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
// 이 값이 왜 필요한가: 7일 예약 창은 **사이트 서버가 자기 시계로** 계산한다(§3.9).
// 따라서 새 날짜가 열리는 순간은 우리 자정이 아니라 '그쪽 자정'이다.
// 실측 결과 이 사이트의 시계는 수십 분 어긋나 있을 수 있으므로, 발사 시각은
// 반드시 그쪽 시계 기준으로 환산해야 한다.
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
			// 서버 시계가 방금 d(정확히 d.000)로 넘어갔다. 그 경계는 [prevMid, mid] 사이에 있다.
			boundary := prevMid.Add(mid.Sub(prevMid) / 2)
			return d.Sub(boundary), nil
		}
		prev, prevMid = d, mid
	}
	// 전환 미포착: Date 는 '그 초의 시작'이므로 평균 0.5초를 보정한다.
	return prev.Add(500 * time.Millisecond).Sub(prevMid), nil
}

// Login 은 로그인하고, 예약 페이지에서 uid 가 보이는지로 성공을 확인한다(§3.1).
// 실패해도 재시도하지 않는다 — 계정 잠금과 불필요한 부하를 피한다.
func (c *Client) Login(ctx context.Context, username, password string) error {
	form := url.Values{
		"url":         {"/"},
		"mb_id":       {username},
		"mb_password": {password},
	}
	body, _, err := c.post(ctx, "/bbs/login_check.php", form)
	if err != nil {
		return fmt.Errorf("로그인 요청 실패: %w", err)
	}
	if msg := alertMessage(body); msg != "" {
		return fmt.Errorf("로그인 거부됨: %s", msg)
	}
	// 확실한 판정: 예약 페이지에 uid 가 있는가.
	uid, err := c.FetchUID(ctx)
	if err != nil {
		return fmt.Errorf("로그인 확인 실패 (아이디/비밀번호를 확인하세요): %w", err)
	}
	c.mu.Lock()
	c.uids = append(c.uids, uid)
	c.mu.Unlock()
	return nil
}

var uidRe = regexp.MustCompile(`name=["']uid["'][^>]*value=["']([^"']+)["']|value=["']([^"']+)["'][^>]*name=["']uid["']`)

// FetchUID 는 예약 페이지를 GET 해서 중복 제출 방지값 uid 를 하나 얻는다(§3.2).
func (c *Client) FetchUID(ctx context.Context) (string, error) {
	u := fmt.Sprintf("%s/bbs/write.php?bo_table=%s&wr_22=%s",
		c.base, BoardTable, url.QueryEscape(ApplyKind))
	req, err := c.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer drain(resp)
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	m := uidRe.FindStringSubmatch(string(b))
	if m == nil {
		return "", fmt.Errorf("예약 페이지에서 uid 를 찾지 못했습니다 (로그인이 풀렸거나 사이트 구조가 바뀜)")
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}

// FillUIDPool 은 풀이 n 개가 되도록 uid 를 채운다. 재시도마다 새 uid 를 쓰기 위한 것이다(§3.2).
func (c *Client) FillUIDPool(ctx context.Context, n int) (int, error) {
	for {
		c.mu.Lock()
		have := len(c.uids)
		c.mu.Unlock()
		if have >= n {
			return have, nil
		}
		uid, err := c.FetchUID(ctx)
		if err != nil {
			return have, err
		}
		c.mu.Lock()
		c.uids = append(c.uids, uid)
		c.mu.Unlock()
	}
}

// NextUID 는 풀에서 uid 를 하나 꺼낸다. 비었으면 페이지를 다시 GET 한다(1 왕복 추가).
func (c *Client) NextUID(ctx context.Context) (string, error) {
	c.mu.Lock()
	if len(c.uids) > 0 {
		uid := c.uids[0]
		c.uids = c.uids[1:]
		c.mu.Unlock()
		return uid, nil
	}
	c.mu.Unlock()
	return c.FetchUID(ctx)
}

// UIDCount 는 남은 풀 크기다(로그용).
func (c *Client) UIDCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.uids)
}

// Query 는 빈 슬롯을 조회한다(§3.3). date 는 "YYYY.MM.DD", building 은 "" 이면 전체.
func (c *Client) Query(ctx context.Context, building, date string) ([]Row, error) {
	form := url.Values{
		"std":  {"location_select_new"},
		"wr_2": {building},
		"wr_3": {date},
		"wr_4": {SpaceKind},
	}
	body, _, err := c.post(ctx, "/ajax_common.php", form)
	if err != nil {
		return nil, err
	}
	rows, err := ParseRows(body)
	if err != nil {
		return nil, fmt.Errorf("조회 응답 파싱 실패: %w", err)
	}
	return rows, nil
}

// Payload 는 write_update.php 로 보낼 예약 신청 한 건이다(§3.5).
type Payload struct {
	Date     string // wr_3, "YYYY.MM.DD"
	Start    string // wr_4, "HH:MM"
	Slots    int    // 30분 단위 길이 → wr_5 = Start + Slots×30분
	Building string // wr_6
	Floor    string // wr_7
	RoomID   string // wr_8
	Contact  string // wr_15
	Manager  string // wr_17
	Subject  string // wr_subject
	Content  string // wr_content
}

// Form 은 페이로드를 실제 전송 폼으로 펼친다. uid 는 매 시도 새 값을 받는다.
func (p Payload) Form(uid string) (url.Values, error) {
	end, err := FormEndTime(p.Start, p.Slots)
	if err != nil {
		return nil, err
	}
	content := p.Content
	if content == "" {
		content = "회의할 내용"
	}
	return url.Values{
		"uid":      {uid},
		"w":        {""},
		"bo_table": {BoardTable},
		"wr_id":    {"0"},
		// 검색 상태 유지용 빈 값들 — 폼에 있는 그대로 함께 보낸다
		"sca": {""}, "sfl": {""}, "stx": {""}, "spt": {""},
		"sst": {""}, "sod": {""}, "page": {""},
		"wr_1":       {"T"},
		"wr_2":       {"회의실이용신청"},
		"wr_3":       {p.Date},
		"wr_4":       {p.Start},
		"wr_5":       {end}, // ★ 배타적 종료시각
		"wr_6":       {p.Building},
		"wr_7":       {p.Floor},
		"wr_8":       {p.RoomID},
		"wr_15":      {p.Contact},
		"wr_17":      {p.Manager},
		"wr_19":      {"승인"},
		"wr_22":      {ApplyKind},
		"wr_subject": {p.Subject},
		"wr_content": {content},
	}, nil
}

// SubmitResult 는 신청 1회의 결과다. 확정 판정은 재조회(§3.6 2차)로 한다.
type SubmitResult struct {
	Status  int
	Alert   string // 응답에서 뽑아낸 alert 메시지(있으면 실패로 본다)
	Body    string // 앞 500자만 (debug 로그용)
	Elapsed time.Duration
}

// Submit 은 예약을 신청한다(§3.5).
//
// 그누보드는 302 대신 <script>location.href=...</script> 를 돌려주기도 하므로
// HTTP 상태코드만으로 성공/실패를 판정하지 않는다(§3.0).
func (c *Client) Submit(ctx context.Context, uid string, p Payload) (*SubmitResult, error) {
	form, err := p.Form(uid)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	body, status, err := c.post(ctx, "/bbs/write_update.php", form)
	elapsed := time.Since(started)
	if err != nil {
		return nil, err
	}
	return &SubmitResult{
		Status:  status,
		Alert:   alertMessage(body),
		Body:    head(body, 500),
		Elapsed: elapsed,
	}, nil
}

// DupTime 은 겹침 확인 API 다(§3.7). 반환값 true = "겹침 있음(예약 불가)".
//
// 함정 두 개: 여기서 wr_5 는 마지막 슬롯의 시작시각(포함)이고,
// 응답 "OK" 는 예약 불가를 뜻한다(의미 반전).
func (c *Client) DupTime(ctx context.Context, p Payload) (bool, error) {
	end, err := DupEndTime(p.Start, p.Slots)
	if err != nil {
		return false, err
	}
	form := url.Values{
		"std": {"dup_time"}, "wr_3": {p.Date}, "wr_4": {p.Start},
		"wr_5": {end}, "wr_8": {p.RoomID}, "wr_id": {"0"},
	}
	body, _, err := c.post(ctx, "/ajax_common.php", form)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(body) == "OK", nil
}

// ── 내부 ───────────────────────────────────────────────────────────────────

func (c *Client) newRequest(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9")
	req.Header.Set("Referer", c.base+"/")
	return req, nil
}

func (c *Client) post(ctx context.Context, path string, form url.Values) (string, int, error) {
	req, err := c.newRequest(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer drain(resp)
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(b), resp.StatusCode, nil
}

var alertRe = regexp.MustCompile(`alert\s*\(\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")\s*\)`)

// alertMessage 는 그누보드가 오류를 표시하는 alert('...') 에서 메시지를 뽑는다(§3.6 1차 판정).
func alertMessage(body string) string {
	m := alertRe.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	msg := m[1]
	if msg == "" {
		msg = m[2]
	}
	msg = strings.NewReplacer(`\'`, "'", `\"`, `"`, `\\`, `\`, `\n`, " ").Replace(msg)
	return strings.TrimSpace(msg)
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

// HTTPClient 는 로그인 세션(쿠키)이 붙은 원본 클라이언트를 돌려준다.
// 조사 도구가 임의의 요청을 같은 세션으로 보내야 할 때만 쓴다.
func (c *Client) HTTPClient() *http.Client { return c.hc }
