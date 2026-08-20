// asdof-random — 제비뽑기 서버
//
// 참가 절차가 없다. 링크를 연 사람은 누구나 같은 명단·같은 결과를 본다.
// 명단과 항목은 뽑기 전이면 화면에서 바로 고칠 수 있고, 판을 처음 열 때의 기본 명단은
// 파일(MEMBERS_FILE)에서 읽는다.
//
// 특징:
//   - 단일 바이너리. 외부 의존성은 WebSocket 라이브러리 하나뿐.
//   - 뽑기는 서버가 crypto/rand 시드로 섞어서 결정 → 접속자마다 결과가 어긋날 수 없다.
//   - 뽑은 결과는 파일(DRAW_DB)에 저장 → 아무도 안 보고 있어도, 서버를 재시작해도 남는다.
//   - 항목은 "이름 + 인원(자리)" 목록. 자리 합이 명단보다 많으면 남는 자리는 빠지고,
//     적으면 모자란 만큼 꽝이 된다.
//   - 명단/항목 편집은 뽑기 전에만 가능하다(뽑은 뒤엔 결과 보존을 위해 잠긴다).
//   - 경로가 곧 판의 키: 루트(/)는 방 만들기 화면이고, 거기서 이름을 정하면 무작위 10글자 키를
//     만들어 /<키> 로 넘어간다. 이미 키를 아는 사람은 그 주소로 바로 들어오면 된다.
//     방 이름은 표시용 이름일 뿐이고, 판을 구분하는 건 언제나 경로다.
//
// 라우트:
//
//	/_ws?room=       판 WebSocket (JSON 메시지)
//	/healthz         헬스체크
//	그 외 모든 경로   static/index.html
//
// 환경 변수:
//
//	LISTEN_ADDR   바인딩 주소(예: 127.0.0.1:8770). 없으면 :PORT
//	PORT          기본 8770
//	STATIC_DIR    정적 파일 디렉토리 (기본 ./static)
//	MEMBERS_FILE  기본 명단 파일 (기본 ./members.md, 한 줄에 한 명)
//	DRAW_DB       결과 저장 파일. 없으면 STATE_DIRECTORY/draws.json, 그것도 없으면 메모리만.
package main

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	sendBuffer = 32    // 클라이언트당 송신 버퍼. 넘치면 '느린 소비자'로 보고 연결 종료.
	readLimit  = 32768 // 수신 프레임 최대 바이트(명단 60명 + 항목 12개 편집을 담을 여유)
	maxRoomLen = 60    // 판 이름 최대 길이
	maxMembers = 60    // 명단 상한
	maxNameLen = 20    // 멤버 이름 최대 길이

	maxPoolItems = 12 // 항목 개수 상한
	maxPrizeName = 70 // 항목 이름 최대 길이
	maxSeats     = 60 // 항목당 인원 상한

	writeWait  = 10 * time.Second
	pingPeriod = 45 * time.Second

	maxTitleLen  = 30                  // 방 이름 최대 길이
	defaultTitle = "랜덤 팀뽑기"            // 방 이름 기본값
	defaultRoom  = "main"              // 경로 없이 접속했을 때의 판 키(화면은 루트에서 방 만들기 게이트를 띄운다)
	pruneAfter   = 30 * 24 * time.Hour // 뽑지 않은 채 이만큼 지난 저장 판은 정리
)

// ---- 항목(뽑기 대상) ----

// Prize: 뽑기 항목 하나. Seats 는 그 항목에 배정될 인원(= 제비 장수).
type Prize struct {
	Name  string `json:"name"`
	Seats int    `json:"seats"`
}

// defaultPool: 판을 처음 만들 때 들어 있는 항목 목록(자리 합 5 = 기본 명단 5명).
// 실제 항목은 화면에서 채워 넣는다 — 여기 값은 자리 표시용.
func defaultPool() []Prize {
	return []Prize{
		{"항목1", 3},
		{"항목2", 2},
	}
}

func totalSeats(pool []Prize) int {
	n := 0
	for _, p := range pool {
		n += p.Seats
	}
	return n
}

// sanitizePool: 클라이언트가 보낸 항목 목록을 다듬는다(이름 정리, 인원 범위, 개수 상한).
func sanitizePool(in []Prize) []Prize {
	out := make([]Prize, 0, len(in))
	for _, p := range in {
		name := sanitize(p.Name, maxPrizeName)
		if name == "" {
			continue
		}
		seats := p.Seats
		if seats < 1 {
			seats = 1
		}
		if seats > maxSeats {
			seats = maxSeats
		}
		out = append(out, Prize{Name: name, Seats: seats})
		if len(out) >= maxPoolItems {
			break
		}
	}
	return out
}

// sanitizeMembers: 화면에서 보낸 명단을 다듬는다(빈 줄 제거, 길이/인원 상한).
// 같은 이름이 둘 있어도 막지 않는다(실제 명단에 "병우A" 처럼 구분해 쓰는 경우가 있어서).
func sanitizeMembers(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = sanitize(s, maxNameLen); s == "" {
			continue
		}
		out = append(out, s)
		if len(out) >= maxMembers {
			break
		}
	}
	return out
}

// loadMembers: 명단 파일을 읽는다. 한 줄에 한 명, 빈 줄과 '#' 주석은 무시.
func loadMembers(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("명단 파일을 읽을 수 없음(%s): %v", path, err)
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		s := sanitize(strings.TrimPrefix(strings.TrimSpace(line), "- "), maxNameLen)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out = append(out, s)
		if len(out) >= maxMembers {
			break
		}
	}
	return out
}

// ---- 판(방) ----

type Room struct {
	name string

	mu      sync.Mutex
	clients map[*Client]struct{}

	title   string   // 방 이름(표시용). 판을 구분하는 키는 URL 경로다.
	members []string // 이 판의 명단(뽑은 시점 그대로 보존 — 결과 해석이 어긋나지 않게)
	base    []string // 명단 파일의 내용(화면에서 손대지 않은 판은 「다시 뽑기」 때 이걸로 갈아탄다)
	pool    []Prize
	custom  bool  // 화면에서 명단/항목을 고친 판인지(고쳤으면 파일 기본값으로 되돌리지 않는다)
	draw    []int // 멤버 인덱스 → 항목 인덱스(-1 이면 꽝). nil 이면 아직 안 뽑음
	drawnAt int64 // unix millis
	round   int
}

type Client struct {
	conn   *websocket.Conn
	send   chan []byte
	cancel context.CancelFunc
}

type Hub struct {
	mu      sync.Mutex
	rooms   map[string]*Room
	members []string // 파일에서 읽은 기본 명단
	store   *Store
}

func NewHub(members []string, store *Store) *Hub {
	h := &Hub{rooms: make(map[string]*Room), members: members, store: store}
	// 저장된 판을 되살린다 → 서버를 재시작해도 결과가 남는다.
	// 뽑지도 않은 채 오래 방치된 판(만들다 만 방)은 이때 정리한다.
	cutoff := time.Now().Add(-pruneAfter).UnixMilli()
	for name, snap := range store.all() {
		if snap.Draw == nil && snap.UpdatedAt > 0 && snap.UpdatedAt < cutoff {
			store.drop(name)
			continue
		}
		h.rooms[name] = &Room{
			name: name, clients: make(map[*Client]struct{}),
			title: snap.Title, members: snap.Members, base: members, pool: snap.Pool, custom: snap.Custom,
			draw: snap.Draw, drawnAt: snap.DrawnAt, round: snap.Round,
		}
	}
	return h
}

func (h *Hub) join(name string, c *Client) *Room {
	h.mu.Lock()
	room := h.rooms[name]
	if room == nil {
		room = &Room{
			name:    name,
			title:   defaultTitle,
			clients: make(map[*Client]struct{}),
			members: h.members,
			base:    h.members,
			pool:    defaultPool(),
		}
		h.rooms[name] = room
	}
	h.mu.Unlock()

	room.mu.Lock()
	room.clients[c] = struct{}{}
	room.broadcastLocked()
	room.mu.Unlock()
	return room
}

func (h *Hub) leave(room *Room, c *Client) {
	room.mu.Lock()
	delete(room.clients, c)
	empty := len(room.clients) == 0
	keep := room.worthKeeping()
	room.broadcastLocked()
	room.mu.Unlock()

	// 이름도 안 붙이고 결과도 없고 편집도 안 한 빈 판은 버린다.
	// 그 외에는 남겨서 나중에 다시 열어도 그대로 보이게 한다.
	if empty && !keep {
		h.mu.Lock()
		if h.rooms[room.name] == room {
			room.mu.Lock()
			if len(room.clients) == 0 && !room.worthKeeping() {
				delete(h.rooms, room.name)
			}
			room.mu.Unlock()
		}
		h.mu.Unlock()
	}
}

// worthKeeping: 저장/유지할 가치가 있는 판인지(호출자가 room.mu 를 들고 있어야 한다).
// 결과가 있거나, 명단/항목을 고쳤거나, 방 이름을 지어준 판.
func (r *Room) worthKeeping() bool {
	return r.draw != nil || r.custom || (r.title != "" && r.title != defaultTitle)
}

// ---- 서버 → 클라이언트 ----

type stateMsg struct {
	Type    string   `json:"type"`  // "state"
	Room    string   `json:"room"`  // 판 키(= URL 경로)
	Title   string   `json:"title"` // 방 이름(표시용)
	Members []string `json:"members"`
	Pool    []Prize  `json:"pool"`
	Viewers int      `json:"viewers"`
	Round   int      `json:"round"`
	Custom  bool     `json:"custom,omitempty"` // 명단/항목을 화면에서 고친 판인지

	Draw    []int `json:"draw,omitempty"`    // 멤버별 뽑은 항목(-1 은 꽝)
	DrawnAt int64 `json:"drawnAt,omitempty"` // 뽑은 시각(unix ms) — 표시용
	// Elapsed: 뽑은 뒤 지난 시간(ms). 서버 기준이라 늦게 들어온 사람은 이미 다 공개된 화면을 본다.
	Elapsed int64 `json:"elapsed,omitempty"`
}

func (r *Room) broadcastLocked() {
	m := stateMsg{
		Type:    "state",
		Room:    r.name,
		Title:   r.title,
		Members: r.members,
		Pool:    r.pool,
		Viewers: len(r.clients),
		Round:   r.round,
		Custom:  r.custom,
	}
	if r.draw != nil {
		m.Draw = r.draw
		m.DrawnAt = r.drawnAt
		m.Elapsed = time.Now().UnixMilli() - r.drawnAt
	}
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	for c := range r.clients {
		select {
		case c.send <- data:
		default:
			c.cancel() // 느린 소비자 — 정리는 해당 고루틴에서
		}
	}
}

func (r *Room) notify(c *Client, text string) {
	data, err := json.Marshal(map[string]string{"type": "notice", "text": text})
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

// ---- 클라이언트 → 서버 ----

type inMsg struct {
	Type    string   `json:"type"` // config|title|reload|draw|reset
	Title   string   `json:"title,omitempty"`
	Members []string `json:"members,omitempty"`
	Pool    []Prize  `json:"pool,omitempty"`
}

func (c *Client) readPump(ctx context.Context, room *Room, store *Store) {
	c.conn.SetReadLimit(readLimit)
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var in inMsg
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		room.handle(c, in, store)
	}
}

func (r *Room) handle(c *Client, in inMsg, store *Store) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch in.Type {
	case "config": // 명단/항목 변경 — 뽑기 전에만
		if r.draw != nil {
			r.notify(c, "이미 뽑았어요. 「다시 뽑기」를 누른 뒤에 바꿀 수 있어요.")
			return
		}
		if in.Members != nil {
			mem := sanitizeMembers(in.Members)
			if len(mem) < 2 {
				r.notify(c, "명단은 두 명 이상이어야 해요.")
				return
			}
			r.members = mem
			r.custom = true
		}
		if in.Pool != nil {
			pool := sanitizePool(in.Pool)
			if len(pool) == 0 {
				r.notify(c, "항목을 한 줄에 하나씩 적어주세요.")
				return
			}
			r.pool = pool
			r.custom = true
		}

	case "title": // 방 이름 지정/변경
		title := sanitize(in.Title, maxTitleLen)
		if title == "" || title == r.title {
			return
		}
		r.title = title

	case "reload": // 명단·항목을 서버 기본값으로 되돌리기 — 뽑기 전에만
		if r.draw != nil {
			r.notify(c, "이미 뽑았어요. 「다시 뽑기」를 누른 뒤에 바꿀 수 있어요.")
			return
		}
		if len(r.base) < 2 {
			r.notify(c, "서버에 기본 명단이 없어요.")
			return
		}
		r.members = r.base
		r.pool = defaultPool()
		r.custom = false

	case "draw": // 제비 뽑기
		if r.draw != nil {
			return // 이미 뽑음 — 결과 보존
		}
		if len(r.members) < 2 {
			r.notify(c, "명단이 비어 있어요(서버의 members 파일을 확인해 주세요).")
			return
		}
		r.drawLocked()

	case "reset": // 다시 뽑기 — 결과를 지운다
		if r.draw == nil {
			return
		}
		r.draw, r.drawnAt = nil, 0
		// 화면에서 손댄 판은 그 명단/항목을 유지하고, 손대지 않은 판은 최신 기본값으로 갈아탄다.
		if !r.custom && len(r.base) > 0 {
			r.members = r.base
			r.pool = defaultPool()
		}

	default:
		return
	}
	r.broadcastLocked()
	store.put(r) // 결과 변경을 파일에 반영(디바운스 저장)
}

// drawLocked: 항목 인원만큼 제비를 만들어 섞고 명단 순서대로 나눠준다.
//
//	자리 > 인원 → 섞은 뒤 잘라낸다(남는 자리는 이번 판에서 빠짐)
//	자리 < 인원 → 모자란 만큼 꽝(-1)
func (r *Room) drawLocked() {
	n := len(r.members)
	rnd := newRand()

	draw := make([]int, 0, n)
	for i, p := range r.pool {
		for s := 0; s < p.Seats; s++ {
			draw = append(draw, i)
		}
	}
	rnd.Shuffle(len(draw), func(i, j int) { draw[i], draw[j] = draw[j], draw[i] })
	if len(draw) > n {
		draw = draw[:n]
	}
	for len(draw) < n {
		draw = append(draw, -1)
	}
	rnd.Shuffle(n, func(i, j int) { draw[i], draw[j] = draw[j], draw[i] })

	r.draw = draw
	r.drawnAt = time.Now().UnixMilli()
	r.round++
}

// newRand: 뽑기이므로 시드는 crypto/rand 에서 받는다(예측 불가).
func newRand() *rand.Rand {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(b[:]))))
}

// ---- 결과 저장 ----
//
// 판별로 명단/항목/결과를 JSON 파일에 저장한다. 저장은 디바운스(약 2초)해서 몰아 쓴다.
// 경로가 비면 메모리에만 유지(휘발성).

type roomSnap struct {
	Title     string   `json:"title,omitempty"`
	UpdatedAt int64    `json:"updatedAt,omitempty"`
	Members   []string `json:"members"`
	Pool      []Prize  `json:"pool"`
	Custom    bool     `json:"custom,omitempty"`
	Draw      []int    `json:"draw,omitempty"`
	DrawnAt   int64    `json:"drawnAt,omitempty"`
	Round     int      `json:"round"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	data  map[string]roomSnap
	dirty chan struct{}
}

func loadStore(path string) *Store {
	s := &Store{path: path, data: make(map[string]roomSnap), dirty: make(chan struct{}, 1)}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &s.data)
		}
	}
	go s.saver()
	return s
}

// drop: 저장 목록에서 판 하나를 지운다.
func (s *Store) drop(name string) {
	s.mu.Lock()
	delete(s.data, name)
	s.mu.Unlock()
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *Store) all() map[string]roomSnap {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]roomSnap, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out
}

// put: 판 상태를 저장 대상에 반영한다(호출자가 room.mu 를 들고 있어야 한다).
// 결과가 있거나 화면에서 명단/항목을 고친 판만 저장한다(손대지 않은 빈 판은 저장할 게 없음).
func (s *Store) put(r *Room) {
	s.mu.Lock()
	if !r.worthKeeping() {
		delete(s.data, r.name)
	} else {
		s.data[r.name] = roomSnap{
			Title: r.title, UpdatedAt: time.Now().UnixMilli(),
			Members: r.members, Pool: r.pool, Custom: r.custom,
			Draw: r.draw, DrawnAt: r.drawnAt, Round: r.round,
		}
	}
	s.mu.Unlock()
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *Store) saver() {
	if s.path == "" {
		for range s.dirty {
		}
		return
	}
	for range s.dirty {
		time.Sleep(2 * time.Second)
		select {
		case <-s.dirty:
		default:
		}
		s.save()
	}
}

func (s *Store) save() {
	s.mu.Lock()
	data, err := json.Marshal(s.data)
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// ---- WebSocket / HTTP ----

func (c *Client) writePump(ctx context.Context) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeWait)
			err := c.conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, writeWait)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func serveWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	roomName := sanitize(r.URL.Query().Get("room"), maxRoomLen)
	if roomName == "" {
		roomName = defaultRoom
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	client := &Client{conn: conn, send: make(chan []byte, sendBuffer), cancel: cancel}
	room := hub.join(roomName, client)
	defer hub.leave(room, client)

	go client.writePump(ctx)
	client.readPump(ctx, room, hub.store)
}

func newMux(hub *Hub, staticDir string) *http.ServeMux {
	indexPath := filepath.Join(staticDir, "index.html")

	mux := http.NewServeMux()
	mux.HandleFunc("/_ws", func(w http.ResponseWriter, r *http.Request) { serveWS(hub, w, r) })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, indexPath)
	})
	return mux
}

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8770"
		}
		addr = ":" + port
	}
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}
	membersFile := os.Getenv("MEMBERS_FILE")
	if membersFile == "" {
		membersFile = "./members.md"
	}
	drawDB := os.Getenv("DRAW_DB")
	if drawDB == "" {
		if sd := os.Getenv("STATE_DIRECTORY"); sd != "" {
			drawDB = filepath.Join(sd, "draws.json")
		}
	}

	members := loadMembers(membersFile)
	store := loadStore(drawDB)
	hub := NewHub(members, store)

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(hub, staticDir),
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout 은 웹소켓 장기 연결을 끊어버리므로 설정하지 않는다.
	}

	log.Printf("asdof-random listening on %s (static=%s, members=%d명, drawDB=%q)",
		addr, staticDir, len(members), drawDB)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// ---- 유틸 ----

func sanitize(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return clip(strings.TrimSpace(s), max)
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
