// Package runner 는 예약 1회의 전체 흐름을 담당한다:
// 워밍업 → 정밀 대기 → 발사 → 재시도 → 재조회 검증 → Notion 기록 (§2, §6).
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"asdof-taker/internal/config"
	"asdof-taker/internal/jointips"
	"asdof-taker/internal/notion"
)

// ── 실행 기록 ──────────────────────────────────────────────────────────────

type LogLine struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"` // info | warn | error | success
	Msg   string    `json:"msg"`
}

type Status string

const (
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
	StatusPartial Status = "partial" // 원하는 건수 중 일부만 확보
	StatusSkipped Status = "skipped" // 멱등성 판정으로 시도하지 않음
)

// Run 은 실행 한 번의 기록이다. 순수 데이터라 자유롭게 복사·직렬화할 수 있다.
type Run struct {
	ID         string    `json:"id"`
	Mode       string    `json:"mode"` // scheduled | manual
	DryRun     bool      `json:"dry_run"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	FireAt     time.Time `json:"fire_at,omitempty"`
	TargetDate string    `json:"target_date"`
	Status     Status    `json:"status"`
	Message    string    `json:"message"`
	Bookings   []Booked  `json:"bookings"` // 이번 실행에서 확보한 예약들
	Want       int       `json:"want"`     // 확보하려던 건수
	Attempts   int       `json:"attempts"`
	OffsetMS   int64     `json:"offset_ms"` // 목표 시각 대비 실제 발사 오차
	Logs       []LogLine `json:"logs"`
}

// Booked 는 실제로 잡힌 예약 한 건이다.
type Booked struct {
	Room      string `json:"room"`       // "현승 5A"
	TimeRange string `json:"time_range"` // "14:00~18:00"
}

func (b Booked) String() string { return b.Room + " " + b.TimeRange }

// Live 는 진행 중인 실행이다. 실행 고루틴이 쓰고 웹 핸들러가 읽으므로
// 모든 접근을 뮤텍스로 감싼다. 밖으로 나갈 때는 언제나 Snapshot 복사본이다.
type Live struct {
	mu  sync.Mutex
	run Run
	log *slog.Logger
}

// Snapshot 은 동시 읽기용 복사본이다.
func (l *Live) Snapshot() Run {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.run
	out.Logs = append([]LogLine(nil), l.run.Logs...)
	out.Bookings = append([]Booked(nil), l.run.Bookings...)
	return out
}

func (l *Live) edit(fn func(*Run)) {
	l.mu.Lock()
	fn(&l.run)
	l.mu.Unlock()
}

func (l *Live) addf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	var id string
	l.mu.Lock()
	id = l.run.ID
	l.run.Logs = append(l.run.Logs, LogLine{At: time.Now(), Level: level, Msg: msg})
	if len(l.run.Logs) > 400 {
		l.run.Logs = l.run.Logs[len(l.run.Logs)-400:]
	}
	l.mu.Unlock()
	if l.log == nil {
		return
	}
	switch level {
	case "error":
		l.log.Error(msg, "run", id)
	case "warn":
		l.log.Warn(msg, "run", id)
	default:
		l.log.Info(msg, "run", id)
	}
}

func (l *Live) info(f string, a ...any) { l.addf("info", f, a...) }
func (l *Live) warn(f string, a ...any) { l.addf("warn", f, a...) }
func (l *Live) fail(f string, a ...any) { l.addf("error", f, a...) }
func (l *Live) good(f string, a ...any) { l.addf("success", f, a...) }

// finish 는 최종 판정을 적는다.
func (l *Live) finish(st Status, msg string) {
	l.edit(func(r *Run) { r.Status, r.Message = st, msg })
}

// nextAttempt 는 시도 횟수를 하나 올리고 그 값을 돌려준다.
func (l *Live) nextAttempt() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.run.Attempts++
	return l.run.Attempts
}

func (l *Live) attempts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.run.Attempts
}

// ── Runner ─────────────────────────────────────────────────────────────────

// Runner 는 동시에 한 번만 실행되도록 보장한다. 여러 번 눌러도 겹치지 않는다.
type Runner struct {
	store *config.Store
	log   *slog.Logger

	mu         sync.Mutex
	siteOffset time.Duration // 대상 사이트 시계 - 우리 시계 (최근 실측)
	offsetSeen bool
	current    *Live
	cancel     context.CancelFunc
	history    []Run
	seq        int
	onFinish   func()
}

func New(store *config.Store, log *slog.Logger) *Runner {
	return &Runner{store: store, log: log}
}

// Current 는 진행 중인 실행(없으면 nil)이다.
func (rn *Runner) Current() *Live {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.current
}

// History 는 최근 실행 기록을 최신순으로 돌려준다.
func (rn *Runner) History(limit int) []Run {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	out := make([]Run, 0, limit)
	for i := len(rn.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, rn.history[i])
	}
	return out
}

// SetHistory 는 디스크에서 복원한 기록을 넣는다(오래된 것부터).
func (rn *Runner) SetHistory(runs []Run) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.history = append([]Run(nil), runs...)
}

var ErrBusy = errors.New("이미 실행 중입니다")

// Options 는 한 번의 실행 조건이다.
type Options struct {
	Mode   string     // "scheduled" | "manual"
	DryRun bool       // true 면 최종 POST 만 생략한다
	FireAt *time.Time // 우리 시계 기준 목표. nil 이면 워밍업 직후 곧바로 발사
}

// Start 는 실행을 비동기로 시작하고 기록 핸들을 돌려준다.
func (rn *Runner) Start(ctx context.Context, opt Options) (*Live, error) {
	rn.mu.Lock()
	if rn.current != nil {
		rn.mu.Unlock()
		return nil, ErrBusy
	}
	rn.seq++
	run := &Live{log: rn.log, run: Run{
		ID:        fmt.Sprintf("%s-%d", time.Now().Format("20060102-150405"), rn.seq),
		Mode:      opt.Mode,
		DryRun:    opt.DryRun,
		StartedAt: time.Now(),
		Status:    StatusRunning,
	}}
	if opt.FireAt != nil {
		run.run.FireAt = *opt.FireAt
	}
	ctx, cancel := context.WithCancel(ctx)
	rn.current, rn.cancel = run, cancel
	rn.mu.Unlock()

	go func() {
		defer cancel()
		defer func() {
			if p := recover(); p != nil {
				run.fail("내부 오류: %v", p)
				run.finish(StatusFailed, "내부 오류")
			}
			run.edit(func(r *Run) { r.FinishedAt = time.Now() })
			rn.mu.Lock()
			rn.history = append(rn.history, run.Snapshot())
			if len(rn.history) > 50 {
				rn.history = rn.history[len(rn.history)-50:]
			}
			rn.current, rn.cancel = nil, nil
			rn.mu.Unlock()
			if rn.onFinish != nil {
				rn.onFinish()
			}
		}()
		rn.execute(ctx, run, opt)
	}()
	return run, nil
}

// SiteOffset 은 마지막으로 실측한 대상 사이트의 시계 오차다.
func (rn *Runner) SiteOffset() (time.Duration, bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.siteOffset, rn.offsetSeen
}

func (rn *Runner) setSiteOffset(d time.Duration) {
	rn.mu.Lock()
	rn.siteOffset, rn.offsetSeen = d, true
	rn.mu.Unlock()
}

// Cancel 은 진행 중인 실행을 중단시킨다.
func (rn *Runner) Cancel() bool {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if rn.cancel == nil {
		return false
	}
	rn.cancel()
	return true
}

// OnFinish 는 실행이 끝날 때마다 불린다(기록 영속화용).
func (rn *Runner) OnFinish(fn func()) { rn.onFinish = fn }

// ── 실제 흐름 ──────────────────────────────────────────────────────────────

func (rn *Runner) execute(ctx context.Context, run *Live, opt Options) {
	cfg := rn.store.Get()
	loc := cfg.Runtime.Location()
	dryRun := opt.DryRun || cfg.Runtime.DryRun
	run.edit(func(r *Run) { r.DryRun = dryRun })

	if err := cfg.Validate(); err != nil {
		run.fail("설정 오류: %v", err)
		run.finish(StatusFailed, "설정 오류")
		return
	}
	if err := cfg.ReadyToRun(); err != nil {
		run.fail("%v", err)
		run.finish(StatusFailed, "설정 미완성")
		return
	}

	// 대상 날짜 — 발사 시각 기준(자정을 넘겨 실행되는 경우를 위해)
	base := time.Now().In(loc)
	if opt.FireAt != nil {
		base = opt.FireAt.In(loc)
	}
	target := base.AddDate(0, 0, cfg.Booking.DateOffsetDays)
	dateDot := target.Format("2006.01.02")  // 사이트 형식
	dateDash := target.Format("2006-01-02") // Notion 형식
	run.edit(func(r *Run) { r.TargetDate = dateDash })

	if dryRun {
		run.warn("드라이런 — 예약 신청 POST 는 보내지 않습니다")
	}
	run.info("대상 날짜 %s (오늘 +%d일)", dateDash, cfg.Booking.DateOffsetDays)

	// ── 워밍업 ──
	password, err := rn.store.Password()
	if err != nil {
		run.fail("저장된 비밀번호를 읽을 수 없습니다: %v", err)
		run.finish(StatusFailed, "비밀번호 복호화 실패")
		return
	}
	client, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, cfg.Site.Timeout())
	if err != nil {
		run.fail("클라이언트 생성 실패: %v", err)
		run.finish(StatusFailed, "내부 오류")
		return
	}
	run.info("로그인 중… (%s)", cfg.Site.Username)
	if err := client.Login(ctx, cfg.Site.Username, password); err != nil {
		run.fail("%v", err)
		run.finish(StatusFailed, "로그인 실패")
		return
	}
	run.good("로그인 성공")

	// 대상 사이트의 시계를 잰다.
	// 7일 예약 창은 사이트 서버가 자기 시계로 계산하므로(§3.9), 새 날짜가 열리는 순간은
	// 우리 자정이 아니라 '그쪽 자정'이다. 발사 시각(fire_at)은 이 실측값을 보고 사람이 정한다.
	siteOffset, offErr := client.ClockOffset(ctx)
	if offErr != nil {
		run.warn("사이트 시계를 재지 못했습니다: %v — 우리 시계 기준으로 발사합니다", offErr)
	} else {
		rn.setSiteOffset(siteOffset)
		switch {
		case siteOffset > 30*time.Second || siteOffset < -30*time.Second:
			run.warn("사이트 시계가 우리보다 %s — 예약 창도 그만큼 늦게/일찍 열립니다 (발사 시각 확인 필요)",
				humanOffset(siteOffset))
		default:
			run.info("사이트 시계 오차 %+.1f초 — 무시할 수준", siteOffset.Seconds())
		}
	}

	// uid 풀 확보(§3.2)
	n, err := client.FillUIDPool(ctx, cfg.Schedule.UIDPoolSize)
	if err != nil {
		run.warn("uid 풀을 다 못 채웠습니다 (%d개): %v", n, err)
	}
	if n == 0 {
		run.fail("uid 를 하나도 얻지 못했습니다 — 사이트 구조 변경 의심")
		run.finish(StatusFailed, "uid 획득 실패")
		return
	}
	run.info("uid %d개 확보", n)

	// 대상 후보 만들기 — 지정 대상(§5.1) 우선, 없으면 폴백 탐색(§5.2)
	targets := buildTargets(cfg, dateDot)
	// 빌딩 필터 없이 전체를 조회한다. 폴백의 빌딩/층 선호는 클라이언트에서 거르므로(§5.2)
	// 전체를 받아 두는 편이 지정 대상 검증(§5.0 4번)까지 함께 할 수 있어 낫다.
	rows, queryErr := client.Query(ctx, "", dateDot)
	if queryErr != nil {
		run.warn("현황 조회 실패: %v (지정 대상으로만 진행)", queryErr)
	} else if len(rows) == 0 {
		// 7일 창 밖의 날짜를 조회하면 빈 응답이 온다(§3.9). 오늘로 다시 조회해 구분한다.
		if today, err := client.Query(ctx, "", base.Format("2006.01.02")); err == nil && len(today) > 0 {
			run.warn("%s 는 아직 예약을 받지 않습니다 — 사이트는 오늘부터 7일 창만 엽니다. "+
				"「대상 날짜」를 줄여 보세요 (현재 +%d일)", dateDash, cfg.Booking.DateOffsetDays)
		} else {
			run.warn("현황 조회 결과가 비었습니다 — 사이트 구조 변경 의심")
		}
	} else {
		run.info("현황 조회 완료 — 회의실 %d곳", len(rows))
		verifyTargets(run, targets, rows)
		if cfg.Booking.Fallback.Enabled {
			if c := findFallback(cfg, rows, nil); c != nil {
				targets = append(targets, targetFromCandidate(cfg, c, dateDot))
				run.info("폴백 후보: %s %s (%d슬롯)", c.Row.Label(), c.Start(), c.Slots)
			} else {
				run.warn("폴백 조건에 맞는 빈 자리를 찾지 못했습니다")
			}
		}
	}
	if len(targets) == 0 {
		run.fail("시도할 대상이 없습니다")
		run.finish(StatusFailed, "대상 없음")
		return
	}

	// 멱등성 점검(§7.6) — Notion 에 이미 기록이 있으면 쿼터를 낭비하지 않는다
	var (
		notionClient *notion.Client
		notionSchema *notion.Schema
		dbID         = config.NormalizeDatabaseID(cfg.Notion.DatabaseID)
	)
	if cfg.Notion.Enabled && dbID != "" {
		if token, err := rn.store.NotionToken(); err != nil || token == "" {
			run.warn("Notion 토큰을 읽을 수 없어 기록을 건너뜁니다")
		} else {
			notionClient = notion.New(token)
			if notionSchema, err = notionClient.FetchSchema(ctx, dbID); err != nil {
				run.warn("Notion 스키마 조회 실패: %v", err)
				notionClient = nil
			} else if n, err := notionClient.CountRowsOn(ctx, dbID, notionSchema, dateDash); err == nil && n >= cfg.Booking.Count {
				run.good("%s 기록이 Notion 에 이미 %d건 있습니다 (목표 %d건) — 예약을 시도하지 않습니다",
					dateDash, n, cfg.Booking.Count)
				run.finish(StatusSkipped, "이미 처리된 날짜")
				return
			} else if err == nil && n > 0 {
				run.info("%s 기록이 Notion 에 %d건 있습니다 — 나머지 %d건을 시도합니다",
					dateDash, n, cfg.Booking.Count-n)
			}
		}
	}

	// 드라이런은 여기서 페이로드만 보여주고 끝낸다(§11 1단계).
	if dryRun {
		for i, t := range targets {
			form, err := t.payload.Form("<uid>")
			if err != nil {
				run.fail("대상 #%d 페이로드 오류: %v", i+1, err)
				continue
			}
			run.info("대상 #%d %s — wr_4=%s wr_5=%s wr_8=%s wr_3=%s",
				i+1, t.label, form.Get("wr_4"), form.Get("wr_5"), form.Get("wr_8"), form.Get("wr_3"))
		}
		if notionSchema != nil {
			run.info("Notion 속성 매칭 — 제목:%s 현황:%s 회의실:%s 시간:%s 예약자:%s 날짜:%s",
				dash(notionSchema.Title), dash(notionSchema.Usage), dash(notionSchema.Room),
				dash(notionSchema.Period), dash(notionSchema.Booker), dash(notionSchema.Date))
		}
		run.info("목표 %d건 · 후보 %d개", cfg.Booking.Count, len(targets))
		run.good("드라이런 완료 — 쓰기 요청을 보내지 않았습니다")
		run.finish(StatusSkipped, "드라이런")
		return
	}

	// ── 정밀 대기 후 발사 ──
	if opt.FireAt != nil {
		run.info("발사 대기 → %s", opt.FireAt.In(loc).Format("15:04:05.000"))
		if err := waitUntil(ctx, *opt.FireAt); err != nil {
			run.fail("대기 중단: %v", err)
			run.finish(StatusFailed, "중단됨")
			return
		}
		offset := time.Since(*opt.FireAt).Milliseconds()
		run.edit(func(r *Run) { r.OffsetMS = offset })
		run.info("발사 (목표 대비 %+dms)", offset)
	}

	retry := cfg.Schedule.Retry
	want := cfg.Booking.Count
	if want < 1 {
		want = 1
	}
	gap := time.Duration(cfg.Booking.GapSeconds) * time.Second
	run.edit(func(r *Run) { r.Want = want })

	// secured 는 이미 확보한 대상의 인덱스. 성공한 대상은 후보에서 빠진다.
	secured := map[int]bool{}
	var lastPost time.Time // 사이트에 글이 실제로 등록된 마지막 시각 (도배 방지 기준점)
	var lastAlert string
	stop := false

	// 예약 '한 건'마다 재시도 예산을 새로 준다. 건 사이에는 도배 방지 간격을 지켜야 하는데,
	// 그 대기가 앞 건의 예산을 잡아먹으면 두 번째를 아예 못 쏘기 때문이다.
	for len(secured) < want && !stop {
		if !lastPost.IsZero() && gap > 0 {
			if w := gap - time.Since(lastPost); w > 0 {
				run.info("도배 방지 대기 %.0f초 — 사이트가 같은 계정의 연속 등록을 막습니다", w.Seconds())
				if !sleepCtx(ctx, w) {
					break
				}
			}
		}

		phaseStart := time.Now()
		phaseAttempts, floodWaits := 0, 0
		got := false

		// Go 에서 switch 안의 break 는 switch 만 빠져나간다. 루프를 끊으려면 라벨이 필요하다.
	phase:
		for phaseAttempts < retry.MaxAttempts && time.Since(phaseStart) < retry.MaxDuration() {
			if ctx.Err() != nil {
				run.fail("중단됨")
				run.finish(StatusFailed, "중단됨")
				return
			}
			pending := pendingIdx(targets, secured)
			if len(pending) == 0 {
				run.warn("남은 대상이 없습니다 (%d/%d건 확보)", len(secured), want)
				stop = true
				break phase
			}
			ti := pending[phaseAttempts%len(pending)]
			t := targets[ti]

			uid, err := client.NextUID(ctx)
			if err != nil {
				run.warn("uid 확보 실패: %v", err)
				phaseAttempts++
				run.nextAttempt()
				sleepCtx(ctx, retry.Interval())
				continue
			}
			phaseAttempts++
			attempt := run.nextAttempt()
			res, err := client.Submit(ctx, uid, t.payload)
			if err != nil {
				run.warn("시도 %d — 전송 오류: %v", attempt, err)
				sleepCtx(ctx, retry.Interval())
				continue
			}

			switch {
			case isFloodAlert(res.Alert):
				// gnuboard 의 도배 방지(cf_delay_sec). 두들겨봐야 소용없고 서버에 부담만 준다.
				lastAlert = res.Alert
				floodWaits++
				if floodWaits > 3 {
					run.warn("도배 방지가 계속 걸립니다 — 이 건은 포기합니다 (gap_seconds 를 늘려 보세요)")
					stop = true
					break phase
				}
				w := gap
				if !lastPost.IsZero() {
					if r := gap - time.Since(lastPost); r > 0 {
						w = r
					} else {
						// 설정한 간격보다 사이트의 실제 제한이 길다 — 조금씩 늘려가며 다시 본다.
						w = gap * time.Duration(floodWaits)
						if w > 30*time.Second {
							w = 30 * time.Second
						}
					}
				}
				if w <= 0 {
					// 간격을 0(기다리지 않음)으로 뒀더라도 도배 방지에는 기다리는 수밖에 없다.
					w = 30 * time.Second
				}
				run.warn("시도 %d — 도배 방지에 걸렸습니다. %.0f초 기다립니다", attempt, w.Seconds())
				if !sleepCtx(ctx, w) {
					stop = true
					break phase
				}
				phaseStart = time.Now() // 대기는 이 건의 재시도 예산에서 빼지 않는다

			case res.Alert != "":
				lastAlert = res.Alert
				run.warn("시도 %d — %s (%s, %dms)", attempt, res.Alert, t.label, res.Elapsed.Milliseconds())
				if isQuotaAlert(res.Alert) {
					run.warn("팀 일일 예약 한도에 걸렸습니다 — 더 시도하지 않습니다")
					stop = true
					break phase
				}
				sleepCtx(ctx, retry.Interval())

			default:
				run.info("시도 %d — 응답 %d (%s, %dms) — 검증 중",
					attempt, res.Status, t.label, res.Elapsed.Milliseconds())
				// 최종 근거는 재조회다(§3.6 2차 판정)
				if ok, label := verifyBooked(ctx, client, cfg, t, dateDot); ok {
					secured[ti] = true
					lastPost = time.Now()
					b := Booked{Room: label, TimeRange: jointips.TimeRange(t.payload.Start, t.payload.Slots)}
					run.edit(func(r *Run) { r.Bookings = append(r.Bookings, b) })
					run.good("예약 성공 (%d/%d) — %s %s", len(secured), want, dateDash, b)
					rn.record(ctx, run, cfg, notionClient, notionSchema, dbID, t, dateDash, label, true, "")
					got = true
					break phase
				}
				run.warn("시도 %d — 재조회에서 확인되지 않음", attempt)
				sleepCtx(ctx, retry.Interval())
			}

			// 3회 연속 실패면 상황이 바뀌었을 수 있으니 조회해서 대상을 보충한다(§6.3)
			if phaseAttempts%3 == 0 && cfg.Booking.Fallback.Enabled {
				if rows, err := client.Query(ctx, "", dateDot); err == nil {
					if c := findFallback(cfg, rows, run.Snapshot().Bookings); c != nil {
						targets = append(targets, targetFromCandidate(cfg, c, dateDot))
						run.info("대상 보충: %s %s (%d슬롯)", c.Row.Label(), c.Start(), c.Slots)
					}
				}
			}
		}
		if !got {
			break
		}
	}

	got := run.Snapshot().Bookings
	if len(got) > 0 {
		parts := make([]string, 0, len(got))
		for _, b := range got {
			parts = append(parts, b.String())
		}
		summary := dateDash + " " + strings.Join(parts, " + ")
		if len(got) >= want {
			run.good("완료 — %d건 확보: %s", len(got), summary)
			run.finish(StatusSuccess, summary)
		} else {
			run.warn("%d/%d건만 확보했습니다: %s", len(got), want, summary)
			run.finish(StatusPartial, fmt.Sprintf("%d/%d건 · %s", len(got), want, summary))
		}
		return
	}

	msg := "재시도를 모두 소진했습니다"
	if lastAlert != "" {
		msg = lastAlert
	}
	run.fail("예약 실패 — %s (시도 %d회, 0/%d건)", msg, run.attempts(), want)
	if len(targets) > 0 {
		rn.record(ctx, run, cfg, notionClient, notionSchema, dbID, targets[0], dateDash, "", false, msg)
	}
	run.finish(StatusFailed, msg)
}

// record 는 Notion 기록을 남긴다. 실패해도 예약 성공 판정을 뒤집지 않는다(§7.7).
func (rn *Runner) record(ctx context.Context, run *Live, cfg config.Config,
	c *notion.Client, s *notion.Schema, dbID string, t target, date, roomLabel string, ok bool, reason string) {
	if c == nil || s == nil {
		return
	}
	if !ok && !cfg.Notion.RecordFailure {
		return
	}
	subject := cfg.Booking.Subject
	usage := jointips.UsageLine(subject, t.payload.Start, t.payload.Slots)
	if !ok {
		subject = "[실패] " + subject
		usage = usage + " — " + reason
	}
	if roomLabel == "" {
		roomLabel = t.roomLabel
	}
	rec := notion.Record{
		Subject: subject,
		Usage:   usage,
		Room:    roomLabel,
		Period:  jointips.TimeRange(t.payload.Start, t.payload.Slots),
		Booker:  cfg.Booking.Manager,
		Date:    date,
	}
	page, err := c.CreatePage(ctx, dbID, s, rec)
	if err != nil {
		run.warn("Notion 기록 실패: %v (예약 결과에는 영향 없음)", err)
		return
	}
	run.info("Notion 에 기록했습니다 — %s", page.URL)
}

// verifyBooked 는 재조회로 예약이 실제로 잡혔는지 확인한다(§3.6 2차 판정 = ground truth).
func verifyBooked(ctx context.Context, client *jointips.Client, cfg config.Config, t target, dateDot string) (bool, string) {
	rows, err := client.Query(ctx, t.payload.Building, dateDot)
	if err != nil {
		return false, ""
	}
	for _, row := range rows {
		if row.RoomID != t.payload.RoomID {
			continue
		}
		if row.Occupied(t.payload.Start, t.payload.Slots, cfg.Booking.Subject) {
			return true, jointips.RoomLabel(row.Place, row.Name)
		}
		return false, ""
	}
	return false, ""
}

// waitUntil 은 목표 시각까지 기다린다(§6.2).
//
// 세 단계로 좁힌다: 타이머로 100ms 전까지 → 짧은 슬립으로 1ms 전까지 → 스핀.
// 절대 목표보다 **먼저** 발사하지 않는 것이 중요하다. fire_at 을 정각+50ms 로 두는
// 이유(서버 시계가 우리보다 느릴 때의 여유)가 조기 발사로 상쇄되면 안 된다.
func waitUntil(ctx context.Context, at time.Time) error {
	if d := time.Until(at) - 100*time.Millisecond; d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for time.Until(at) > time.Millisecond {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(200 * time.Microsecond)
	}
	for time.Now().Before(at) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// ── 대상 만들기 ────────────────────────────────────────────────────────────

type target struct {
	label     string
	roomLabel string
	payload   jointips.Payload
}

func buildTargets(cfg config.Config, dateDot string) []target {
	out := make([]target, 0, len(cfg.Booking.Targets))
	for _, t := range cfg.Booking.Targets {
		label := fmt.Sprintf("회의실 %d", t.RoomID)
		roomLabel := ""
		if rc := jointips.LookupRoom(t.RoomID); rc != nil {
			label = rc.Place + " " + rc.Name
			roomLabel = jointips.RoomLabel(rc.Place, rc.Name)
		}
		out = append(out, target{
			label:     fmt.Sprintf("%s %s", label, jointips.TimeRange(t.Start, t.DurationSlots)),
			roomLabel: roomLabel,
			payload: jointips.Payload{
				Date: dateDot, Start: t.Start, Slots: t.DurationSlots,
				Building: t.Building, Floor: t.Floor, RoomID: fmt.Sprint(t.RoomID),
				Contact: cfg.Booking.Contact, Manager: cfg.Booking.Manager,
				Subject: cfg.Booking.Subject, Content: cfg.Booking.Content,
			},
		})
	}
	return out
}

// verifyTargets 는 설정의 회의실 코드가 실제 조회 결과와 맞는지 본다(§5.0 4번).
// 어긋나면 조회 응답 값을 신뢰하고 덮어쓴 뒤 경고를 남긴다.
func verifyTargets(run *Live, targets []target, rows []jointips.Row) {
	for i := range targets {
		for _, row := range rows {
			if row.RoomID != targets[i].payload.RoomID {
				continue
			}
			if row.Building != targets[i].payload.Building || row.Floor != targets[i].payload.Floor {
				run.warn("회의실 %s 의 빌딩/층 코드가 설정과 다릅니다 (설정 %s/%s → 조회 %s/%s) — 조회 값을 씁니다",
					row.RoomID, targets[i].payload.Building, targets[i].payload.Floor, row.Building, row.Floor)
				targets[i].payload.Building = row.Building
				targets[i].payload.Floor = row.Floor
			}
			targets[i].roomLabel = jointips.RoomLabel(row.Place, row.Name)
			break
		}
	}
}

// pendingIdx 는 아직 확보하지 못한 대상들의 인덱스를 순서대로 돌려준다.
// 성공한 대상은 후보에서 빠지므로 같은 자리를 두 번 잡으려 들지 않는다.
func pendingIdx(targets []target, secured map[int]bool) []int {
	out := make([]int, 0, len(targets))
	for i := range targets {
		if !secured[i] {
			out = append(out, i)
		}
	}
	return out
}

// findFallback 은 빈 자리를 찾되, 이미 확보한 예약과 같은 자리는 제외한다.
// (방금 잡은 슬롯은 재조회에서 selected 로 보이므로 대개 자연히 걸러지지만,
//
//	조회 시점 차이로 아직 available 로 보일 수 있어 한 겹 더 막는다.)
func findFallback(cfg config.Config, rows []jointips.Row, already []Booked) *jointips.Candidate {
	fb := cfg.Booking.Fallback
	startMin, err := config.ParseHHMM(fb.Start)
	if err != nil {
		return nil
	}
	c := jointips.MatchWithPreference(rows, fb.Building, fb.PreferFloor,
		startMin, fb.StartWindowMinutes, fb.MinSlots, fb.MaxSlots)
	if c == nil {
		return nil
	}
	cand := Booked{
		Room:      jointips.RoomLabel(c.Row.Place, c.Row.Name),
		TimeRange: jointips.TimeRange(c.Start(), c.Slots),
	}
	for _, b := range already {
		if b == cand {
			return nil
		}
	}
	return c
}

func targetFromCandidate(cfg config.Config, c *jointips.Candidate, dateDot string) target {
	return target{
		label:     fmt.Sprintf("[폴백] %s %s", c.Row.Label(), jointips.TimeRange(c.Start(), c.Slots)),
		roomLabel: jointips.RoomLabel(c.Row.Place, c.Row.Name),
		payload: jointips.Payload{
			Date: dateDot, Start: c.Start(), Slots: c.Slots,
			Building: c.Row.Building, Floor: c.Row.Floor, RoomID: c.Row.RoomID,
			Contact: cfg.Booking.Contact, Manager: cfg.Booking.Manager,
			Subject: cfg.Booking.Subject, Content: cfg.Booking.Content,
		},
	}
}

// isFloodAlert 는 gnuboard 의 도배 방지(cf_delay_sec)에 걸렸는지 본다.
// "너무 빠른 시간내에 게시물을 연속해서 올릴 수 없습니다." — 자리가 없어서가 아니라
// 같은 계정이 방금 글을 썼기 때문이므로, 재시도가 아니라 '대기'로 대응해야 한다.
func isFloodAlert(alert string) bool {
	if alert == "" {
		return false
	}
	for _, k := range []string{"너무 빠른", "연속해서"} {
		if contains(alert, k) {
			return true
		}
	}
	return false
}

// sleepCtx 는 중단 가능한 대기다. ctx 가 끝나면 false.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// isQuotaAlert 는 재시도가 무의미한 한도 초과 메시지인지 본다.
func isQuotaAlert(alert string) bool {
	for _, k := range []string{"2번만", "2건", "하루", "초과"} {
		if len(alert) > 0 && contains(alert, k) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func dash(s string) string {
	if s == "" {
		return "(없음)"
	}
	return s
}

// humanOffset 은 시계 오차를 사람이 읽는 문구로 만든다.
func humanOffset(d time.Duration) string {
	sign := "느립니다"
	if d > 0 {
		sign = "빠릅니다"
	} else {
		d = -d
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.1f시간 %s", d.Hours(), sign)
	case d >= time.Minute:
		return fmt.Sprintf("%.1f분 %s", d.Minutes(), sign)
	default:
		return fmt.Sprintf("%.1f초 %s", d.Seconds(), sign)
	}
}
