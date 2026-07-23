// anonchat — 초경량 익명 채팅 + 파일 릴레이 서버
//
// 특징:
//   - 단일 바이너리. 외부 의존성은 WebSocket 라이브러리 하나뿐.
//   - 완전 휘발성: 메시지/방을 메모리에만 유지. DB 없음. 재시작하면 초기화.
//   - 경로가 곧 방 이름: /rd1 → 방 "rd1". 루트(/)는 방 선택 화면.
//   - 파일 전송은 서버에 쌓지 않고 청크 단위로 흘려보내는 릴레이(sender→server→receiver).
//     서버는 전송당 청크 하나 정도만 메모리에 들고, TCP 백프레셔로 흐름을 제어한다.
//   - 같은 바이너리가 static/index.html 도 서빙하므로 nginx 외 별도 프로세스가 불필요.
//
// 라우트:
//   /_ws?room=&nick=      채팅 WebSocket (텍스트 + 파일 시그널링)
//   /_file?id=&role=      파일 릴레이 WebSocket (바이너리 pass-through)
//   /healthz              헬스체크
//   그 외 모든 경로        static/index.html (경로 자체가 방 이름)
//
// 환경 변수:
//   LISTEN_ADDR  바인딩 주소(예: 127.0.0.1:8080). 없으면 :PORT
//   PORT         기본 8080
//   STATIC_DIR   정적 파일 디렉토리 (기본 ./static)
//   SCORE_DB     핀볼 순위판 저장 파일. 없으면 STATE_DIRECTORY/scores.json, 그것도 없으면 메모리만(휘발성).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	sendBuffer     = 32              // 클라이언트당 송신 버퍼. 넘치면 '느린 소비자'로 보고 연결 종료.
	maxTextLen     = 1000            // 메시지 최대 길이(문자 수)
	maxNickLen     = 24              // 닉네임 최대 길이
	maxRoomLen     = 60              // 방 이름 최대 길이
	maxIDLen       = 64              // 파일 전송 ID 최대 길이
	topN           = 100                     // 방별 순위판 보관 개수(핀볼 경쟁 모드)
	maxScore       = 1_000_000_000_000_000   // 점수 상한(비정상 값 차단)
	readLimit      = 8192            // 채팅 수신 프레임 최대 바이트(파일 시그널 여유 포함)
	fileChunkLimit = 1 << 20         // 파일 릴레이 청크 최대 바이트(1MB)
	writeWait      = 10 * time.Second
	fileWriteWait  = 60 * time.Second
	pairTimeout    = 60 * time.Second // 상대 피어 접속 대기 한도
	pingPeriod     = 45 * time.Second // 유휴 연결 유지 및 죽은 연결 감지용 ping 주기
)

// ---- 메시지 포맷 (서버 ↔ 클라이언트, 채팅 소켓) ----

type Message struct {
	Type       string `json:"type"`                 // chat|system|file-offer|file-accept|file-cancel
	Nick       string `json:"nick,omitempty"`       // 발신자
	Text       string `json:"text,omitempty"`       // 본문
	Count      int    `json:"count,omitempty"`      // 현재 방 인원
	Time       int64  `json:"time,omitempty"`       // unix millis
	TransferID string `json:"transferId,omitempty"` // 파일 전송 식별자
	Name       string `json:"name,omitempty"`       // 파일명
	Size       int64  `json:"size,omitempty"`       // 파일 크기(byte)
	Mime       string `json:"mime,omitempty"`       // MIME 타입
	Score      int64        `json:"score,omitempty"`   // 점수(핀볼 경쟁 모드)
	Final      bool         `json:"final,omitempty"`   // 게임오버 확정 점수 여부
	Entries    []ScoreEntry `json:"entries,omitempty"` // 순위판(type:"board")
}

// ---- 채팅 허브 / 방 / 클라이언트 ----

type Hub struct {
	mu    sync.Mutex
	rooms map[string]*Room
}

type Room struct {
	name    string
	mu      sync.Mutex
	clients map[*Client]struct{}
}

type Client struct {
	conn   *websocket.Conn
	nick   string
	send   chan []byte
	cancel context.CancelFunc
}

func NewHub() *Hub { return &Hub{rooms: make(map[string]*Room)} }

func (h *Hub) join(name string, c *Client) *Room {
	h.mu.Lock()
	room := h.rooms[name]
	if room == nil {
		room = &Room{name: name, clients: make(map[*Client]struct{})}
		h.rooms[name] = room
	}
	room.mu.Lock()
	room.clients[c] = struct{}{}
	n := len(room.clients)
	room.mu.Unlock()
	h.mu.Unlock()

	room.broadcast(Message{Type: "system", Text: c.nick + " 님이 입장했어요", Count: n})
	return room
}

func (h *Hub) leave(room *Room, c *Client) {
	h.mu.Lock()
	room.mu.Lock()
	if _, ok := room.clients[c]; ok {
		delete(room.clients, c)
	}
	n := len(room.clients)
	if n == 0 && h.rooms[room.name] == room {
		delete(h.rooms, room.name)
	}
	room.mu.Unlock()
	h.mu.Unlock()

	if n > 0 {
		room.broadcast(Message{Type: "system", Text: c.nick + " 님이 나갔어요", Count: n})
	}
}

// broadcast: 방의 모든 클라이언트에게 논블로킹 전송. 버퍼가 꽉 찬 느린 클라이언트는
// cancel()로 정리를 예약한다(실제 leave 정리는 해당 클라이언트 고루틴에서 발생).
func (r *Room) broadcast(m Message) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	r.mu.Lock()
	for c := range r.clients {
		select {
		case c.send <- data:
		default:
			c.cancel()
		}
	}
	r.mu.Unlock()
}

// ---- 점수판 (핀볼 경쟁 모드: 방별 영구 순위) ----
//
// 방 이름별로 플레이어(nick)당 최고 점수를 top-N 만큼 유지하고 JSON 파일로 영속화한다.
// 저장은 디바운스(약 2초)해서 몰아 쓴다. 경로가 비면 메모리에만 유지(휘발성).

type ScoreEntry struct {
	Nick  string `json:"nick"`
	Score int64  `json:"score"`
	Time  int64  `json:"time"`
}

type Board struct {
	mu    sync.Mutex
	rooms map[string][]ScoreEntry // 방 → 점수 내림차순 정렬, top-N
	path  string
	dirty chan struct{}
}

func loadBoard(path string) *Board {
	b := &Board{rooms: make(map[string][]ScoreEntry), path: path, dirty: make(chan struct{}, 1)}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &b.rooms)
		}
	}
	go b.saver()
	return b
}

// record: 방에 점수 반영(플레이어별 최고점만 유지). 순위가 바뀌면 true.
func (b *Board) record(room string, e ScoreEntry) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.rooms[room]
	idx := -1
	for i := range list {
		if list[i].Nick == e.Nick {
			idx = i
			break
		}
	}
	changed := false
	if idx >= 0 {
		if e.Score > list[idx].Score {
			list[idx] = e
			changed = true
		}
	} else {
		list = append(list, e)
		changed = true
	}
	if !changed {
		return false
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Score > list[j].Score })
	if len(list) > topN {
		list = list[:topN]
	}
	b.rooms[room] = list
	b.markDirty()
	return true
}

func (b *Board) message(room string) Message {
	b.mu.Lock()
	entries := append([]ScoreEntry(nil), b.rooms[room]...)
	b.mu.Unlock()
	return Message{Type: "board", Entries: entries}
}

// sendTo: 방의 현재 순위판을 특정 클라이언트에게 직접 전송(입장 직후 1회).
func (b *Board) sendTo(c *Client, room string) {
	b.mu.Lock()
	has := len(b.rooms[room]) > 0
	b.mu.Unlock()
	if !has {
		return
	}
	if data, err := json.Marshal(b.message(room)); err == nil {
		select {
		case c.send <- data:
		default:
		}
	}
}

func (b *Board) markDirty() {
	select {
	case b.dirty <- struct{}{}:
	default:
	}
}

// saver: dirty 신호를 받으면 잠깐 모았다가(디바운스) 파일에 저장. 경로 없으면 신호만 소비.
func (b *Board) saver() {
	if b.path == "" {
		for range b.dirty {
		}
		return
	}
	for range b.dirty {
		time.Sleep(2 * time.Second)
		select {
		case <-b.dirty:
		default:
		}
		b.save()
	}
}

func (b *Board) save() {
	b.mu.Lock()
	data, err := json.Marshal(b.rooms)
	b.mu.Unlock()
	if err != nil {
		return
	}
	tmp := b.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, b.path)
}

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

// readPump: 클라이언트가 보낸 제어 메시지를 처리한다.
//   - 일반 채팅({"text":...} 또는 type:"chat")  → 방에 브로드캐스트
//   - file-offer / file-accept / file-cancel     → 방에 그대로 브로드캐스트(시그널링)
func (c *Client) readPump(ctx context.Context, room *Room, board *Board) {
	c.conn.SetReadLimit(readLimit)
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var in Message
		if json.Unmarshal(data, &in) != nil {
			continue
		}
		switch in.Type {
		case "", "chat":
			text := clip(strings.TrimSpace(in.Text), maxTextLen)
			if text == "" {
				continue
			}
			room.broadcast(Message{Type: "chat", Nick: c.nick, Text: text, Time: time.Now().UnixMilli()})

		case "score":
			// 핀볼 경쟁 모드: 점수를 방에 중계하고, final(게임오버 확정)이면 순위판에 반영.
			if in.Score < 0 || in.Score > maxScore {
				continue
			}
			now := time.Now().UnixMilli()
			room.broadcast(Message{Type: "score", Nick: c.nick, Score: in.Score, Final: in.Final, Time: now})
			if in.Final && board.record(room.name, ScoreEntry{Nick: c.nick, Score: in.Score, Time: now}) {
				room.broadcast(board.message(room.name))
			}

		case "file-offer":
			id := sanitize(in.TransferID, maxIDLen)
			name := sanitize(in.Name, 255)
			if id == "" || name == "" || in.Size < 0 {
				continue
			}
			room.broadcast(Message{
				Type: "file-offer", Nick: c.nick, TransferID: id,
				Name: name, Size: in.Size, Mime: sanitize(in.Mime, 128),
				Time: time.Now().UnixMilli(),
			})

		case "file-accept":
			id := sanitize(in.TransferID, maxIDLen)
			if id == "" {
				continue
			}
			room.broadcast(Message{Type: "file-accept", Nick: c.nick, TransferID: id})

		case "file-cancel":
			id := sanitize(in.TransferID, maxIDLen)
			if id == "" {
				continue
			}
			room.broadcast(Message{Type: "file-cancel", Nick: c.nick, TransferID: id})
		}
	}
}

func serveWS(hub *Hub, board *Board, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	room := sanitize(r.URL.Query().Get("room"), maxRoomLen)
	if room == "" {
		room = "lobby"
	}
	nick := sanitize(r.URL.Query().Get("nick"), maxNickLen)
	if nick == "" {
		nick = "익명"
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	client := &Client{conn: conn, nick: nick, send: make(chan []byte, sendBuffer), cancel: cancel}
	rm := hub.join(room, client)
	defer hub.leave(rm, client)

	go client.writePump(ctx)
	board.sendTo(client, room) // 입장 시 저장된 순위판 1회 전송
	client.readPump(ctx, rm, board)
}

// ---- 파일 릴레이 ----

type fileTransfer struct {
	mu        sync.Mutex
	sender    *websocket.Conn
	receiver  *websocket.Conn
	ready     chan struct{} // sender/receiver 둘 다 접속하면 close
	done      chan struct{} // 전송 종료(완료/중단) 시 close
	readyOnce sync.Once
	doneOnce  sync.Once
}

func (t *fileTransfer) markReady() { t.readyOnce.Do(func() { close(t.ready) }) }
func (t *fileTransfer) markDone()  { t.doneOnce.Do(func() { close(t.done) }) }

type FileHub struct {
	mu        sync.Mutex
	transfers map[string]*fileTransfer
}

func NewFileHub() *FileHub { return &FileHub{transfers: make(map[string]*fileTransfer)} }

func (h *FileHub) get(id string) *fileTransfer {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.transfers[id]
	if t == nil {
		t = &fileTransfer{ready: make(chan struct{}), done: make(chan struct{})}
		h.transfers[id] = t
	}
	return t
}

func (h *FileHub) remove(id string) {
	h.mu.Lock()
	delete(h.transfers, id)
	h.mu.Unlock()
}

func serveFile(fh *FileHub, w http.ResponseWriter, r *http.Request) {
	id := sanitize(r.URL.Query().Get("id"), maxIDLen)
	role := r.URL.Query().Get("role")
	if id == "" || (role != "send" && role != "recv") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	t := fh.get(id)

	// 역할별 conn 등록. 같은 역할이 이미 있으면 거절(전송당 1:1).
	t.mu.Lock()
	switch role {
	case "send":
		if t.sender != nil {
			t.mu.Unlock()
			conn.Close(websocket.StatusPolicyViolation, "sender already connected")
			return
		}
		t.sender = conn
	case "recv":
		if t.receiver != nil {
			t.mu.Unlock()
			conn.Close(websocket.StatusPolicyViolation, "receiver already connected")
			return
		}
		t.receiver = conn
	}
	both := t.sender != nil && t.receiver != nil
	t.mu.Unlock()
	if both {
		t.markReady()
	}

	ctx := r.Context()
	select {
	case <-t.ready:
	case <-ctx.Done():
		return
	case <-time.After(pairTimeout):
		conn.Close(websocket.StatusGoingAway, "peer timeout")
		fh.remove(id)
		return
	}

	if role == "send" {
		relayFile(ctx, t)
		t.markDone()
		// 마지막 청크까지 전달됐으면 receiver를 정상 종료(EOF 신호)
		t.mu.Lock()
		rc := t.receiver
		t.mu.Unlock()
		if rc != nil {
			rc.Close(websocket.StatusNormalClosure, "eof")
		}
		fh.remove(id)
	} else {
		// receiver: 전송이 끝날 때까지 conn을 살려둔다(릴레이는 sender 고루틴이 수행).
		select {
		case <-t.done:
		case <-ctx.Done():
		}
	}
}

// relayFile: sender 소켓에서 바이너리 청크를 읽어 receiver 소켓으로 즉시 흘려보낸다.
// 청크를 하나 쓴 뒤에야 다음 청크를 읽으므로, 서버는 전송당 청크 하나 정도만 메모리에 든다.
// receiver가 느리면 Write가 블록되고 → sender에서 다음 Read가 지연되어 TCP 백프레셔가 걸린다.
func relayFile(ctx context.Context, t *fileTransfer) {
	t.sender.SetReadLimit(fileChunkLimit)
	for {
		typ, data, err := t.sender.Read(ctx)
		if err != nil {
			return // EOF/close/에러 → 전송 종료
		}
		if typ != websocket.MessageBinary {
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, fileWriteWait)
		err = t.receiver.Write(wctx, websocket.MessageBinary, data)
		cancel()
		if err != nil {
			return
		}
	}
}

// ---- HTTP ----

func newMux(hub *Hub, board *Board, staticDir string) *http.ServeMux {
	fileHub := NewFileHub()
	indexPath := filepath.Join(staticDir, "index.html")

	mux := http.NewServeMux()
	mux.HandleFunc("/_ws", func(w http.ResponseWriter, r *http.Request) { serveWS(hub, board, w, r) })
	mux.HandleFunc("/_file", func(w http.ResponseWriter, r *http.Request) { serveFile(fileHub, w, r) })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// 그 외 모든 경로 → index.html (경로가 곧 방 이름이므로 SPA처럼 항상 같은 페이지를 준다)
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
			port = "8080"
		}
		addr = ":" + port
	}
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}

	// 점수판 영속화 경로: SCORE_DB, 없으면 systemd StateDirectory 아래(scores.json), 그것도 없으면 메모리만.
	scoreDB := os.Getenv("SCORE_DB")
	if scoreDB == "" {
		if sd := os.Getenv("STATE_DIRECTORY"); sd != "" {
			scoreDB = filepath.Join(sd, "scores.json")
		}
	}
	board := loadBoard(scoreDB)

	hub := NewHub()
	mux := newMux(hub, board, staticDir)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout은 웹소켓 장기 연결을 끊어버리므로 설정하지 않는다.
	}

	log.Printf("anonchat listening on %s (static=%s, scoreDB=%q)", addr, staticDir, scoreDB)
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
