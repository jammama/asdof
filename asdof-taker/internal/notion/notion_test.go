package notion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 실제 DB("회의실 예약 현황")의 스키마. title 속성 이름이 "회의실" 이라
// 방 이름이 갈 곳이 제목 칸밖에 없는 배치다.
func realSchemaServer(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var created []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/databases/db1":
			json.NewEncoder(w).Encode(map[string]any{
				"title": []any{map[string]any{"plain_text": "회의실 예약 현황"}},
				"properties": map[string]any{
					"회의실":       map[string]any{"type": "title"},
					"회의실 사용 현황": map[string]any{"type": "rich_text"},
					"예약시간":      map[string]any{"type": "rich_text"},
					"예약자":       map[string]any{"type": "people"},
					"날짜":        map[string]any{"type": "date"},
					"비고":        map[string]any{"type": "rich_text"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/users/me":
			// 내부 통합 토큰. 소유자는 담당자와 다른 사람이다.
			json.NewEncoder(w).Encode(map[string]any{
				"bot": map[string]any{"owner": map[string]any{"user": map[string]any{
					"id": "u-owner", "name": "다른 사람"}}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/users":
			json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"id": "u-1", "name": "이승혜", "type": "person",
						"person": map[string]any{"email": "shlee02911@amberroad.ai"}},
					map[string]any{"id": "bot-1", "name": "회의실 예약 페이지 연결", "type": "bot"},
				},
				"has_more": false,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/pages":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body)
			json.NewEncoder(w).Encode(map[string]any{"id": "p-1", "url": "https://notion.so/p-1"})
		default:
			t.Errorf("예상 못한 요청: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &created
}

func newTestClient(base string) *Client { return NewAt("tok", base) }

func TestFetchSchemaTitleNamedRoom(t *testing.T) {
	srv, _ := realSchemaServer(t)
	s, err := newTestClient(srv.URL).FetchSchema(context.Background(), "db1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "회의실" {
		t.Errorf("Title = %q, want 회의실", s.Title)
	}
	// title 이름이 "회의실" 이므로 방 역할도 그 칸이 맡아야 한다.
	if s.Room != "회의실" || !s.RoomIsTitle() {
		t.Errorf("Room = %q RoomIsTitle=%v, want 회의실/true", s.Room, s.RoomIsTitle())
	}
	if s.Usage != "회의실 사용 현황" {
		t.Errorf("Usage = %q", s.Usage)
	}
	if s.Period != "예약시간" {
		t.Errorf("Period = %q", s.Period)
	}
	if s.Booker != "예약자" {
		t.Errorf("Booker = %q", s.Booker)
	}
	if s.Date != "날짜" {
		t.Errorf("Date = %q", s.Date)
	}
}

func TestCreatePagePutsRoomInTitleAndResolvesPeople(t *testing.T) {
	srv, created := realSchemaServer(t)
	c := newTestClient(srv.URL)
	ctx := context.Background()
	s, err := c.FetchSchema(ctx, "db1")
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.CreatePage(ctx, "db1", s, Record{
		Subject: "기획 회의",
		Usage:   "• 기획 회의 13:30~17:30",
		Room:    "현승 5A",
		Period:  "13:30~17:30",
		Booker:  "이승혜",
		Date:    "2026-09-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "https://notion.so/p-1" {
		t.Errorf("URL = %q", page.URL)
	}
	if len(*created) != 1 {
		t.Fatalf("페이지 %d개 생성", len(*created))
	}
	props := (*created)[0]["properties"].(map[string]any)

	// 제목 칸에는 회의명이 아니라 방 이름이 들어가야 한다.
	if got := plainOf(props["회의실"], "title"); got != "현승 5A" {
		t.Errorf("제목 = %q, want 현승 5A", got)
	}
	// 회의명은 현황 줄에 남는다.
	if got := plainOf(props["회의실 사용 현황"], "rich_text"); got != "• 기획 회의 13:30~17:30" {
		t.Errorf("현황 = %q", got)
	}
	if got := plainOf(props["예약시간"], "rich_text"); got != "13:30~17:30" {
		t.Errorf("예약시간 = %q", got)
	}
	// people 은 이름 → 사용자 id 로 바뀌어 들어가야 한다.
	people := props["예약자"].(map[string]any)["people"].([]any)
	if len(people) != 1 || people[0].(map[string]any)["id"] != "u-1" {
		t.Errorf("예약자 = %v", people)
	}
	if props["날짜"].(map[string]any)["date"].(map[string]any)["start"] != "2026-09-07" {
		t.Errorf("날짜 = %v", props["날짜"])
	}
}

// 제목 속성 이름이 방과 무관하면 예전처럼 회의명이 제목으로 간다.
func TestCreatePageKeepsSubjectInNeutralTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/databases/db1" {
			json.NewEncoder(w).Encode(map[string]any{
				"title": []any{map[string]any{"plain_text": "예약"}},
				"properties": map[string]any{
					"이름": map[string]any{"type": "title"},
					"장소": map[string]any{"type": "rich_text"},
					"날짜": map[string]any{"type": "date"},
				},
			})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		props := body["properties"].(map[string]any)
		if got := plainOf(props["이름"], "title"); got != "기획 회의" {
			t.Errorf("제목 = %q, want 기획 회의", got)
		}
		if got := plainOf(props["장소"], "rich_text"); got != "현승 5A" {
			t.Errorf("장소 = %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "p-2", "url": "u"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := context.Background()
	s, err := c.FetchSchema(ctx, "db1")
	if err != nil {
		t.Fatal(err)
	}
	if s.RoomIsTitle() {
		t.Fatal("장소 속성이 따로 있으면 제목이 방 역할을 맡으면 안 된다")
	}
	if _, err := c.CreatePage(ctx, "db1", s, Record{
		Subject: "기획 회의", Room: "현승 5A", Date: "2026-09-07",
	}); err != nil {
		t.Fatal(err)
	}
}

func plainOf(v any, typ string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	runs, ok := m[typ].([]any)
	if !ok {
		return ""
	}
	out := ""
	for _, r := range runs {
		out += r.(map[string]any)["text"].(map[string]any)["content"].(string)
	}
	return out
}

// 내부 통합 토큰은 사용자 목록 조회가 막혀 있다. 그때는 DB 에 이미 있는 행의
// people 값에서 이름-id 를 주워야 담당자가 들어간다.
func TestPeopleResolvedFromExistingRowsWhenUserListBlocked(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/databases/db1" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{
				"properties": map[string]any{
					"회의실": map[string]any{"type": "title"},
					"예약자": map[string]any{"type": "people"},
				},
			})
		case r.URL.Path == "/users/me":
			json.NewEncoder(w).Encode(map[string]any{
				"bot": map[string]any{"owner": map[string]any{"user": map[string]any{
					"id": "u-owner", "name": "다른 사람"}}},
			})
		case r.URL.Path == "/users":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"restricted_resource","message":"Personal access tokens cannot list users."}`))
		case r.URL.Path == "/databases/db1/query":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{
				map[string]any{"properties": map[string]any{
					"예약자": map[string]any{"people": []any{
						map[string]any{"id": "u-1", "name": "이승혜", "type": "person"}}}}},
			}})
		case r.URL.Path == "/pages":
			json.NewDecoder(r.Body).Decode(&sent)
			json.NewEncoder(w).Encode(map[string]any{"id": "p", "url": "u"})
		default:
			t.Errorf("예상 못한 요청: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := context.Background()
	sc, err := c.FetchSchema(ctx, "db1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreatePage(ctx, "db1", sc, Record{Room: "현승 5A", Booker: "이승혜"}); err != nil {
		t.Fatal(err)
	}
	props := sent["properties"].(map[string]any)
	people, ok := props["예약자"].(map[string]any)
	if !ok {
		t.Fatalf("예약자가 안 들어갔다: %v", props)
	}
	if id := people["people"].([]any)[0].(map[string]any)["id"]; id != "u-1" {
		t.Errorf("예약자 id = %v, want u-1", id)
	}
}

// 이름을 못 찾으면 그 속성만 빠지고 기록은 성공해야 한다.
func TestPeopleSkippedWhenUnresolvable(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/databases/db1":
			json.NewEncoder(w).Encode(map[string]any{"properties": map[string]any{
				"회의실": map[string]any{"type": "title"},
				"예약자": map[string]any{"type": "people"},
			}})
		case "/pages":
			json.NewDecoder(r.Body).Decode(&sent)
			json.NewEncoder(w).Encode(map[string]any{"id": "p", "url": "u"})
		default:
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"code":"restricted_resource","message":"nope"}`))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ctx := context.Background()
	sc, err := c.FetchSchema(ctx, "db1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreatePage(ctx, "db1", sc, Record{Room: "현승 5A", Booker: "없는사람"}); err != nil {
		t.Fatal(err)
	}
	props := sent["properties"].(map[string]any)
	if _, ok := props["예약자"]; ok {
		t.Errorf("못 찾은 사람이 들어갔다: %v", props["예약자"])
	}
	if plainOf(props["회의실"], "title") != "현승 5A" {
		t.Errorf("나머지 값은 그대로 들어가야 한다: %v", props)
	}
}
