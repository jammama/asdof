package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newTestServer(t *testing.T) (*server, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	st, err := newStore(path)
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	return &server{st: st, password: "asdof1234", static: dir}, path
}

func do(t *testing.T, s *server, method, path, pw string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if pw != "" {
		req.Header.Set("X-Admin-Password", pw)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func TestWritesRequirePassword(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/restaurants"},
		{"PUT", "/api/restaurants/r_x"},
		{"DELETE", "/api/restaurants/r_x"},
		{"POST", "/api/restaurants/r_x/visits"},
	} {
		if got := do(t, s, tc.method, tc.path, "wrong", map[string]string{"name": "가게"}).Code; got != http.StatusUnauthorized {
			t.Errorf("%s %s: 잘못된 비밀번호인데 %d (401 기대)", tc.method, tc.path, got)
		}
	}
	// 읽기는 비밀번호 없이 가능해야 한다
	if got := do(t, s, "GET", "/api/restaurants", "", nil).Code; got != http.StatusOK {
		t.Errorf("GET /api/restaurants: %d (200 기대)", got)
	}
}

func TestAuthEndpoint(t *testing.T) {
	s, _ := newTestServer(t)
	if got := do(t, s, "POST", "/api/auth", "", map[string]string{"password": "asdof1234"}).Code; got != http.StatusOK {
		t.Errorf("올바른 비밀번호: %d (200 기대)", got)
	}
	if got := do(t, s, "POST", "/api/auth", "", map[string]string{"password": "nope"}).Code; got != http.StatusUnauthorized {
		t.Errorf("틀린 비밀번호: %d (401 기대)", got)
	}
}

func TestRestaurantAndVisitLifecycle(t *testing.T) {
	s, path := newTestServer(t)

	rec := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{
		"name": "곰국시집", "category": "한식", "lat": 37.56, "lng": 126.98, "kakao_id": "12345",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("등록 실패: %d %s", rec.Code, rec.Body)
	}
	var created Restaurant
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "곰국시집" || created.Visits == nil {
		t.Fatalf("생성 결과가 이상함: %+v", created)
	}

	// 같은 kakao_id 재등록은 409
	if got := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{
		"name": "곰국시집 사칭", "kakao_id": "12345",
	}).Code; got != http.StatusConflict {
		t.Errorf("중복 등록: %d (409 기대)", got)
	}

	// 이름 없는 등록은 400
	if got := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{"name": "  "}).Code; got != http.StatusBadRequest {
		t.Errorf("빈 이름: %d (400 기대)", got)
	}

	base := "/api/restaurants/" + created.ID
	// 방문 기록 3건 — 날짜 역순 정렬을 확인하기 위해 섞어서 넣는다
	for _, d := range []string{"2026-08-10", "2026-09-01", "2026-08-20"} {
		if got := do(t, s, "POST", base+"/visits", "asdof1234", map[string]any{
			"date": d, "menus": []string{"전골국수", " "}, "rating": 9, "price": 12000,
		}).Code; got != http.StatusCreated {
			t.Fatalf("방문 기록(%s) 실패: %d", d, got)
		}
	}

	var got struct{ Restaurants []Restaurant }
	if err := json.Unmarshal(do(t, s, "GET", "/api/restaurants", "", nil).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Restaurants) != 1 {
		t.Fatalf("식당 %d곳 (1 기대)", len(got.Restaurants))
	}
	vs := got.Restaurants[0].Visits
	if len(vs) != 3 {
		t.Fatalf("방문 %d건 (3 기대)", len(vs))
	}
	if vs[0].Date != "2026-09-01" || vs[2].Date != "2026-08-10" {
		t.Errorf("날짜 내림차순 정렬 안 됨: %s %s %s", vs[0].Date, vs[1].Date, vs[2].Date)
	}
	if vs[0].Rating != 5 {
		t.Errorf("rating 9 는 5 로 clamp 돼야 함: %d", vs[0].Rating)
	}
	if len(vs[0].Menus) != 1 {
		t.Errorf("빈 메뉴는 제거돼야 함: %#v", vs[0].Menus)
	}

	// 방문 수정
	if got := do(t, s, "PUT", base+"/visits/"+vs[0].ID, "asdof1234", map[string]any{
		"date": "2026-09-02", "menus": []string{"수육"}, "rating": 4,
	}).Code; got != http.StatusOK {
		t.Errorf("방문 수정: %d (200 기대)", got)
	}
	// 방문 삭제
	if got := do(t, s, "DELETE", base+"/visits/"+vs[1].ID, "asdof1234", nil).Code; got != http.StatusOK {
		t.Errorf("방문 삭제: %d (200 기대)", got)
	}
	// 없는 방문 삭제는 404
	if got := do(t, s, "DELETE", base+"/visits/v_nope", "asdof1234", nil).Code; got != http.StatusNotFound {
		t.Errorf("없는 방문 삭제: %d (404 기대)", got)
	}
	// 없는 식당 수정은 404
	if got := do(t, s, "PUT", "/api/restaurants/r_nope", "asdof1234", map[string]any{"name": "x"}).Code; got != http.StatusNotFound {
		t.Errorf("없는 식당 수정: %d (404 기대)", got)
	}

	// 파일에 실제로 반영됐는지 (재시작 후에도 살아있어야 한다)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := newStore(path)
	if err != nil {
		t.Fatalf("재로딩 실패: %v (파일: %s)", err, raw)
	}
	rl := st2.list()
	if len(rl) != 1 || len(rl[0].Visits) != 2 {
		t.Fatalf("재로딩 결과: 식당 %d곳, 방문 %d건 (1곳 2건 기대)", len(rl), len(rl[0].Visits))
	}

	// 식당 삭제
	if got := do(t, s, "DELETE", base, "asdof1234", nil).Code; got != http.StatusOK {
		t.Errorf("식당 삭제: %d (200 기대)", got)
	}
	if len(s.st.list()) != 0 {
		t.Errorf("삭제 후에도 남아있음: %d", len(s.st.list()))
	}
}

func TestClampAndDefaults(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{"name": "가"})
	var r Restaurant
	_ = json.Unmarshal(rec.Body.Bytes(), &r)

	// 날짜 미지정 → 오늘 날짜로 채워진다
	vrec := do(t, s, "POST", "/api/restaurants/"+r.ID+"/visits", "asdof1234", map[string]any{
		"menus": []string{"라면"},
	})
	var v Visit
	if err := json.Unmarshal(vrec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Date) != 10 {
		t.Errorf("날짜 기본값이 채워지지 않음: %q", v.Date)
	}
	if v.Menus == nil {
		t.Error("menus 가 nil — JSON 에 null 로 나가면 클라이언트가 깨진다")
	}
}

func TestStoreHandlesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := newStore(path)
	if err != nil {
		t.Fatalf("빈 파일에서 실패: %v", err)
	}
	if len(st.list()) != 0 {
		t.Errorf("빈 파일인데 %d곳", len(st.list()))
	}
}

func TestMenusRegisteredAsArray(t *testing.T) {
	s, _ := newTestServer(t)

	// 등록 폼은 쉼표로 끊은 문자열 배열을 보낸다
	rec := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{
		"name":  "곰국시집",
		"menus": []string{"전골국수", " 수육 ", "", "전골국수"}, // 공백 트림·빈값·중복 제거 확인
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("등록 실패: %d %s", rec.Code, rec.Body)
	}
	var r Restaurant
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Menus) != 2 {
		t.Fatalf("메뉴 %d개 (2 기대): %+v", len(r.Menus), r.Menus)
	}
	if r.Menus[0].Name != "전골국수" || r.Menus[1].Name != "수육" {
		t.Errorf("메뉴 이름/순서가 이상함: %q %q", r.Menus[0].Name, r.Menus[1].Name)
	}
	for _, m := range r.Menus {
		if m.ID == "" {
			t.Errorf("메뉴 id 가 비어있음: %+v", m)
		}
		if m.Rating != 0 {
			t.Errorf("등록 직후 별점은 0 이어야 함: %d", m.Rating)
		}
	}

	base := "/api/restaurants/" + r.ID
	mid := r.Menus[0].ID

	// 별점만 찍는 전용 경로 — 저장 버튼 없이 별 클릭 한 번으로 저장된다
	prec := do(t, s, "PUT", base+"/menus/"+mid, "asdof1234", map[string]any{"rating": 4})
	if prec.Code != http.StatusOK {
		t.Fatalf("별점 저장: %d %s", prec.Code, prec.Body)
	}
	var m MenuItem
	if err := json.Unmarshal(prec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Rating != 4 || m.Name != "전골국수" {
		t.Errorf("별점 응답이 이상함: %+v", m)
	}

	// 범위를 넘는 값은 clamp, 0 은 '지우기' 로 그대로 반영
	if got := do(t, s, "PUT", base+"/menus/"+mid, "asdof1234", map[string]any{"rating": 99}); got.Code != http.StatusOK {
		t.Fatalf("clamp 요청 실패: %d", got.Code)
	}
	if s.st.list()[0].Menus[0].Rating != 5 {
		t.Errorf("99 는 5 로 clamp 돼야 함: %d", s.st.list()[0].Menus[0].Rating)
	}
	if got := do(t, s, "PUT", base+"/menus/"+mid, "asdof1234", map[string]any{"rating": 0}); got.Code != http.StatusOK {
		t.Fatalf("별점 지우기 실패: %d", got.Code)
	}
	if s.st.list()[0].Menus[0].Rating != 0 {
		t.Errorf("0 이면 별점이 지워져야 함: %d", s.st.list()[0].Menus[0].Rating)
	}

	// 인증 없으면 401, 없는 메뉴는 404, 잘못된 메서드는 405
	if got := do(t, s, "PUT", base+"/menus/"+mid, "wrong", map[string]any{"rating": 3}).Code; got != http.StatusUnauthorized {
		t.Errorf("비밀번호 없이 별점: %d (401 기대)", got)
	}
	if got := do(t, s, "PUT", base+"/menus/m_nope", "asdof1234", map[string]any{"rating": 3}).Code; got != http.StatusNotFound {
		t.Errorf("없는 메뉴: %d (404 기대)", got)
	}
	if got := do(t, s, "DELETE", base+"/menus/"+mid, "asdof1234", nil).Code; got != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: %d (405 기대)", got)
	}
}

func TestMenuRatingSurvivesRestaurantEdit(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{
		"name": "가게", "menus": []string{"국수", "수육"},
	})
	var r Restaurant
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	base := "/api/restaurants/" + r.ID

	// 두 메뉴에 별점을 찍고
	for i, want := range []int{5, 3} {
		if got := do(t, s, "PUT", base+"/menus/"+r.Menus[i].ID, "asdof1234",
			map[string]any{"rating": want}).Code; got != http.StatusOK {
			t.Fatalf("별점 저장(%d): %d", i, got)
		}
	}
	// 식당 수정으로 메뉴를 지우고 추가한다 (별점은 폼이 안 보낸다)
	if got := do(t, s, "PUT", base, "asdof1234", map[string]any{
		"name": "가게", "menus": []string{"국수", "만두"},
	}).Code; got != http.StatusOK {
		t.Fatalf("식당 수정: %d", got)
	}

	ms := s.st.list()[0].Menus
	if len(ms) != 2 {
		t.Fatalf("메뉴 %d개 (2 기대)", len(ms))
	}
	if ms[0].Name != "국수" || ms[0].Rating != 5 {
		t.Errorf("남긴 메뉴의 별점이 유지돼야 함: %+v", ms[0])
	}
	if ms[0].ID != r.Menus[0].ID {
		t.Errorf("이름이 같으면 메뉴 id 도 유지돼야 함: %s -> %s", r.Menus[0].ID, ms[0].ID)
	}
	if ms[1].Name != "만두" || ms[1].Rating != 0 {
		t.Errorf("새 메뉴는 별점 0: %+v", ms[1])
	}
}

func TestMenusAcceptObjectForm(t *testing.T) {
	s, _ := newTestServer(t)
	// 객체 형태({name,rating})도 받아야 한다 — 클라이언트가 별점을 실어 보내는 경우
	rec := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{
		"name": "가게",
		"menus": []map[string]any{
			{"name": "국수", "rating": 4},
			{"name": "수육"},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("등록 실패: %d %s", rec.Code, rec.Body)
	}
	var r Restaurant
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if len(r.Menus) != 2 || r.Menus[0].Rating != 4 || r.Menus[1].Rating != 0 {
		t.Fatalf("객체 형태 파싱 실패: %+v %+v", r.Menus[0], r.Menus[1])
	}
}

func TestOldRecordWithoutMenusLoads(t *testing.T) {
	// 메뉴 기능 이전에 저장된 파일도 그대로 열려야 한다
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	old := `{"restaurants":[{"id":"r_1","name":"옛가게","visits":[{"id":"v_1","date":"2026-08-01","menus":["국밥"]}]}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := newStore(path)
	if err != nil {
		t.Fatalf("옛 파일 로딩 실패: %v", err)
	}
	r := st.list()[0]
	if r.Menus == nil {
		t.Error("Menus 가 nil — JSON 에 null 로 나가면 클라이언트가 깨진다")
	}
	if len(r.Visits) != 1 || r.Visits[0].Menus[0] != "국밥" {
		t.Errorf("방문 기록이 보존돼야 함: %+v", r.Visits)
	}
}

func TestKakaoPlaceRequiresAuthAndValidID(t *testing.T) {
	s, _ := newTestServer(t)
	// 외부로 나가는 프록시라 인증이 없으면 401 — 열린 프록시가 되면 안 된다
	if got := do(t, s, "GET", "/api/kakao/place/16328087", "", nil).Code; got != http.StatusUnauthorized {
		t.Errorf("비밀번호 없이 호출: %d (401 기대)", got)
	}
	// id 검증은 외부 호출 전에 끝나야 한다 (네트워크를 타지 않는다)
	for _, bad := range []string{"abc", "1;2", "", "123456789012345678901", "16328087/extra"} {
		if got := do(t, s, "GET", "/api/kakao/place/"+bad, "asdof1234", nil).Code; got != http.StatusBadRequest {
			t.Errorf("잘못된 id %q: %d (400 기대)", bad, got)
		}
	}
	// 경로 탈출(.. 포함)은 핸들러에 닿기 전에 ServeMux 가 정규화 리다이렉트로 막는다.
	// 어느 쪽이든 200 이 나오면 안 된다.
	for _, bad := range []string{"../../etc/passwd", "16328087/../x"} {
		if got := do(t, s, "GET", "/api/kakao/place/"+bad, "asdof1234", nil).Code; got == http.StatusOK {
			t.Errorf("경로 탈출 %q 가 통과했다: %d", bad, got)
		}
	}
	if got := do(t, s, "POST", "/api/kakao/place/16328087", "asdof1234", nil).Code; got != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d (405 기대)", got)
	}
}

func TestIsDigits(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"16328087", true}, {"0", true},
		{"", false}, {"12a", false}, {"-1", false}, {"1 2", false}, {"１２", false},
	} {
		if got := isDigits(c.in); got != c.want {
			t.Errorf("isDigits(%q) = %v (want %v)", c.in, got, c.want)
		}
	}
}

func TestVisitAddsNewMenusAndReorder(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, "POST", "/api/restaurants", "asdof1234", map[string]any{
		"name": "가게", "menus": []string{"국수", "수육"},
	})
	var r Restaurant
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	base := "/api/restaurants/" + r.ID
	_ = do(t, s, "PUT", base+"/menus/"+r.Menus[0].ID, "asdof1234", map[string]any{"rating": 4})

	// 방문 기록에 처음 나온 메뉴(만두)는 식당 메뉴 끝에 붙는다. 이미 있는 건 중복 안 됨.
	if got := do(t, s, "POST", base+"/visits", "asdof1234", map[string]any{
		"date": "2026-10-01", "menus": []string{"수육", "만두"},
	}).Code; got != http.StatusCreated {
		t.Fatalf("방문 기록: %d", got)
	}
	names := func() (out []string) {
		for _, m := range s.st.list()[0].Menus {
			out = append(out, m.Name)
		}
		return
	}
	if got := names(); len(got) != 3 || got[2] != "만두" {
		t.Fatalf("메뉴 흡수: %v", got)
	}

	// 순서 변경 — 모르는 이름은 무시, 빠진 메뉴(국수)는 뒤에 남는다. 별점 유지.
	if got := do(t, s, "PUT", base+"/menus", "asdof1234", map[string]any{
		"names": []string{"만두", "없는메뉴", "수육"},
	}).Code; got != http.StatusOK {
		t.Fatalf("순서 변경: %d", got)
	}
	if got := names(); len(got) != 3 || got[0] != "만두" || got[1] != "수육" || got[2] != "국수" {
		t.Fatalf("순서: %v", got)
	}
	if s.st.list()[0].Menus[2].Rating != 4 {
		t.Errorf("순서 바꿔도 별점 유지돼야 함")
	}
	if got := do(t, s, "PUT", base+"/menus", "", map[string]any{"names": []string{}}).Code; got != http.StatusUnauthorized {
		t.Errorf("인증 없이 순서 변경: %d", got)
	}
}
