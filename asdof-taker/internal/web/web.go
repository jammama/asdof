// Package web 은 관리 UI 와 그 JSON API 를 서빙한다.
//
// 이 서비스는 공개 도메인(taker.asdof.xyz)에 노출되므로, 정적 파일 하나를 빼면
// 모든 경로가 관리자 세션을 요구한다. 비밀값(사이트 비밀번호·Notion 토큰)은
// 저장만 되고 절대 응답으로 돌아 나오지 않는다 — 설정 여부만 알려준다.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"asdof-taker/internal/config"
	"asdof-taker/internal/jointips"
	"asdof-taker/internal/notion"
	"asdof-taker/internal/runner"
)

//go:embed static/*
var assets embed.FS

const (
	cookieName    = "taker_session"
	sessionMaxAge = 12 * time.Hour
)

type Server struct {
	store  *config.Store
	runner *runner.Runner
	sched  *runner.Scheduler
	log    *slog.Logger
	seskey []byte

	// notionBase 는 비워 두면 진짜 Notion API 를 쓴다. 테스트만 바꿔 끼운다.
	notionBase string

	throttle *throttle
	cat      catalogCache
}

func New(store *config.Store, r *runner.Runner, s *runner.Scheduler, log *slog.Logger) *Server {
	return &Server{
		store: store, runner: r, sched: s, log: log,
		seskey:   store.Vault().SessionKey(),
		throttle: newThrottle(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// 인증 없이 접근 가능한 것: 로그인 화면과 로그인 API 뿐이다.
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)

	// 나머지는 전부 세션 필요
	mux.Handle("GET /api/state", s.auth(s.state))
	mux.Handle("POST /api/config", s.auth(s.saveConfig))
	mux.Handle("POST /api/secrets", s.auth(s.saveSecrets))
	mux.Handle("POST /api/admin/password", s.auth(s.changePassword))
	mux.Handle("POST /api/test/site", s.auth(s.testSite))
	mux.Handle("POST /api/test/notion", s.auth(s.testNotion))
	mux.Handle("POST /api/test/notion/write", s.auth(s.testNotionWrite))
	mux.Handle("POST /api/catalog", s.auth(s.catalog))
	mux.Handle("POST /api/query", s.auth(s.query))
	mux.Handle("POST /api/run", s.auth(s.run))
	mux.Handle("POST /api/run/cancel", s.auth(s.cancelRun))

	return securityHeaders(mux)
}

// ── 인증 ───────────────────────────────────────────────────────────────────

func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeErr(w, http.StatusUnauthorized, "로그인이 필요합니다")
			return
		}
		h(w, r)
	})
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	payload, ok := config.VerifyCookie(s.seskey, c.Value)
	if !ok {
		return false
	}
	// payload = "exp=<unix>|<발급 시점 비밀번호 해시 앞 8자>"
	parts := strings.SplitN(payload, "|", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "exp=") {
		return false
	}
	exp, err := strconv.ParseInt(parts[0][4:], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	// 비밀번호가 바뀌면 기존 세션은 모두 무효가 된다.
	return parts[1] == fingerprint(s.store.Get().Admin.PasswordHash)
}

func fingerprint(hash string) string {
	if len(hash) < 8 {
		return "none"
	}
	return hash[len(hash)-8:]
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request) {
	payload := fmt.Sprintf("exp=%d|%s",
		time.Now().Add(sessionMaxAge).Unix(), fingerprint(s.store.Get().Admin.PasswordHash))
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    config.SignCookie(s.seskey, payload),
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionMaxAge.Seconds()),
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait := s.throttle.blocked(ip); wait > 0 {
		writeErr(w, http.StatusTooManyRequests,
			fmt.Sprintf("로그인 시도가 너무 잦습니다 — %d초 후 다시 시도하세요", int(wait.Seconds())+1))
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "요청을 읽을 수 없습니다")
		return
	}
	hash := s.store.Get().Admin.PasswordHash
	if hash == "" || !config.VerifyPassword(hash, in.Password) {
		s.throttle.fail(ip)
		s.log.Warn("관리자 로그인 실패", "ip", ip)
		writeErr(w, http.StatusUnauthorized, "비밀번호가 틀렸습니다")
		return
	}
	s.throttle.ok(ip)
	s.issue(w, r)
	s.log.Info("관리자 로그인", "ip", ip)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"authed": s.authed(r)})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "요청을 읽을 수 없습니다")
		return
	}
	if !config.VerifyPassword(s.store.Get().Admin.PasswordHash, in.Current) {
		writeErr(w, http.StatusUnauthorized, "현재 비밀번호가 틀렸습니다")
		return
	}
	if len([]rune(in.Next)) < 8 {
		writeErr(w, http.StatusBadRequest, "새 비밀번호는 8자 이상이어야 합니다")
		return
	}
	hash, err := config.HashPassword(in.Next)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "비밀번호를 저장하지 못했습니다")
		return
	}
	if _, err := s.store.Update(func(c *config.Config) error {
		c.Admin.PasswordHash = hash
		return nil
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 비밀번호를 바꾸면 기존 세션이 전부 무효가 되므로 이 브라우저만 다시 발급한다.
	s.issue(w, r)
	writeJSON(w, map[string]any{"ok": true})
}

// ── 상태 / 설정 ────────────────────────────────────────────────────────────

// publicAccount 는 계정 한 줄이다. 비밀번호는 "설정됨" 플래그로만 나간다.
type publicAccount struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"password_set"`
	Enabled     bool   `json:"enabled"`
}

// publicConfig 는 화면으로 내보내는 설정이다. 비밀값은 "설정됨" 플래그로만 나간다.
type publicConfig struct {
	Site struct {
		BaseURL   string          `json:"base_url"`
		Accounts  []publicAccount `json:"accounts"`
		UserAgent string          `json:"user_agent"`
		TimeoutS  int             `json:"timeout_s"`
	} `json:"site"`
	Schedule config.Schedule `json:"schedule"`
	Booking  config.Booking  `json:"booking"`
	Notion   struct {
		Enabled       bool   `json:"enabled"`
		TokenSet      bool   `json:"token_set"`
		DatabaseID    string `json:"database_id"`
		RecordFailure bool   `json:"record_failure"`
	} `json:"notion"`
	Runtime config.Runtime `json:"runtime"`
}

func toPublic(c config.Config) publicConfig {
	var p publicConfig
	p.Site.BaseURL = c.Site.BaseURL
	p.Site.Accounts = make([]publicAccount, 0, len(c.Site.Accounts))
	for _, a := range c.Site.Accounts {
		p.Site.Accounts = append(p.Site.Accounts, publicAccount{
			ID: a.ID, Label: a.Label, Username: a.Username,
			PasswordSet: a.PasswordEnc != "", Enabled: a.Enabled,
		})
	}
	p.Site.UserAgent = c.Site.UserAgent
	p.Site.TimeoutS = c.Site.TimeoutS
	p.Schedule = c.Schedule
	p.Booking = c.Booking
	p.Notion.Enabled = c.Notion.Enabled
	p.Notion.TokenSet = c.Notion.TokenEnc != ""
	p.Notion.DatabaseID = c.Notion.DatabaseID
	p.Notion.RecordFailure = c.Notion.RecordFailure
	p.Runtime = c.Runtime
	return p
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	loc := cfg.Runtime.Location()
	next, why := s.sched.Next()

	out := map[string]any{
		"config":   toPublic(cfg),
		"now":      time.Now().In(loc).Format(time.RFC3339),
		"timezone": loc.String(),
		"next_why": why,
		"history":  s.runner.History(20),
		"ready":    errText(cfg.ReadyToRun()),
		// 설정을 손봐 준 내역(사이트 개편 마이그레이션)은 한 번 띄우고 지운다.
		"notes": s.store.Notes(),
	}
	if off, seen, at := s.sched.SiteOffset(); seen {
		out["site_offset_ms"] = off.Milliseconds()
		out["site_offset_at"] = at.In(loc).Format(time.RFC3339)
		out["site_now"] = time.Now().Add(off).In(loc).Format(time.RFC3339)
	}
	if !next.IsZero() {
		out["next_fire"] = next.In(loc).Format(time.RFC3339)
		// 대상 날짜는 설정한 표준시 기준이다 — 프로세스 로컬 시간대로 계산하면
		// 자정 근처에서 하루가 밀린다.
		out["next_target_date"] = next.In(loc).AddDate(0, 0, cfg.Booking.DateOffsetDays).Format("2006-01-02")
	}
	if cur := s.runner.Current(); cur != nil {
		out["current"] = cur.Snapshot()
	}
	writeJSON(w, out)
}

// saveConfig 는 보내온 섹션만 갱신한다.
//
// 섹션 구조체를 통째로 대입하지 않고 **현재 값 위에 언마샬**한다. 통째로 대입하면
// 클라이언트가 빠뜨린 필드가 Go 제로값으로 덮어써지기 때문이다 — 실제로 필드를 새로 추가한 뒤
// 새로고침 전 화면에서 저장하자 gap_seconds 가 0 으로 밀린 적이 있다.
// 이렇게 하면 요청에 없는 키는 기존 값을 그대로 유지한다(PATCH 의미).
func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Schedule json.RawMessage `json:"schedule"`
		Booking  json.RawMessage `json:"booking"`
		Notion   json.RawMessage `json:"notion"`
		Site     json.RawMessage `json:"site"`
		Runtime  json.RawMessage `json:"runtime"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "요청을 읽을 수 없습니다")
		return
	}

	cfg, err := s.store.Update(func(c *config.Config) error {
		for _, sec := range []struct {
			name string
			raw  json.RawMessage
			dst  any
		}{
			{"스케줄", in.Schedule, &c.Schedule},
			{"예약 설정", in.Booking, &c.Booking},
			{"실행 옵션", in.Runtime, &c.Runtime},
		} {
			if len(sec.raw) == 0 {
				continue
			}
			if err := json.Unmarshal(sec.raw, sec.dst); err != nil {
				return fmt.Errorf("%s 를 읽을 수 없습니다: %w", sec.name, err)
			}
		}
		// 캘린더에서 온 항목은 순서가 보장되지 않고, 새로 만든 건 id 가 없다.
		if len(in.Schedule) > 0 {
			for i := range c.Schedule.Entries {
				if strings.TrimSpace(c.Schedule.Entries[i].ID) == "" {
					c.Schedule.Entries[i].ID = config.NewEntryID()
				}
			}
			c.Schedule.SortEntries()
			if c.Schedule.Mode == "" {
				c.Schedule.Mode = config.ModeWeekly
			}
		}
		// 비밀값이 섞인 섹션은 허용 필드만 따로 받는다(암호문이 덮어써지지 않게).
		if len(in.Notion) > 0 {
			var n struct {
				Enabled       *bool   `json:"enabled"`
				DatabaseID    *string `json:"database_id"`
				RecordFailure *bool   `json:"record_failure"`
			}
			if err := json.Unmarshal(in.Notion, &n); err != nil {
				return fmt.Errorf("Notion 설정을 읽을 수 없습니다: %w", err)
			}
			if n.Enabled != nil {
				c.Notion.Enabled = *n.Enabled
			}
			if n.DatabaseID != nil {
				c.Notion.DatabaseID = strings.TrimSpace(*n.DatabaseID)
			}
			if n.RecordFailure != nil {
				c.Notion.RecordFailure = *n.RecordFailure
			}
		}
		if len(in.Site) > 0 {
			var st struct {
				BaseURL   *string `json:"base_url"`
				UserAgent *string `json:"user_agent"`
				TimeoutS  *int    `json:"timeout_s"`
			}
			if err := json.Unmarshal(in.Site, &st); err != nil {
				return fmt.Errorf("사이트 설정을 읽을 수 없습니다: %w", err)
			}
			if st.BaseURL != nil {
				c.Site.BaseURL = strings.TrimSpace(*st.BaseURL)
			}
			if st.UserAgent != nil {
				c.Site.UserAgent = *st.UserAgent
			}
			if st.TimeoutS != nil {
				c.Site.TimeoutS = *st.TimeoutS
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.sched.Reload()
	writeJSON(w, map[string]any{"ok": true, "config": toPublic(cfg)})
}

// saveSecrets 는 자격증명만 따로 받는다. 빈 문자열은 "바꾸지 않음"이고,
// 명시적으로 지우려면 clear 플래그를 쓴다.
func (s *Server) saveSecrets(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// AccountID 가 비어 있으면 새 계정을 만든다.
		AccountID   string `json:"account_id"`
		Label       string `json:"label"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		Enabled     *bool  `json:"enabled"`
		Delete      bool   `json:"delete_account"`
		NotionToken string `json:"notion_token"`
		ClearToken  bool   `json:"clear_notion_token"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "요청을 읽을 수 없습니다")
		return
	}
	vault := s.store.Vault()
	cfg, err := s.store.Update(func(c *config.Config) error {
		if in.Delete {
			if in.AccountID == "" {
				return fmt.Errorf("지울 계정을 지정하세요")
			}
			kept := c.Site.Accounts[:0]
			for _, a := range c.Site.Accounts {
				if a.ID != in.AccountID {
					kept = append(kept, a)
				}
			}
			c.Site.Accounts = kept
		} else if in.AccountID != "" || strings.TrimSpace(in.Username) != "" || in.Password != "" {
			idx := -1
			for i, a := range c.Site.Accounts {
				if a.ID == in.AccountID && in.AccountID != "" {
					idx = i
					break
				}
			}
			if idx < 0 {
				// 같은 아이디를 새로 추가하려 하면 기존 계정을 고치는 것으로 본다 —
				// 실수로 중복 계정을 만들면 잡을 수 있는 건수가 부풀려진다.
				if u := strings.ToLower(strings.TrimSpace(in.Username)); u != "" {
					for i, a := range c.Site.Accounts {
						if strings.ToLower(strings.TrimSpace(a.Username)) == u {
							idx = i
							break
						}
					}
				}
			}
			if idx < 0 {
				if len(c.Site.Accounts) >= config.MaxAccounts {
					return fmt.Errorf("계정은 최대 %d개까지입니다", config.MaxAccounts)
				}
				c.Site.Accounts = append(c.Site.Accounts, config.Account{ID: config.NewAccountID(), Enabled: true})
				idx = len(c.Site.Accounts) - 1
			}
			a := &c.Site.Accounts[idx]
			if u := strings.TrimSpace(in.Username); u != "" {
				a.Username = u
			}
			a.Label = strings.TrimSpace(in.Label)
			if in.Password != "" {
				enc, err := vault.Encrypt(in.Password)
				if err != nil {
					return err
				}
				a.PasswordEnc = enc
			}
			if in.Enabled != nil {
				a.Enabled = *in.Enabled
			}
		}
		switch {
		case in.ClearToken:
			c.Notion.TokenEnc = ""
		case in.NotionToken != "":
			enc, err := vault.Encrypt(strings.TrimSpace(in.NotionToken))
			if err != nil {
				return err
			}
			c.Notion.TokenEnc = enc
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.sched.Reload()
	s.log.Info("자격증명 갱신", "accounts", len(cfg.Site.Accounts),
		"notion_token", config.Mask(cfg.Notion.TokenEnc))
	writeJSON(w, map[string]any{"ok": true, "config": toPublic(cfg)})
}

// ── 연결 테스트 ────────────────────────────────────────────────────────────

// testSite 는 계정 하나로 로그인해 본다. account_id 를 안 주면 첫 계정을 쓴다.
func (s *Server) testSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID string `json:"account_id"`
	}
	decode(r, &in)
	cfg := s.store.Get()
	acc, ok := cfg.Site.Account(in.AccountID)
	if !ok {
		ready := cfg.Site.ReadyAccounts()
		if len(ready) == 0 {
			writeErr(w, http.StatusBadRequest, "아이디/비밀번호를 먼저 저장하세요")
			return
		}
		acc = ready[0]
	}
	pw, err := s.store.AccountPassword(acc.ID)
	if err != nil || pw == "" || strings.TrimSpace(acc.Username) == "" {
		writeErr(w, http.StatusBadRequest, "아이디/비밀번호를 먼저 저장하세요")
		return
	}
	client, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, cfg.Site.Timeout())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := ctxWithTimeout(r, 30*time.Second)
	defer cancel()
	if err := client.Login(ctx, acc.Username, pw); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	msg := fmt.Sprintf("로그인 성공 — %s (%s)", acc.Name(), client.Member().UserNm)
	if st, err := client.ServerTime(ctx); err == nil {
		if d := time.Since(st); d > 2*time.Second || d < -2*time.Second {
			msg += fmt.Sprintf(" · ⚠ 서버 시계와 %.0f초 차이", d.Seconds())
		} else {
			msg += " · 서버 시계 정상"
		}
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func (s *Server) testNotion(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Get()
	token, err := s.store.NotionToken()
	if err != nil || token == "" {
		writeErr(w, http.StatusBadRequest, "Notion 토큰을 먼저 저장하세요")
		return
	}
	dbID := config.NormalizeDatabaseID(cfg.Notion.DatabaseID)
	if dbID == "" {
		writeErr(w, http.StatusBadRequest, "데이터베이스 ID 가 올바르지 않습니다 (32자리 hex 또는 DB 링크)")
		return
	}
	ctx, cancel := ctxWithTimeout(r, 20*time.Second)
	defer cancel()
	schema, err := notion.NewAt(token, s.notionBase).FetchSchema(ctx, dbID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": fmt.Sprintf("연결됨 — %q (속성 %d개)", schema.DBTitle, len(schema.All)),
		"schema":  schema,
	})
}

// testNotionWrite 는 실제로 행 하나를 만들어 보고, 만들어진 내용을 되읽어 돌려준다.
// 연결 테스트(스키마 조회)만으로는 통합에 쓰기 권한이 있는지, 값이 어느 칸에
// 들어가는지 알 수 없어서 이 경로가 따로 필요하다.
//
// 기본값은 만든 뒤 휴지통으로 되돌리는 것이다. keep=true 면 남긴다.
func (s *Server) testNotionWrite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Keep bool `json:"keep"`
	}
	decode(r, &in)

	cfg := s.store.Get()
	token, err := s.store.NotionToken()
	if err != nil || token == "" {
		writeErr(w, http.StatusBadRequest, "Notion 토큰을 먼저 저장하세요")
		return
	}
	dbID := config.NormalizeDatabaseID(cfg.Notion.DatabaseID)
	if dbID == "" {
		writeErr(w, http.StatusBadRequest, "데이터베이스 ID 가 올바르지 않습니다 (32자리 hex 또는 DB 링크)")
		return
	}
	ctx, cancel := ctxWithTimeout(r, 30*time.Second)
	defer cancel()

	c := notion.NewAt(token, s.notionBase)
	schema, err := c.FetchSchema(ctx, dbID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	// 실제 예약과 같은 경로를 타되, 사람이 보고 바로 테스트임을 알 수 있게 표시한다.
	subject := "[등록 테스트] " + cfg.Booking.Subject
	start, slots := "13:00", 8
	if len(cfg.Booking.Targets) > 0 {
		start, slots = cfg.Booking.Targets[0].Start, cfg.Booking.Targets[0].DurationSlots
	}
	room := "테스트 회의실"
	if len(cfg.Booking.Targets) > 0 && cfg.Booking.Targets[0].Label != "" {
		// Label 은 "현승빌딩(S3) 5층 회의실A" — Notion 표기("현승 5A")로 줄여 둔다.
		room = jointips.RoomLabel(cfg.Booking.Targets[0].Label, "", cfg.Booking.Targets[0].Label)
	}
	loc := cfg.Runtime.Location()
	rec := notion.Record{
		Subject: subject,
		Usage:   jointips.UsageLine(subject, start, slots),
		Room:    room,
		Period:  jointips.TimeRange(start, slots),
		Booker:  cfg.Booking.Manager,
		Date:    time.Now().In(loc).AddDate(0, 0, cfg.Booking.DateOffsetDays).Format("2006-01-02"),
	}

	page, err := c.CreatePage(ctx, dbID, schema, rec)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "등록 실패 — "+err.Error())
		return
	}
	s.log.Info("Notion 등록 테스트", "page", page.URL, "keep", in.Keep)

	// 되읽어서 '보낸 값'과 '남은 값'을 나란히 보여준다.
	stored, readErr := c.ReadRow(ctx, page.ID)
	if readErr != nil {
		stored = nil
	}

	msg := "등록 성공 — 만든 행을 휴지통으로 되돌렸습니다"
	if in.Keep {
		msg = "등록 성공 — 만든 행을 그대로 두었습니다 (직접 지워 주세요)"
	} else if err := c.Archive(ctx, page.ID); err != nil {
		msg = "등록은 됐지만 되돌리지 못했습니다 (" + err.Error() + ") — 직접 지워 주세요"
	}

	writeJSON(w, map[string]any{
		"ok":      true,
		"message": msg,
		"url":     page.URL,
		"kept":    in.Keep,
		"sent":    sentValues(schema, rec),
		"stored":  stored,
	})
}

// sentValues 는 "이 속성에 이 값을 보냈다" 를 속성 이름 기준으로 정리한다.
// 되읽은 값(stored)과 나란히 놓으면 어느 칸이 비었는지 바로 보인다.
func sentValues(s *notion.Schema, r notion.Record) map[string]string {
	out := map[string]string{}
	put := func(name, value string) {
		if name != "" && value != "" {
			out[name] = value
		}
	}
	if s.RoomIsTitle() {
		put(s.Title, r.Room)
	} else {
		put(s.Title, r.Subject)
		put(s.Room, r.Room)
	}
	put(s.Usage, r.Usage)
	put(s.Period, r.Period)
	put(s.Booker, r.Booker)
	put(s.Date, r.Date)
	return out
}

// ── 현황 조회 / 실행 ───────────────────────────────────────────────────────

func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Date   string `json:"date"`    // "2026-09-18"
		BldgCd string `json:"bldg_cd"` // "" = 전체
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "요청을 읽을 수 없습니다")
		return
	}
	cfg := s.store.Get()
	d, err := time.ParseInLocation("2006-01-02", in.Date, cfg.Runtime.Location())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "날짜 형식이 잘못됐습니다 (YYYY-MM-DD)")
		return
	}
	client, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, cfg.Site.Timeout())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := ctxWithTimeout(r, 60*time.Second)
	defer cancel()
	// 슬롯 조회 자체는 비로그인으로도 되지만, 로그인하면 내 예약(MY_CONFLICT)까지 표시된다.
	// 자격증명이 없거나 틀려도 조회는 계속한다 — 화면을 못 쓰게 만들 이유가 없다.
	// 첫 번째 쓸 수 있는 계정으로 로그인해 두면 내 예약(MY_CONFLICT)까지 표시된다.
	if ready := cfg.Site.ReadyAccounts(); len(ready) > 0 {
		if pw, perr := s.store.AccountPassword(ready[0].ID); perr == nil && pw != "" {
			if err := client.Login(ctx, ready[0].Username, pw); err != nil {
				s.log.Warn("현황 조회용 로그인 실패 — 비로그인으로 조회합니다", "err", err)
			}
		}
	}
	rows, err := client.Timetable(ctx, jointips.DefaultRegion, in.BldgCd, d.Format("2006-01-02"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "date": in.Date, "rows": rows})
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DryRun  bool   `json:"dry_run"`
		EntryID string `json:"entry_id"` // 지정 스케줄 항목 하나만 돌려 볼 때
		Date    string `json:"date"`     // 그 실행일의 건 전부를 돌려 볼 때
	}
	decode(r, &in)
	// 수동 실행도 지정 스케줄 항목을 집을 수 있게 한다 — 드라이런으로 그 항목의 설정을
	// 그대로 확인할 수 있어야 쓸모가 있다. date 를 주면 그날의 건 전부를 돌린다.
	cfg := s.store.Get()
	var entries []config.ScheduleEntry
	switch {
	case in.EntryID != "":
		for _, e := range cfg.Schedule.Entries {
			if e.ID == in.EntryID {
				entries = append(entries, e)
				break
			}
		}
		if len(entries) == 0 {
			writeErr(w, http.StatusBadRequest, "지정 예약을 찾을 수 없습니다")
			return
		}
	case in.Date != "":
		if entries = cfg.Schedule.EntriesOn(in.Date); len(entries) == 0 {
			writeErr(w, http.StatusBadRequest, "그 날짜에 켜져 있는 지정 예약이 없습니다")
			return
		}
	}
	run, err := s.runner.Start(context.Background(), runner.Options{
		Mode: "manual", DryRun: in.DryRun, Entries: entries})
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "run": run.Snapshot()})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if !s.runner.Cancel() {
		writeErr(w, http.StatusConflict, "실행 중인 작업이 없습니다")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ── 정적 ───────────────────────────────────────────────────────────────────

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := assets.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "화면 파일이 없습니다", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src data:")
		h.ServeHTTP(w, r)
	})
}

// ── 로그인 시도 제한 ───────────────────────────────────────────────────────

// throttle 은 IP 별로 연속 실패를 세어 지수적으로 지연시킨다.
// 공개 도메인이라 비밀번호 대입 시도를 늦추는 최소한의 방어가 필요하다.
type throttle struct {
	mu    sync.Mutex
	fails map[string]*failState
}

type failState struct {
	count int
	until time.Time
}

func newThrottle() *throttle { return &throttle{fails: map[string]*failState{}} }

func (t *throttle) blocked(ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.fails[ip]
	if f == nil {
		return 0
	}
	if d := time.Until(f.until); d > 0 {
		return d
	}
	return 0
}

func (t *throttle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.fails[ip]
	if f == nil {
		f = &failState{}
		t.fails[ip] = f
	}
	f.count++
	if f.count >= 3 {
		delay := time.Duration(1<<min(f.count-3, 8)) * time.Second // 1s → 최대 256s
		f.until = time.Now().Add(delay)
	}
	if len(t.fails) > 1000 { // 메모리 상한
		t.fails = map[string]*failState{ip: f}
	}
}

func (t *throttle) ok(ip string) {
	t.mu.Lock()
	delete(t.fails, ip)
	t.mu.Unlock()
}

// ── 도우미 ─────────────────────────────────────────────────────────────────

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func clientIP(r *http.Request) string {
	// nginx 가 앞단이므로 X-Real-IP 를 신뢰한다(로컬에서만 listen 한다).
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ctxWithTimeout 은 요청 컨텍스트에 상한을 건다. 사이트가 느려도 핸들러가 매달리지 않는다.
func ctxWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
