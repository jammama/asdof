package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func dial(t *testing.T, base, room, nick string) *websocket.Conn {
	t.Helper()
	url := strings.Replace(base, "http", "ws", 1) + "/_ws?room=" + room + "&nick=" + nick
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial(%s): %v", nick, err)
	}
	return c
}

func readMsg(t *testing.T, c *websocket.Conn) Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func waitFor(t *testing.T, c *websocket.Conn, pred func(Message) bool) Message {
	t.Helper()
	for i := 0; i < 10; i++ {
		m := readMsg(t, c)
		if pred(m) {
			return m
		}
	}
	t.Fatal("expected message not received")
	return Message{}
}

func TestBroadcastAndPresence(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	alice := dial(t, srv.URL, "testroom", "alice")
	defer alice.CloseNow()
	m := waitFor(t, alice, func(m Message) bool { return m.Type == "system" })
	if m.Count != 1 {
		t.Fatalf("alice join: count=%d, want 1", m.Count)
	}

	bob := dial(t, srv.URL, "testroom", "bob")
	defer bob.CloseNow()
	m = waitFor(t, alice, func(m Message) bool { return m.Type == "system" && m.Count == 2 })
	if !strings.Contains(m.Text, "bob") {
		t.Fatalf("expected bob join notice, got %q", m.Text)
	}

	if err := bob.Write(context.Background(), websocket.MessageText,
		[]byte(`{"text":"hello world"}`)); err != nil {
		t.Fatalf("bob write: %v", err)
	}
	got := waitFor(t, alice, func(m Message) bool { return m.Type == "chat" })
	if got.Nick != "bob" || got.Text != "hello world" {
		t.Fatalf("alice got chat %+v, want nick=bob text=hello world", got)
	}
	if got.Time == 0 {
		t.Fatalf("chat message missing timestamp")
	}
}

func TestRoomsAreIsolated(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	a := dial(t, srv.URL, "roomA", "a")
	defer a.CloseNow()
	waitFor(t, a, func(m Message) bool { return m.Type == "system" })

	b := dial(t, srv.URL, "roomB", "b")
	defer b.CloseNow()
	waitFor(t, b, func(m Message) bool { return m.Type == "system" })

	if err := b.Write(context.Background(), websocket.MessageText,
		[]byte(`{"text":"secret"}`)); err != nil {
		t.Fatalf("b write: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	for {
		_, data, err := a.Read(ctx)
		if err != nil {
			break
		}
		var m Message
		json.Unmarshal(data, &m)
		if m.Type == "chat" {
			t.Fatalf("room isolation broken: roomA received %q from roomB", m.Text)
		}
	}
}

func TestEmptyRoomIsReaped(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	c := dial(t, srv.URL, "ephemeral", "x")
	waitFor(t, c, func(m Message) bool { return m.Type == "system" })

	hub.mu.Lock()
	_, exists := hub.rooms["ephemeral"]
	hub.mu.Unlock()
	if !exists {
		t.Fatal("room should exist while a client is connected")
	}

	c.Close(websocket.StatusNormalClosure, "bye")

	reaped := false
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		hub.mu.Lock()
		_, ok := hub.rooms["ephemeral"]
		hub.mu.Unlock()
		if !ok {
			reaped = true
			break
		}
	}
	if !reaped {
		t.Fatal("empty room was not reaped from hub")
	}
}

// file-offer / file-accept 시그널이 방 안에서 그대로 중계되는지.
func TestFileSignaling(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	sender := dial(t, srv.URL, "froom", "sender")
	defer sender.CloseNow()
	waitFor(t, sender, func(m Message) bool { return m.Type == "system" })

	receiver := dial(t, srv.URL, "froom", "receiver")
	defer receiver.CloseNow()
	waitFor(t, receiver, func(m Message) bool { return m.Type == "system" && m.Count == 2 })

	offer := `{"type":"file-offer","transferId":"tf-1","name":"a.bin","size":12345,"mime":"application/octet-stream"}`
	if err := sender.Write(context.Background(), websocket.MessageText, []byte(offer)); err != nil {
		t.Fatalf("offer write: %v", err)
	}
	got := waitFor(t, receiver, func(m Message) bool { return m.Type == "file-offer" })
	if got.TransferID != "tf-1" || got.Name != "a.bin" || got.Size != 12345 {
		t.Fatalf("receiver got bad offer: %+v", got)
	}
	if got.Nick != "sender" {
		t.Fatalf("offer nick=%q, want sender", got.Nick)
	}
}

// 실제 파일 릴레이: sender가 보낸 바이트가 정확히 receiver로 전달되는지.
func TestFileRelay(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	base := strings.Replace(srv.URL, "http", "ws", 1)
	ctx := context.Background()

	recv, _, err := websocket.Dial(ctx, base+"/_file?id=tX&role=recv", nil)
	if err != nil {
		t.Fatalf("recv dial: %v", err)
	}
	defer recv.CloseNow()
	recv.SetReadLimit(fileChunkLimit)

	send, _, err := websocket.Dial(ctx, base+"/_file?id=tX&role=send", nil)
	if err != nil {
		t.Fatalf("send dial: %v", err)
	}
	defer send.CloseNow()

	// 250KB를 100KB 청크 3개로 쪼개 전송
	payload := bytes.Repeat([]byte("Kimchi🍚"), 30000) // 임의 멀티바이트 페이로드
	const chunk = 100 * 1024
	go func() {
		for off := 0; off < len(payload); off += chunk {
			end := off + chunk
			if end > len(payload) {
				end = len(payload)
			}
			if err := send.Write(ctx, websocket.MessageBinary, payload[off:end]); err != nil {
				return
			}
		}
		send.Close(websocket.StatusNormalClosure, "eof")
	}()

	var got []byte
	for {
		typ, data, err := recv.Read(ctx)
		if err != nil {
			break // 서버가 sender EOF 후 receiver를 정상 종료
		}
		if typ == websocket.MessageBinary {
			got = append(got, data...)
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("relayed bytes mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// 같은 전송 ID에 receiver가 둘 붙으면 두 번째는 거절되는지.
func TestFileSecondReceiverRejected(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	base := strings.Replace(srv.URL, "http", "ws", 1)
	ctx := context.Background()

	r1, _, err := websocket.Dial(ctx, base+"/_file?id=dup&role=recv", nil)
	if err != nil {
		t.Fatalf("r1 dial: %v", err)
	}
	defer r1.CloseNow()

	r2, _, err := websocket.Dial(ctx, base+"/_file?id=dup&role=recv", nil)
	if err != nil {
		t.Fatalf("r2 dial: %v", err)
	}
	defer r2.CloseNow()

	// r2는 곧 policy violation으로 닫혀야 한다.
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _, err = r2.Read(rctx)
	if err == nil {
		t.Fatal("second receiver should have been rejected/closed")
	}
}

// 점수 이벤트가 방에 중계되고, final 이면 순위판(board)이 갱신·브로드캐스트되는지.
func TestScoreRelayAndBoard(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(newMux(hub, loadBoard(""), "./static"))
	defer srv.Close()

	dash := dial(t, srv.URL, "proom", "board")
	defer dash.CloseNow()
	waitFor(t, dash, func(m Message) bool { return m.Type == "system" })

	player := dial(t, srv.URL, "proom", "P1")
	defer player.CloseNow()
	waitFor(t, dash, func(m Message) bool { return m.Type == "system" && m.Count == 2 })

	// live 점수
	if err := player.Write(context.Background(), websocket.MessageText,
		[]byte(`{"type":"score","score":100,"final":false}`)); err != nil {
		t.Fatalf("player live write: %v", err)
	}
	live := waitFor(t, dash, func(m Message) bool { return m.Type == "score" })
	if live.Nick != "P1" || live.Score != 100 || live.Final {
		t.Fatalf("live score = %+v, want nick=P1 score=100 final=false", live)
	}

	// final 점수 → score(final) + board 브로드캐스트
	if err := player.Write(context.Background(), websocket.MessageText,
		[]byte(`{"type":"score","score":500,"final":true}`)); err != nil {
		t.Fatalf("player final write: %v", err)
	}
	board := waitFor(t, dash, func(m Message) bool { return m.Type == "board" })
	if len(board.Entries) != 1 || board.Entries[0].Nick != "P1" || board.Entries[0].Score != 500 {
		t.Fatalf("board = %+v, want [P1:500]", board.Entries)
	}
}

// 입장 시 저장된 순위판이 새 클라이언트에게 전송되는지.
func TestBoardSentOnJoin(t *testing.T) {
	hub := NewHub()
	board := loadBoard("")
	board.record("proom", ScoreEntry{Nick: "veteran", Score: 9999})
	srv := httptest.NewServer(newMux(hub, board, "./static"))
	defer srv.Close()

	c := dial(t, srv.URL, "proom", "newcomer")
	defer c.CloseNow()
	got := waitFor(t, c, func(m Message) bool { return m.Type == "board" })
	if len(got.Entries) != 1 || got.Entries[0].Nick != "veteran" || got.Entries[0].Score != 9999 {
		t.Fatalf("join board = %+v, want [veteran:9999]", got.Entries)
	}
}

// 순위판은 플레이어별 최고점만 유지하고 내림차순 정렬한다.
func TestBoardRecordBestPerNick(t *testing.T) {
	b := loadBoard("")
	if !b.record("r", ScoreEntry{Nick: "P1", Score: 100}) {
		t.Fatal("first record should change board")
	}
	if b.record("r", ScoreEntry{Nick: "P1", Score: 50}) {
		t.Fatal("lower score must not replace best")
	}
	if !b.record("r", ScoreEntry{Nick: "P1", Score: 200}) {
		t.Fatal("higher score should update")
	}
	b.record("r", ScoreEntry{Nick: "P2", Score: 150})
	e := b.message("r").Entries
	if len(e) != 2 || e[0].Nick != "P1" || e[0].Score != 200 || e[1].Nick != "P2" {
		t.Fatalf("board = %+v, want [P1:200, P2:150]", e)
	}
}

// 파일 경로가 있으면 순위판이 저장되고 재기동 시 복원되는지.
func TestBoardPersistence(t *testing.T) {
	path := t.TempDir() + "/scores.json"
	b1 := loadBoard(path)
	b1.record("r", ScoreEntry{Nick: "P1", Score: 777})
	b1.save() // 디바운스 우회, 즉시 저장

	b2 := loadBoard(path)
	e := b2.message("r").Entries
	if len(e) != 1 || e[0].Nick != "P1" || e[0].Score != 777 {
		t.Fatalf("reloaded board = %+v, want [P1:777]", e)
	}
}
