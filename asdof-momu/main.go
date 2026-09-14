// asdof-momu — "뭐 먹었지?" 식당 방문 기록 앱의 파일 서버.
//
// DB 없이 JSON 파일 하나(DATA_FILE)에 전부 저장한다. 쓰기는 mutex 로 직렬화하고
// 임시파일 + rename 으로 원자적으로 교체하므로 프로세스가 죽어도 파일이 깨지지 않는다.
// 쓰기 API 는 모두 X-Admin-Password 헤더로 관리자 비밀번호를 요구한다.
//
// 환경변수
//
//	LISTEN_ADDR     기본 127.0.0.1:8790  (nginx 리버스 프록시 앞단)
//	STATIC_DIR      기본 ./static
//	DATA_FILE       기본 ./data.json
//	ADMIN_PASSWORD  기본 asdof1234
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- 모델 ----------

// Visit 은 한 번의 방문 기록 — 이 앱의 본질(뭘 먹었는지).
type Visit struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`    // YYYY-MM-DD
	Menus     []string `json:"menus"`   // 먹은 메뉴
	Rating    int      `json:"rating"`  // 0~5 (0 = 미평가)
	Price     int      `json:"price"`   // 1인 기준 원 (0 = 미기재)
	Company   string   `json:"company"` // 같이 간 사람/모임
	Memo      string   `json:"memo"`
	CreatedAt string   `json:"created_at"`
}

// MenuItem 은 식당에 등록해 둔 메뉴 하나와 그 메뉴의 별점.
// 방문 기록의 rating(그날 만족도)과 달리 메뉴 자체에 매기는 점수다.
type MenuItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Rating int    `json:"rating"` // 0~5 (0 = 미평가)
}

// UnmarshalJSON 은 "김치찌개" 같은 문자열과 {"name":…,"rating":…} 객체를 모두 받는다.
// (등록 폼은 쉼표로 끊은 문자열 배열을 보내고, 별점은 전용 엔드포인트로 따로 들어온다)
func (m *MenuItem) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		m.ID, m.Name, m.Rating = "", s, 0
		return nil
	}
	var raw struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Rating int    `json:"rating"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.ID, m.Name, m.Rating = raw.ID, raw.Name, raw.Rating
	return nil
}

type Restaurant struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Category    string      `json:"category"`
	Address     string      `json:"address"`
	RoadAddress string      `json:"road_address"`
	Phone       string      `json:"phone"`
	Lat         float64     `json:"lat"`
	Lng         float64     `json:"lng"`
	PlaceURL    string      `json:"place_url"`
	KakaoID     string      `json:"kakao_id"`
	Memo        string      `json:"memo"`
	CreatedAt   string      `json:"created_at"`
	Menus       []*MenuItem `json:"menus"`
	Visits      []*Visit    `json:"visits"`
}

type db struct {
	Restaurants []*Restaurant `json:"restaurants"`
}

// ---------- 저장소 ----------

type store struct {
	mu   sync.RWMutex
	path string
	data db
}

func newStore(path string) (*store, error) {
	s := &store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = db{Restaurants: []*Restaurant{}}
		return s, s.flush() // 첫 실행: 빈 파일을 만들어 쓰기 권한을 미리 확인한다
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		s.data = db{Restaurants: []*Restaurant{}}
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("%s 파싱 실패: %w", path, err)
	}
	if s.data.Restaurants == nil {
		s.data.Restaurants = []*Restaurant{}
	}
	for _, r := range s.data.Restaurants {
		if r.Visits == nil {
			r.Visits = []*Visit{}
		}
		// 메뉴 기능이 생기기 전에 등록된 식당 보정
		if r.Menus == nil {
			r.Menus = []*MenuItem{}
		}
		for _, m := range r.Menus {
			if m.ID == "" {
				m.ID = newID("m")
			}
		}
	}
	return s, nil
}

// flush 는 호출자가 lock 을 쥔 상태에서 호출한다. 임시파일 → rename 으로 원자적 교체.
func (s *store) flush() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) list() []*Restaurant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Restaurant, len(s.data.Restaurants))
	copy(out, s.data.Restaurants)
	return out
}

// findLocked 는 lock 을 쥔 상태에서 id 로 식당을 찾는다.
func (s *store) findLocked(id string) *Restaurant {
	for _, r := range s.data.Restaurants {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// mutate 는 쓰기 lock 안에서 fn 을 실행하고 성공 시 파일에 반영한다.
func (s *store) mutate(fn func(*db) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.data); err != nil {
		return err
	}
	return s.flush()
}

// ---------- 유틸 ----------

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 실패는 사실상 없지만, 그래도 시간으로 대체해 진행한다
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// clampStr 은 입력 길이를 제한한다 (JSON 파일이 무한정 커지는 것 방지).
func clampStr(s string, max int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------- 서버 ----------

type server struct {
	st       *store
	password string
	static   string
}

func (s *server) authed(r *http.Request) bool {
	given := r.Header.Get("X-Admin-Password")
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.password)) == 1
}

// requireAuth 는 인증 실패 시 401 을 쓰고 false 를 반환한다.
func (s *server) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if !s.authed(r) {
		writeErr(w, http.StatusUnauthorized, "관리자 비밀번호가 틀렸습니다.")
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "요청 본문을 읽을 수 없습니다: "+err.Error())
		return false
	}
	return true
}

// 방문 기록 입력(등록/수정 공용)
type visitInput struct {
	Date    string   `json:"date"`
	Menus   []string `json:"menus"`
	Rating  int      `json:"rating"`
	Price   int      `json:"price"`
	Company string   `json:"company"`
	Memo    string   `json:"memo"`
}

func cleanMenus(in []string) []string {
	out := []string{}
	for _, m := range in {
		if m = clampStr(m, 60); m != "" {
			out = append(out, m)
		}
		if len(out) >= 30 {
			break
		}
	}
	return out
}

// applyVisit 은 입력값을 정규화해 방문 기록에 반영한다.
func (v *Visit) applyVisit(in visitInput) {
	v.Date = clampStr(in.Date, 10)
	if v.Date == "" {
		v.Date = time.Now().Format("2006-01-02")
	}
	v.Menus = cleanMenus(in.Menus)
	v.Rating = clampInt(in.Rating, 0, 5)
	v.Price = clampInt(in.Price, 0, 100_000_000)
	v.Company = clampStr(in.Company, 60)
	v.Memo = clampStr(in.Memo, 500)
}

type restaurantInput struct {
	Name        string     `json:"name"`
	Category    string     `json:"category"`
	Address     string     `json:"address"`
	RoadAddress string     `json:"road_address"`
	Phone       string     `json:"phone"`
	Lat         float64    `json:"lat"`
	Lng         float64    `json:"lng"`
	PlaceURL    string     `json:"place_url"`
	KakaoID     string     `json:"kakao_id"`
	Memo        string     `json:"memo"`
	Menus       []MenuItem `json:"menus"` // 문자열 배열도 받는다 (MenuItem.UnmarshalJSON)
}

func (r *Restaurant) apply(in restaurantInput) {
	r.Name = clampStr(in.Name, 80)
	r.Category = clampStr(in.Category, 20)
	r.Address = clampStr(in.Address, 200)
	r.RoadAddress = clampStr(in.RoadAddress, 200)
	r.Phone = clampStr(in.Phone, 40)
	r.Lat = in.Lat
	r.Lng = in.Lng
	r.PlaceURL = clampStr(in.PlaceURL, 300)
	r.KakaoID = clampStr(in.KakaoID, 40)
	r.Memo = clampStr(in.Memo, 500)
	r.applyMenus(in.Menus)
}

// applyMenus 는 새 메뉴 목록을 반영한다. 별점은 등록/수정 폼이 보내지 않으므로,
// 이름(또는 id)이 같은 기존 메뉴의 별점을 살려 둔다 — 메뉴를 추가·삭제해도 점수가 날아가지 않는다.
// 별점 자체는 PUT /api/restaurants/{id}/menus/{mid} 로만 바뀐다.
func (r *Restaurant) applyMenus(in []MenuItem) {
	prevByID := map[string]*MenuItem{}
	prevByName := map[string]*MenuItem{}
	for _, m := range r.Menus {
		if m.ID != "" {
			prevByID[m.ID] = m
		}
		prevByName[m.Name] = m
	}

	out := []*MenuItem{}
	seen := map[string]bool{}
	for _, m := range in {
		name := clampStr(m.Name, 60)
		if name == "" || seen[name] {
			continue // 빈 값·중복 제거
		}
		seen[name] = true

		item := &MenuItem{ID: m.ID, Name: name, Rating: clampInt(m.Rating, 0, 5)}
		prev := prevByID[m.ID]
		if prev == nil {
			prev = prevByName[name]
		}
		if prev != nil {
			if item.Rating == 0 {
				item.Rating = prev.Rating
			}
			if item.ID == "" {
				item.ID = prev.ID
			}
		}
		if item.ID == "" {
			item.ID = newID("m")
		}
		out = append(out, item)
		if len(out) >= 60 {
			break
		}
	}
	r.Menus = out
}

// ---------- 핸들러 ----------

func (s *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST 만 허용됩니다.")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(in.Password), []byte(s.password)) != 1 {
		writeErr(w, http.StatusUnauthorized, "관리자 비밀번호가 틀렸습니다.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleRestaurants(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"restaurants": s.st.list()})

	case http.MethodPost:
		if !s.requireAuth(w, r) {
			return
		}
		var in restaurantInput
		if !decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			writeErr(w, http.StatusBadRequest, "식당 이름은 필수입니다.")
			return
		}
		rec := &Restaurant{ID: newID("r"), CreatedAt: nowISO(), Visits: []*Visit{}}
		rec.apply(in)
		err := s.st.mutate(func(d *db) error {
			// 같은 카카오 장소가 이미 등록돼 있으면 중복 등록을 막는다
			if rec.KakaoID != "" {
				for _, ex := range d.Restaurants {
					if ex.KakaoID == rec.KakaoID {
						return fmt.Errorf("이미 등록된 식당입니다: %s", ex.Name)
					}
				}
			}
			d.Restaurants = append(d.Restaurants, rec)
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, rec)

	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET/POST 만 허용됩니다.")
	}
}

// handleRestaurantItem 은 /api/restaurants/{id} 와 그 하위 /visits[/{vid}] 를 처리한다.
func (s *server) handleRestaurantItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/restaurants/"), "/")
	if rest == "" {
		writeErr(w, http.StatusNotFound, "식당 id 가 없습니다.")
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]

	// /api/restaurants/{id}/visits...
	if len(parts) >= 2 && parts[1] == "visits" {
		vid := ""
		if len(parts) >= 3 {
			vid = parts[2]
		}
		s.handleVisits(w, r, id, vid)
		return
	}
	// /api/restaurants/{id}/menus/{mid} — 메뉴 별점만 바꾸는 전용 경로
	if len(parts) >= 2 && parts[1] == "menus" {
		mid := ""
		if len(parts) >= 3 {
			mid = parts[2]
		}
		s.handleMenuRating(w, r, id, mid)
		return
	}
	if len(parts) != 1 {
		writeErr(w, http.StatusNotFound, "알 수 없는 경로입니다.")
		return
	}

	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		if !s.requireAuth(w, r) {
			return
		}
		var in restaurantInput
		if !decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Name) == "" {
			writeErr(w, http.StatusBadRequest, "식당 이름은 필수입니다.")
			return
		}
		var out *Restaurant
		if err := s.st.mutate(func(d *db) error {
			rec := s.st.findLocked(id)
			if rec == nil {
				return errNotFound
			}
			rec.apply(in)
			out = rec
			return nil
		}); err != nil {
			respondMutateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodDelete:
		if !s.requireAuth(w, r) {
			return
		}
		if err := s.st.mutate(func(d *db) error {
			for i, rec := range d.Restaurants {
				if rec.ID == id {
					d.Restaurants = append(d.Restaurants[:i], d.Restaurants[i+1:]...)
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			respondMutateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "PUT/DELETE 만 허용됩니다.")
	}
}

var errNotFound = errors.New("대상을 찾을 수 없습니다.")

func respondMutateErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// ---------- 카카오 장소 정보 가져오기 ----------

// 카카오 장소 API 는 브라우저에서 직접 부르면 CORS 로 막히고,
// pf/Referer 헤더가 없으면 406 을 준다. 그래서 서버가 대신 호출한다.
const kakaoPlaceAPI = "https://place-api.map.kakao.com/places/panel3/"

var kakaoClient = &http.Client{Timeout: 8 * time.Second}

// kakaoPanel 은 응답에서 우리가 쓰는 부분만 뽑아낸 형태.
type kakaoPanel struct {
	Menu struct {
		Menus struct {
			Items []struct {
				Name  string  `json:"name"`
				Price float64 `json:"price"`
			} `json:"items"`
			MenuType string `json:"menu_type"`
		} `json:"menus"`
	} `json:"menu"`
	KakaomapReview struct {
		ScoreSet struct {
			ReviewCount  int     `json:"review_count"`
			AverageScore float64 `json:"average_score"`
		} `json:"score_set"`
	} `json:"kakaomap_review"`
}

type kakaoMenuOut struct {
	Name  string `json:"name"`
	Price int    `json:"price"` // 0 = 가격 미공개 (카카오가 -1 로 주는 경우 포함)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// handleKakaoPlace 는 GET /api/kakao/place/{kakao_id} 로 메뉴·평점을 돌려준다.
// 외부로 나가는 요청이라 관리자 인증을 요구한다(열린 프록시가 되면 안 된다).
func (s *server) handleKakaoPlace(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET 만 허용됩니다.")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/kakao/place/"), "/")
	if !isDigits(id) || len(id) > 20 {
		writeErr(w, http.StatusBadRequest, "카카오 장소 id 가 올바르지 않습니다.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kakaoPlaceAPI+id, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 이 세 헤더가 다 있어야 200 이 온다 (pf 없으면 406, Referer 없으면 406)
	req.Header.Set("pf", "web")
	req.Header.Set("Referer", "https://place.map.kakao.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")

	resp, err := kakaoClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "카카오에 연결하지 못했습니다: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway,
			fmt.Sprintf("카카오가 %d 를 응답했습니다. 장소 id 를 확인해 주세요.", resp.StatusCode))
		return
	}

	var panel kakaoPanel
	// 응답이 60KB 정도라 1MB 로 막아 둔다
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&panel); err != nil {
		writeErr(w, http.StatusBadGateway, "카카오 응답을 해석하지 못했습니다: "+err.Error())
		return
	}

	menus := []kakaoMenuOut{}
	seen := map[string]bool{}
	for _, it := range panel.Menu.Menus.Items {
		name := clampStr(it.Name, 60)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		price := 0
		if it.Price > 0 { // 가격 미공개는 -1 로 온다
			price = int(it.Price)
		}
		menus = append(menus, kakaoMenuOut{Name: name, Price: price})
		if len(menus) >= 60 {
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"kakao_id": id,
		"menus":    menus,
		"score":    panel.KakaomapReview.ScoreSet.AverageScore,
		"reviews":  panel.KakaomapReview.ScoreSet.ReviewCount,
	})
}

// handleMenuRating 은 메뉴 하나의 별점만 갱신한다.
// 상세 화면에서 별을 누르면 저장 버튼 없이 바로 이 경로로 들어온다.
func (s *server) handleMenuRating(w http.ResponseWriter, r *http.Request, id, mid string) {
	if !s.requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		writeErr(w, http.StatusMethodNotAllowed, "PUT 만 허용됩니다.")
		return
	}
	if mid == "" {
		writeErr(w, http.StatusNotFound, "메뉴 id 가 없습니다.")
		return
	}
	var in struct {
		Rating int `json:"rating"`
	}
	if !decode(w, r, &in) {
		return
	}
	var out *MenuItem
	if err := s.st.mutate(func(d *db) error {
		rec := s.st.findLocked(id)
		if rec == nil {
			return errNotFound
		}
		for _, m := range rec.Menus {
			if m.ID == mid {
				m.Rating = clampInt(in.Rating, 0, 5)
				out = m
				return nil
			}
		}
		return errNotFound
	}); err != nil {
		respondMutateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleVisits(w http.ResponseWriter, r *http.Request, id, vid string) {
	if !s.requireAuth(w, r) {
		return
	}
	switch {
	case r.Method == http.MethodPost && vid == "":
		var in visitInput
		if !decode(w, r, &in) {
			return
		}
		v := &Visit{ID: newID("v"), CreatedAt: nowISO()}
		v.applyVisit(in)
		if err := s.st.mutate(func(d *db) error {
			rec := s.st.findLocked(id)
			if rec == nil {
				return errNotFound
			}
			rec.Visits = append(rec.Visits, v)
			// 최신 방문이 위로 오도록 날짜 내림차순 유지
			sortVisits(rec.Visits)
			return nil
		}); err != nil {
			respondMutateErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)

	case (r.Method == http.MethodPut || r.Method == http.MethodPatch) && vid != "":
		var in visitInput
		if !decode(w, r, &in) {
			return
		}
		var out *Visit
		if err := s.st.mutate(func(d *db) error {
			rec := s.st.findLocked(id)
			if rec == nil {
				return errNotFound
			}
			for _, v := range rec.Visits {
				if v.ID == vid {
					v.applyVisit(in)
					out = v
					sortVisits(rec.Visits)
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			respondMutateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)

	case r.Method == http.MethodDelete && vid != "":
		if err := s.st.mutate(func(d *db) error {
			rec := s.st.findLocked(id)
			if rec == nil {
				return errNotFound
			}
			for i, v := range rec.Visits {
				if v.ID == vid {
					rec.Visits = append(rec.Visits[:i], rec.Visits[i+1:]...)
					return nil
				}
			}
			return errNotFound
		}); err != nil {
			respondMutateErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "허용되지 않은 메서드/경로입니다.")
	}
}

func sortVisits(vs []*Visit) {
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Date != vs[j].Date {
			return vs[i].Date > vs[j].Date
		}
		return vs[i].CreatedAt > vs[j].CreatedAt
	})
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "ok %d\n", len(s.st.list()))
	})
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/restaurants", s.handleRestaurants)
	mux.HandleFunc("/api/restaurants/", s.handleRestaurantItem)
	mux.HandleFunc("/api/kakao/place/", s.handleKakaoPlace)

	fs := http.FileServer(http.Dir(s.static))
	mux.Handle("/", noCacheHTML(fs))
	return mux
}

// noCacheHTML 은 index.html 만 캐시를 막는다(배포 직후 옛 화면이 남는 것 방지).
func noCacheHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || strings.HasSuffix(p, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	addr := env("LISTEN_ADDR", "127.0.0.1:8790")
	staticDir := env("STATIC_DIR", "./static")
	dataFile := env("DATA_FILE", "./data.json")
	password := env("ADMIN_PASSWORD", "asdof1234")

	st, err := newStore(dataFile)
	if err != nil {
		log.Fatalf("데이터 파일 열기 실패: %v", err)
	}
	s := &server{st: st, password: password, static: staticDir}

	log.Printf("asdof-momu 시작 — addr=%s static=%s data=%s (식당 %d곳)",
		addr, staticDir, dataFile, len(st.list()))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
