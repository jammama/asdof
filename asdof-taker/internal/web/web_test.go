package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"asdof-taker/internal/config"
	"asdof-taker/internal/runner"
)

func newServer(t *testing.T) (*httptest.Server, *config.Store) {
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
	hash, _ := config.HashPassword("admin-pass-123")
	if _, err := store.Update(func(c *config.Config) error {
		c.Admin.PasswordHash = hash
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	r := runner.New(store, log)
	s := New(store, r, runner.NewScheduler(store, r, log), log)
	s.notionBase = notionBase
	return httptest.NewServer(s.Handler()), store
}

// notionBase 는 테스트가 가짜 Notion 을 세울 때 채운다(newServer 보다 먼저).
var notionBase string

func post(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// 공개 도메인에 노출되므로 세션 없이는 어떤 API 도 열려선 안 된다.
func TestAPIsRequireAuth(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()
	client := srv.Client()

	for _, path := range []string{"/api/state"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, 401 이어야 함", path, resp.StatusCode)
		}
	}
	for _, path := range []string{
		"/api/config", "/api/secrets", "/api/admin/password",
		"/api/test/site", "/api/test/notion", "/api/test/notion/write",
		"/api/query", "/api/run", "/api/run/cancel",
	} {
		resp := post(t, client, srv.URL+path, map[string]any{})
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s = %d, 401 이어야 함", path, resp.StatusCode)
		}
	}
}

func TestLoginAndSecretsNeverEcho(t *testing.T) {
	srv, store := newServer(t)
	defer srv.Close()
	jar := newJarClient(t, srv)

	if resp := post(t, jar, srv.URL+"/api/login", map[string]string{"password": "wrong"}); resp.StatusCode != 401 {
		resp.Body.Close()
		t.Fatalf("틀린 비밀번호 = %d", resp.StatusCode)
	}
	resp := post(t, jar, srv.URL+"/api/login", map[string]string{"password": "admin-pass-123"})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("로그인 = %d", resp.StatusCode)
	}

	// 자격증명 저장
	resp = post(t, jar, srv.URL+"/api/secrets", map[string]any{
		"username": "tester", "password": "site-secret-pw", "notion_token": "ntn_topsecret",
	})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("자격증명 저장 = %d", resp.StatusCode)
	}

	// 저장은 됐는가 (복호화해서 확인)
	if pw, err := store.Password(); err != nil || pw != "site-secret-pw" {
		t.Fatalf("저장된 비밀번호 = %q %v", pw, err)
	}
	// 상태 응답에 비밀값이 절대 섞이면 안 된다
	sresp, err := jar.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	raw, err := io.ReadAll(sresp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, secret := range []string{"site-secret-pw", "ntn_topsecret", "password_enc", "token_enc", "password_hash"} {
		if strings.Contains(body, secret) {
			t.Errorf("/api/state 응답에 %q 가 들어 있다", secret)
		}
	}
	if !strings.Contains(body, `"password_set":true`) || !strings.Contains(body, `"token_set":true`) {
		t.Error("비밀값 설정 여부 플래그가 없다")
	}
}

func TestConfigValidationRejectsBadTarget(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()
	jar := newJarClient(t, srv)
	post(t, jar, srv.URL+"/api/login", map[string]string{"password": "admin-pass-123"}).Body.Close()

	resp := post(t, jar, srv.URL+"/api/config", map[string]any{
		"booking": map[string]any{
			"date_offset_days": 7, "subject": "회의",
			"targets": []any{map[string]any{
				"room_id": 41, "building": "3", "floor": "25", "start": "13:00", "duration_slots": 99,
			}},
			"fallback": map[string]any{"enabled": false},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("4시간 초과 대상을 받아들였다 (%d)", resp.StatusCode)
	}
}

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()
	a := newJarClient(t, srv)
	b := newJarClient(t, srv)
	post(t, a, srv.URL+"/api/login", map[string]string{"password": "admin-pass-123"}).Body.Close()
	post(t, b, srv.URL+"/api/login", map[string]string{"password": "admin-pass-123"}).Body.Close()

	resp := post(t, a, srv.URL+"/api/admin/password",
		map[string]string{"current": "admin-pass-123", "next": "brand-new-pass"})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("비밀번호 변경 = %d", resp.StatusCode)
	}
	// 바꾼 브라우저는 유지
	r1, err := a.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Body.Close()
	if r1.StatusCode != 200 {
		t.Errorf("변경한 세션이 끊겼다 (%d)", r1.StatusCode)
	}
	// 다른 브라우저는 끊긴다
	r2, err := b.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Errorf("다른 세션이 살아 있다 (%d)", r2.StatusCode)
	}
}

func TestLoginThrottle(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()
	c := newJarClient(t, srv)
	var last int
	for i := 0; i < 5; i++ {
		resp := post(t, c, srv.URL+"/api/login", map[string]string{"password": "nope"})
		last = resp.StatusCode
		resp.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("연속 실패 후 = %d, 429 여야 함", last)
	}
}

// newJarClient 는 매번 독립된 쿠키 자를 가진 클라이언트를 만든다.
// httptest.Server.Client() 는 같은 인스턴스를 돌려주므로 "다른 브라우저"를 흉내 낼 수 없다.
func newJarClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Transport: srv.Client().Transport}
}

// 부분 저장은 보내지 않은 필드를 지우면 안 된다.
// (필드를 새로 추가한 뒤 새로고침 전 화면에서 저장하자 gap_seconds 가 0 으로 밀린 적이 있다.)
func TestPartialSaveKeepsOmittedFields(t *testing.T) {
	srv, store := newServer(t)
	defer srv.Close()
	jar := newJarClient(t, srv)
	post(t, jar, srv.URL+"/api/login", map[string]string{"password": "admin-pass-123"}).Body.Close()

	// 먼저 온전한 예약 설정을 저장한다.
	full := map[string]any{
		"date_offset_days": 6, "count": 2, "gap_seconds": 35,
		"subject": "주간 정기 회의", "manager": "홍길동", "contact": "010-0000-0000",
		"targets": []any{
			map[string]any{"room_id": 41, "building": "3", "floor": "25", "start": "09:00", "duration_slots": 8},
			map[string]any{"room_id": 41, "building": "3", "floor": "25", "start": "14:00", "duration_slots": 8},
		},
		"fallback": map[string]any{"enabled": false},
	}
	resp := post(t, jar, srv.URL+"/api/config", map[string]any{"booking": full})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("최초 저장 = %d", resp.StatusCode)
	}

	// 이제 '옛 화면'을 흉내 내 gap_seconds 없이 저장한다.
	old := map[string]any{}
	for k, v := range full {
		old[k] = v
	}
	delete(old, "gap_seconds")
	old["subject"] = "기획 회의"
	resp = post(t, jar, srv.URL+"/api/config", map[string]any{"booking": old})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("부분 저장 = %d", resp.StatusCode)
	}

	got := store.Get().Booking
	if got.Subject != "기획 회의" {
		t.Errorf("보낸 필드는 갱신돼야 한다: subject = %q", got.Subject)
	}
	if got.GapSeconds != 35 {
		t.Errorf("보내지 않은 필드가 지워졌다: gap_seconds = %d, want 35", got.GapSeconds)
	}
	if got.Count != 2 || len(got.Targets) != 2 {
		t.Errorf("나머지도 유지돼야 한다: count=%d targets=%d", got.Count, len(got.Targets))
	}
}

// 등록 테스트는 실제로 행을 만들고, 되읽고, 기본적으로 휴지통으로 되돌려야 한다.
func TestNotionWriteTest(t *testing.T) {
	var created, archived bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/databases/0123456789abcdef0123456789abcdef" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{
				"title": []any{map[string]any{"plain_text": "회의실 예약 현황"}},
				"properties": map[string]any{
					"회의실":       map[string]any{"type": "title"},
					"회의실 사용 현황": map[string]any{"type": "rich_text"},
					"예약시간":      map[string]any{"type": "rich_text"},
					"날짜":        map[string]any{"type": "date"},
				},
			})
		case r.URL.Path == "/pages" && r.Method == http.MethodPost:
			created = true
			json.NewEncoder(w).Encode(map[string]any{"id": "pg1", "url": "https://notion.so/pg1"})
		case r.URL.Path == "/pages/pg1" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"properties": map[string]any{
				"회의실":  map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": "현승 5A"}}},
				"예약시간": map[string]any{"type": "rich_text", "rich_text": []any{map[string]any{"plain_text": "13:30~17:30"}}},
			}})
		case r.URL.Path == "/pages/pg1" && r.Method == http.MethodPatch:
			archived = true
			json.NewEncoder(w).Encode(map[string]any{"id": "pg1"})
		default:
			t.Errorf("예상 못한 요청: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fake.Close()

	notionBase = fake.URL
	defer func() { notionBase = "" }()
	srv, _ := newServer(t)
	defer srv.Close()
	jar := newJarClient(t, srv)
	post(t, jar, srv.URL+"/api/login", map[string]string{"password": "admin-pass-123"}).Body.Close()
	post(t, jar, srv.URL+"/api/secrets", map[string]any{"notion_token": "ntn_x"}).Body.Close()
	post(t, jar, srv.URL+"/api/config", map[string]any{
		"notion": map[string]any{"enabled": true, "database_id": "0123456789abcdef0123456789abcdef"},
	}).Body.Close()

	resp := post(t, jar, srv.URL+"/api/test/notion/write", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("등록 테스트 = %d %s", resp.StatusCode, b)
	}
	var out struct {
		OK     bool              `json:"ok"`
		URL    string            `json:"url"`
		Sent   map[string]string `json:"sent"`
		Stored map[string]string `json:"stored"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if !created || !archived {
		t.Errorf("created=%v archived=%v — 만들고 되돌려야 한다", created, archived)
	}
	if out.URL != "https://notion.so/pg1" {
		t.Errorf("url = %q", out.URL)
	}
	// title 이름이 "회의실" 이므로 제목 칸에 보낸 값은 방 이름이어야 한다.
	if !strings.Contains(out.Sent["회의실"], "현승") && out.Sent["회의실"] != "테스트 회의실" {
		t.Errorf("보낸 제목 = %q", out.Sent["회의실"])
	}
	if !strings.Contains(out.Sent["회의실 사용 현황"], "[등록 테스트]") {
		t.Errorf("보낸 현황 = %q", out.Sent["회의실 사용 현황"])
	}
	if out.Stored["회의실"] != "현승 5A" {
		t.Errorf("되읽은 제목 = %q", out.Stored["회의실"])
	}
}
