// Package notion 은 예약 결과를 Notion 데이터베이스에 한 행으로 남긴다(§7).
// 알림은 만들지 않는다. DB 컬럼 구성은 사용자가 바꿀 수 있으므로 속성 이름을
// 정규식으로 매칭해서 역할을 정한다.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	apiBase = "https://api.notion.com/v1"
	version = "2022-06-28"
)

type Client struct {
	token string
	base  string // 비어 있으면 apiBase
	hc    *http.Client

	mu      sync.Mutex
	users   map[string]string // 사용자 이름·이메일 → id
	sources int               // 이미 다 써 본 조회처 개수
}

func New(token string) *Client { return NewAt(token, "") }

// NewAt 는 API 주소를 바꿔 끼운다. 테스트에서 가짜 Notion 을 세울 때 쓴다.
func NewAt(token, base string) *Client {
	return &Client{token: token, base: base, hc: &http.Client{Timeout: 15 * time.Second}}
}

// Property 는 DB 스키마의 속성 하나다.
type Property struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Schema 는 역할별로 매칭된 속성 이름 모음이다. 비어 있으면 그 속성은 생략한다.
type Schema struct {
	Title   string            `json:"title"`
	Usage   string            `json:"usage"`  // "회의실 사용 현황"
	Room    string            `json:"room"`   // "회의실" / "장소"
	Period  string            `json:"period"` // "시간" (date 타입이 아닌 것)
	Booker  string            `json:"booker"` // "예약자" / "담당"
	Date    string            `json:"date"`   // date 타입
	Types   map[string]string `json:"types"`
	DBTitle string            `json:"db_title"`
	All     []Property        `json:"all"`
}

var (
	reUsage  = regexp.MustCompile(`현황`)
	reRoom   = regexp.MustCompile(`회의실|장소`)
	rePeriod = regexp.MustCompile(`시간`)
	reBooker = regexp.MustCompile(`예약자|담당`)
	reDate   = regexp.MustCompile(`예약|날짜|일자|[Dd]ate`)
)

// FetchSchema 는 DB 스키마를 읽고 속성 이름 패턴으로 역할을 매칭한다(§7.2).
//
// 판정 순서가 중요하다: "회의실 사용 현황" 에는 "회의실"도 들어 있으므로
// 현황 → 회의실 → 시간 → 예약자 순으로 검사해야 오분류되지 않는다.
func (c *Client) FetchSchema(ctx context.Context, dbID string) (*Schema, error) {
	var out struct {
		Title []struct {
			PlainText string `json:"plain_text"`
		} `json:"title"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := c.do(ctx, http.MethodGet, "/databases/"+dbID, nil, &out); err != nil {
		return nil, err
	}
	s := &Schema{Types: map[string]string{}}
	for _, t := range out.Title {
		s.DBTitle += t.PlainText
	}
	for name, p := range out.Properties {
		s.All = append(s.All, Property{Name: name, Type: p.Type})
		s.Types[name] = p.Type
	}
	// 이름순으로 정렬해 매칭이 맵 순회 순서에 흔들리지 않게 한다.
	sortProps(s.All)

	for _, p := range s.All {
		switch {
		case p.Type == "title" && s.Title == "":
			s.Title = p.Name
		case p.Type == "date":
			if s.Date == "" || (reDate.MatchString(p.Name) && !reDate.MatchString(s.Date)) {
				s.Date = p.Name
			}
		}
	}
	for _, p := range s.All {
		if p.Type == "date" || p.Type == "title" {
			continue
		}
		switch {
		case s.Usage == "" && reUsage.MatchString(p.Name):
			s.Usage = p.Name
		case s.Room == "" && reRoom.MatchString(p.Name):
			s.Room = p.Name
		case s.Period == "" && rePeriod.MatchString(p.Name):
			s.Period = p.Name
		case s.Booker == "" && reBooker.MatchString(p.Name):
			s.Booker = p.Name
		}
	}
	// title 속성 이름 자체가 "회의실"/"장소" 인 DB 가 흔하다. 그런 DB 에서 제목 칸은
	// 회의명이 아니라 방 이름을 담는 자리이므로(기존 행들이 그렇게 쓰여 있다),
	// 방 역할을 title 에 준다. 회의명은 "현황" 줄 앞머리에 그대로 남는다.
	if s.Room == "" && s.Title != "" && reRoom.MatchString(s.Title) {
		s.Room = s.Title
	}
	return s, nil
}

// RoomIsTitle 은 방 이름이 제목 칸에 들어가는 배치인지 알려준다.
func (s *Schema) RoomIsTitle() bool { return s.Room != "" && s.Room == s.Title }

// Record 는 Notion 에 남길 예약 한 건이다.
type Record struct {
	Subject string // 회의명 (title)
	Usage   string // "• 주간 정기 회의 12:00~16:00"
	Room    string // "현승 4A"
	Period  string // "12:00~16:00"
	Booker  string // 담당자명
	Date    string // "2026-08-25" — 시간은 넣지 않는다
}

// Page 는 만들어진 행의 최소 식별 정보다.
type Page struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreatePage 는 DB 에 행 하나를 만든다(§7.5). 속성 타입에 맞춰 페이로드를 만들고,
// relation/rollup/formula/files 는 전송하지 않는다. people 은 워크스페이스에서
// 이름이 같은 사람을 찾아 넣고, 못 찾으면 조용히 건너뛴다.
func (c *Client) CreatePage(ctx context.Context, dbID string, s *Schema, r Record) (Page, error) {
	props := c.properties(ctx, dbID, s, r)
	body := map[string]any{
		"parent":     map[string]any{"database_id": dbID},
		"properties": props,
	}
	var out Page
	if err := c.do(ctx, http.MethodPost, "/pages", body, &out); err != nil {
		return Page{}, err
	}
	return out, nil
}

// properties 는 Record 를 DB 스키마에 맞는 속성 페이로드로 옮긴다.
func (c *Client) properties(ctx context.Context, dbID string, s *Schema, r Record) map[string]any {
	props := map[string]any{}
	set := func(name, value string) {
		if name == "" || value == "" {
			return
		}
		if p := c.valueFor(ctx, dbID, s, s.Types[name], value); p != nil {
			props[name] = p
		}
	}
	// 제목 칸: 방 이름을 담는 배치면 방 이름을, 아니면 회의명을 넣는다.
	if s.Title != "" {
		title := r.Subject
		if s.RoomIsTitle() {
			title = r.Room
		}
		if title != "" {
			props[s.Title] = map[string]any{"title": textRuns(title)}
		}
	}
	set(s.Usage, r.Usage)
	if !s.RoomIsTitle() {
		set(s.Room, r.Room)
	}
	set(s.Period, r.Period)
	set(s.Booker, r.Booker)
	if s.Date != "" && r.Date != "" {
		props[s.Date] = map[string]any{"date": map[string]any{"start": r.Date}}
	}
	return props
}

// Archive 는 행을 휴지통으로 보낸다. 등록 테스트가 뒤처리에 쓴다.
func (c *Client) Archive(ctx context.Context, pageID string) error {
	return c.do(ctx, http.MethodPatch, "/pages/"+pageID, map[string]any{"archived": true}, nil)
}

// ReadRow 는 행 하나를 "속성 이름 → 사람이 읽을 수 있는 값" 으로 되읽는다.
// 우리가 보낸 값이 Notion 에 그대로 남았는지 눈으로 확인하는 용도다.
func (c *Client) ReadRow(ctx context.Context, pageID string) (map[string]string, error) {
	var out struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := c.do(ctx, http.MethodGet, "/pages/"+pageID, nil, &out); err != nil {
		return nil, err
	}
	res := map[string]string{}
	for name, raw := range out.Properties {
		res[name] = renderValue(raw)
	}
	return res, nil
}

// CountRowsOn 은 대상 날짜에 이미 몇 건이 기록돼 있는지 센다(§7.6 멱등성).
// 하루 여러 건을 잡을 수 있으므로 "있다/없다"가 아니라 건수가 필요하다.
func (c *Client) CountRowsOn(ctx context.Context, dbID string, s *Schema, date string) (int, error) {
	if s.Date == "" {
		return 0, fmt.Errorf("날짜 속성을 찾지 못해 멱등성 확인을 건너뜁니다")
	}
	next, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, err
	}
	body := map[string]any{
		"page_size": 100,
		"filter": map[string]any{"and": []any{
			map[string]any{"property": s.Date, "date": map[string]any{"on_or_after": date}},
			map[string]any{"property": s.Date, "date": map[string]any{"before": next.AddDate(0, 0, 1).Format("2006-01-02")}},
		}},
	}
	var out struct {
		Results []struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/databases/"+dbID+"/query", body, &out); err != nil {
		return 0, err
	}
	return len(out.Results), nil
}

// ── 내부 ───────────────────────────────────────────────────────────────────

// valueFor 는 valuePayload 에 people 처리를 얹은 것이다. people 속성에는 이름
// 문자열을 넣을 수 없고 사용자 id 가 필요해서, 이름 → id 를 한 번 찾아 둔다.
func (c *Client) valueFor(ctx context.Context, dbID string, s *Schema, typ, value string) map[string]any {
	if typ == "people" {
		if id := c.userID(ctx, dbID, s, value); id != "" {
			return map[string]any{"people": []any{map[string]any{"object": "user", "id": id}}}
		}
		return nil
	}
	return valuePayload(typ, value)
}

// userID 는 이름으로 워크스페이스 사용자 id 를 찾는다. 못 찾으면 빈 문자열이고,
// 그러면 그 속성만 건너뛴다 — 기록 자체를 실패시키지는 않는다.
//
// 찾는 곳이 세 군데인 이유: 내부 통합 토큰(ntn_…)은 /users 목록 조회가 아예
// 막혀 있다("Personal access tokens cannot list users"). 그래서 토큰 소유자와,
// DB 에 이미 쌓인 행들의 people 값에서 이름-id 쌍을 주워 모은다.
func (c *Client) userID(ctx context.Context, dbID string, s *Schema, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.users == nil {
		c.users = map[string]string{}
	}
	// 싼 것부터 하나씩 열어 보고, 찾는 이름이 나오면 거기서 멈춘다.
	// 한 번 열어 본 조회처는 다시 열지 않는다.
	sources := []func(){
		func() { c.collectFromTokenOwner(ctx) },
		func() { c.collectFromUserList(ctx) },
		func() { c.collectFromExistingRows(ctx, dbID, s) },
	}
	for {
		if id, ok := c.users[name]; ok {
			return id
		}
		if id, ok := c.users[strings.ToLower(name)]; ok {
			return id
		}
		if c.sources >= len(sources) {
			return ""
		}
		sources[c.sources]()
		c.sources++
	}
}

func (c *Client) put(name, id string) {
	if name = strings.TrimSpace(name); name == "" || id == "" {
		return
	}
	if _, ok := c.users[name]; !ok {
		c.users[name] = id
	}
}

// collectFromUserList 는 OAuth 통합에서만 통하는 정식 경로다.
func (c *Client) collectFromUserList(ctx context.Context) {
	var cursor string
	for page := 0; page < 10; page++ {
		path := "/users?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + cursor
		}
		var out struct {
			Results []struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Type   string `json:"type"`
				Person struct {
					Email string `json:"email"`
				} `json:"person"`
			} `json:"results"`
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
			return
		}
		for _, u := range out.Results {
			if u.Type != "person" {
				continue
			}
			c.put(u.Name, u.ID)
			c.put(strings.ToLower(u.Person.Email), u.ID)
		}
		if !out.HasMore || out.NextCursor == "" {
			return
		}
		cursor = out.NextCursor
	}
}

// collectFromTokenOwner 는 토큰을 만든 사람 한 명을 얻는다. 담당자가 보통
// 이 사람이라 적중률이 높다.
func (c *Client) collectFromTokenOwner(ctx context.Context) {
	var me struct {
		Bot struct {
			Owner struct {
				User struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
					Person struct {
						Email string `json:"email"`
					} `json:"person"`
				} `json:"user"`
			} `json:"owner"`
		} `json:"bot"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/me", nil, &me); err != nil {
		return
	}
	u := me.Bot.Owner.User
	c.put(u.Name, u.ID)
	c.put(strings.ToLower(u.Person.Email), u.ID)
}

// collectFromExistingRows 는 DB 에 이미 있는 행들의 people 값에서 이름-id 쌍을
// 줍는다. 사용자 목록 조회가 막힌 토큰에서도 통하는 유일한 경로다.
func (c *Client) collectFromExistingRows(ctx context.Context, dbID string, s *Schema) {
	if dbID == "" || s == nil || s.Booker == "" || s.Types[s.Booker] != "people" {
		return
	}
	var out struct {
		Results []struct {
			Properties map[string]struct {
				People []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"people"`
			} `json:"properties"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/databases/"+dbID+"/query",
		map[string]any{"page_size": 100}, &out); err != nil {
		return
	}
	for _, row := range out.Results {
		for _, p := range row.Properties[s.Booker].People {
			if p.Type == "bot" {
				continue
			}
			c.put(p.Name, p.ID)
		}
	}
}

// renderValue 는 되읽은 속성값 하나를 한 줄 문자열로 만든다(등록 테스트 표시용).
func renderValue(raw json.RawMessage) string {
	var v struct {
		Type     string `json:"type"`
		Title    []run  `json:"title"`
		RichText []run  `json:"rich_text"`
		Date     *struct {
			Start string `json:"start"`
		} `json:"date"`
		People []struct {
			Name string `json:"name"`
		} `json:"people"`
		Select *struct {
			Name string `json:"name"`
		} `json:"select"`
		Status *struct {
			Name string `json:"name"`
		} `json:"status"`
		Checkbox bool   `json:"checkbox"`
		URL      string `json:"url"`
		Email    string `json:"email"`
		Phone    string `json:"phone_number"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	switch v.Type {
	case "title":
		return joinRuns(v.Title)
	case "rich_text":
		return joinRuns(v.RichText)
	case "date":
		if v.Date != nil {
			return v.Date.Start
		}
	case "people":
		names := make([]string, 0, len(v.People))
		for _, p := range v.People {
			names = append(names, p.Name)
		}
		return strings.Join(names, ", ")
	case "select":
		if v.Select != nil {
			return v.Select.Name
		}
	case "status":
		if v.Status != nil {
			return v.Status.Name
		}
	case "checkbox":
		if v.Checkbox {
			return "예"
		}
		return "아니오"
	case "url":
		return v.URL
	case "email":
		return v.Email
	case "phone_number":
		return v.Phone
	}
	return ""
}

type run struct {
	PlainText string `json:"plain_text"`
}

func joinRuns(rs []run) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.PlainText)
	}
	return b.String()
}

func valuePayload(typ, value string) map[string]any {
	switch typ {
	case "rich_text", "":
		return map[string]any{"rich_text": textRuns(value)}
	case "title":
		return map[string]any{"title": textRuns(value)}
	case "select":
		return map[string]any{"select": map[string]any{"name": value}}
	case "status":
		return map[string]any{"status": map[string]any{"name": value}}
	case "url":
		return map[string]any{"url": value}
	case "email":
		return map[string]any{"email": value}
	case "phone_number":
		return map[string]any{"phone_number": value}
	case "checkbox":
		return map[string]any{"checkbox": value != ""}
	default:
		// relation / rollup / formula / files 등은 전송하지 않는다
		// (people 은 valueFor 가 따로 처리한다)
		return nil
	}
}

func textRuns(s string) []any {
	return []any{map[string]any{"text": map[string]any{"content": s}}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	base := c.base
	if base == "" {
		base = apiBase
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", version)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &e)
		switch e.Code {
		case "unauthorized":
			return fmt.Errorf("Notion 토큰이 유효하지 않습니다")
		case "object_not_found":
			return fmt.Errorf("데이터베이스를 찾을 수 없습니다 — DB 에 통합(integration)을 연결했는지 확인하세요")
		}
		if e.Message != "" {
			return fmt.Errorf("Notion API %d: %s", resp.StatusCode, e.Message)
		}
		return fmt.Errorf("Notion API %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func sortProps(ps []Property) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].Name < ps[j-1].Name; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}
