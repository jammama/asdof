package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"asdof-taker/internal/jointips"
)

// 예전에는 빌딩·층·회의실 목록을 코드에 표로 박아 뒀다(codes.go). 2026-09 개편으로
// 공간이 코드 하나(spaceCd)로 바뀌고 목록도 공개 API 로 나오므로, 사이트에서 직접
// 받아 쓴다. 사이트가 회의실을 늘리거나 없애도 우리 쪽 수정이 필요 없다.
//
// 다만 화면이 /api/state 를 주기적으로 폴링하므로 그때마다 사이트를 두드릴 수는 없다.
// 별도 엔드포인트로 빼고 짧게 캐시한다.

const catalogTTL = 10 * time.Minute

// Catalog 는 지역 하나의 건물과 공간 전부다.
type Catalog struct {
	Buildings []jointips.Building `json:"buildings"`
	Spaces    []jointips.Space    `json:"spaces"`
	Purposes  []jointips.Code     `json:"purposes"`
	// BookableDays 는 {"MON":"Y",…} — 화면이 요일 안내에 쓴다.
	BookableDays map[string]string `json:"bookable_days"`
	FetchedAt    time.Time         `json:"fetched_at"`
}

type catalogCache struct {
	mu   sync.Mutex
	val  *Catalog
	when time.Time
}

// get 은 캐시가 살아 있으면 그대로, 아니면 사이트에서 새로 받는다.
//
// 조회 계열은 토큰 없이도 응답하므로 로그인 자격증명이 없어도 목록을 채울 수 있다 —
// 설정을 처음 채우는 사람이 회의실을 고르려면 이게 먼저 돼야 한다.
func (c *catalogCache) get(ctx context.Context, base, ua string, timeout time.Duration, force bool) (*Catalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.val != nil && time.Since(c.when) < catalogTTL {
		return c.val, nil
	}
	client, err := jointips.New(base, ua, timeout)
	if err != nil {
		return nil, err
	}
	buildings, err := client.Buildings(ctx, jointips.DefaultRegion)
	if err != nil {
		return nil, err
	}
	out := &Catalog{Buildings: buildings, FetchedAt: time.Now()}
	for _, b := range buildings {
		spaces, err := client.Spaces(ctx, b.BldgCd)
		if err != nil {
			return nil, err
		}
		out.Spaces = append(out.Spaces, spaces...)
	}
	// 이용목적과 운영요일은 없어도 화면이 돌아가므로 실패를 치명적으로 보지 않는다.
	if p, err := client.Purposes(ctx); err == nil {
		out.Purposes = p
	}
	if d, err := client.BookableDays(ctx); err == nil {
		out.BookableDays = d
	}
	c.val, c.when = out, time.Now()
	return out, nil
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Refresh bool `json:"refresh"`
	}
	decode(r, &in)
	cfg := s.store.Get()
	ctx, cancel := ctxWithTimeout(r, 40*time.Second)
	defer cancel()
	cat, err := s.cat.get(ctx, cfg.Site.BaseURL, cfg.Site.UserAgent, cfg.Site.Timeout(), in.Refresh)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "회의실 목록을 받지 못했습니다: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "catalog": cat})
}
