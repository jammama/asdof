package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testRoom(members []string, pool []Prize) *Room {
	return &Room{name: "t", clients: make(map[*Client]struct{}), members: members, pool: pool}
}

func members(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('가' + i))
	}
	return out
}

// 자리 합에 따른 배분: 딱 맞음 / 부족(꽝) / 남음(자리 제외)
func TestDrawSeatFitting(t *testing.T) {
	cases := []struct {
		name string
		pool []Prize
		n    int
		want map[int]int // 비어 있으면 "꽝이 없어야 함"만 확인
	}{
		{"딱 맞음", []Prize{{"A", 3}, {"B", 2}}, 5, map[int]int{0: 3, 1: 2}},
		{"부족", []Prize{{"A", 2}}, 5, map[int]int{0: 2, -1: 3}},
		{"딱 맞음(기본 항목)", defaultPool(), 5, map[int]int{0: 3, 1: 2}},
		{"남음", []Prize{{"A", 4}, {"B", 4}}, 6, map[int]int{}},
	}
	for _, c := range cases {
		r := testRoom(members(c.n), c.pool)
		r.drawLocked()
		if len(r.draw) != c.n {
			t.Fatalf("%s: draw=%d, 기대 %d", c.name, len(r.draw), c.n)
		}
		got := map[int]int{}
		for _, g := range r.draw {
			got[g]++
		}
		if len(c.want) > 0 {
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("%s: 배정 %v, 기대 %v", c.name, got, c.want)
				}
			}
		} else if got[-1] != 0 {
			t.Fatalf("%s: 자리가 남는데 꽝이 %d명", c.name, got[-1])
		}
	}
	if totalSeats(defaultPool()) != 5 {
		t.Fatalf("기본 항목 자리 합 %d, 기대 5", totalSeats(defaultPool()))
	}
}

// 명단 순서에 따른 유불리가 없어야 한다(확률 = 자리수/인원).
func TestDrawIsUniform(t *testing.T) {
	const n, iters = 6, 12000
	hits := make([]int, n)
	for i := 0; i < iters; i++ {
		r := testRoom(members(n), []Prize{{"당첨", 1}})
		r.drawLocked()
		for k, g := range r.draw {
			if g == 0 {
				hits[k]++
			}
		}
	}
	exp := float64(iters) / n
	for i, h := range hits {
		if d := float64(h)/exp - 1; d < -0.12 || d > 0.12 {
			t.Fatalf("%d번째 멤버 당첨 %d회(기대 %.0f) — 편향 의심: %v", i, h, exp, hits)
		}
	}
}

func TestLoadMembers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "members.md")
	os.WriteFile(path, []byte("임언호\n\n박도겸\n# 주석\n- 김유미\n  \n김유경"), 0o644)
	got := loadMembers(path)
	want := []string{"임언호", "박도겸", "김유미", "김유경"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("명단 %v, 기대 %v", got, want)
	}
	if loadMembers(filepath.Join(dir, "없음.md")) != nil {
		t.Fatal("없는 파일은 nil 이어야 함")
	}
}

// 뽑기 → 결과 저장 → 재시작(새 Hub)에서도 같은 결과가 남아야 한다.
func TestDrawPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "draws.json")
	mem := []string{"가", "나", "다", "라", "마", "바"}

	hub := NewHub(mem, loadStore(db))
	srv := httptest.NewServer(newMux(hub, "static"))
	defer srv.Close()

	c := dial(t, srv.URL)
	c.send(inMsg{Type: "config", Pool: []Prize{{"치킨", 2}}})
	c.waitState(func(s stateMsg) bool { return len(s.Pool) == 1 })
	c.send(inMsg{Type: "draw"})
	st := c.waitState(func(s stateMsg) bool { return s.Draw != nil })
	if len(st.Draw) != 6 || len(st.Members) != 6 {
		t.Fatalf("결과가 이상함: %+v", st)
	}
	win := 0
	for _, g := range st.Draw {
		if g == 0 {
			win++
		}
	}
	if win != 2 {
		t.Fatalf("당첨 %d명, 기대 2명", win)
	}

	// 저장은 디바운스(2초) → 파일이 생길 때까지 기다린 뒤 새 Hub 로 읽는다.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(db); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	hub2 := NewHub(mem, loadStore(db))
	hub2.mu.Lock()
	r2 := hub2.rooms[defaultRoom]
	hub2.mu.Unlock()
	if r2 == nil {
		t.Fatal("재시작 후 판이 복원되지 않음")
	}
	for i := range st.Draw {
		if r2.draw[i] != st.Draw[i] {
			t.Fatalf("재시작 후 결과가 다름: %v vs %v", r2.draw, st.Draw)
		}
	}
}

// ---- WS 헬퍼 ----

type conn struct {
	c *websocket.Conn
	t *testing.T
}

func dial(t *testing.T, base string) *conn {
	t.Helper()
	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(base, "http")+"/_ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return &conn{c: c, t: t}
}

func (c *conn) send(v any) {
	c.t.Helper()
	data, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.c.Write(ctx, websocket.MessageText, data); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *conn) waitState(cond func(stateMsg) bool) stateMsg {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := c.c.Read(ctx)
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		var s stateMsg
		if json.Unmarshal(data, &s) != nil || s.Type != "state" {
			continue
		}
		if cond(s) {
			return s
		}
	}
}
