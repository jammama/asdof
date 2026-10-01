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
	Date      string `json:"date"`       // "2026-09-22" — 건마다 대상 날짜가 다를 수 있다
	TimeRange string `json:"time_range"` // "14:00~18:00"
	Account   string `json:"account"`    // 어느 계정으로 잡았는지 (계정이 여러 개일 때)
}

func (b Booked) String() string {
	if b.Date == "" {
		return b.Room + " " + b.TimeRange
	}
	return b.Date + " " + b.Room + " " + b.TimeRange
}

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
	// Entries 는 지정 스케줄에서 이번에 실행할 항목들이다(같은 실행일에 여러 건 가능).
	// 비어 있으면 「예약 설정」의 값으로 Count 건을 만든다.
	Entries []config.ScheduleEntry
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

	// 이번 발사로 잡을 예약들을 만든다. 지정 스케줄이면 항목 하나가 예약 한 건이고,
	// 요일 반복/수동이면 「예약 설정」의 건수만큼 만든다.
	jobs, err := buildJobs(cfg, opt.Entries, base, loc)
	if err != nil {
		run.fail("%v", err)
		run.finish(StatusFailed, "지정 예약 설정 오류")
		return
	}
	if len(jobs) == 0 {
		run.fail("시도할 예약이 없습니다")
		run.finish(StatusFailed, "대상 없음")
		return
	}
	run.edit(func(r *Run) {
		r.TargetDate = jobs[0].date
		r.Want = len(jobs)
	})

	if dryRun {
		run.warn("드라이런 — 예약 신청 POST 는 보내지 않습니다")
	}
	for i, j := range jobs {
		run.info("예약 %d/%d — %s", i+1, len(jobs), j.describe(cfg))
	}

	// ── 워밍업 ──
	//
	// 계정을 전부 **미리** 로그인해 둔다. 사이트 한도가 계정별이라 한 계정이 막히면 다음으로
	// 넘어가야 하는데, 그 시점에 로그인하면 발사 직후의 가장 비싼 순간에 왕복이 하나 더 붙는다.
	seats, err := rn.warmup(ctx, run, cfg, pinnedAccounts(jobs))
	if err != nil {
		run.fail("%v", err)
		run.finish(StatusFailed, "로그인 실패")
		return
	}
	client := seats[0].client

	// 대상 사이트의 시계를 잰다.
	// 예약 창은 사이트 서버가 자기 시계로 계산하므로(§0.4), 새 날짜가 열리는 순간은
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

	// ── 날짜별 사전 점검 ──
	//
	// 한 번 발사에 여러 건이 딸려 오고 건마다 대상 날짜가 다를 수 있다. 날짜 하나가
	// 막혀 있다고 나머지까지 버리지 않고, 그 날짜의 건만 접는다.
	rowsByDate := map[string][]jointips.Row{}
	for _, date := range jobDates(jobs) {
		day := jobs[indexOfDate(jobs, date)].day
		if err := checkWindow(run, base, day, siteOffset, offErr == nil); err != nil {
			skipDate(run, jobs, date, err.Error(), "예약 창 밖")
			continue
		}
		if days, err := client.BookableDays(ctx); err == nil && len(days) > 0 {
			key := [...]string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}[int(day.Weekday())]
			if days[key] == "N" {
				skipDate(run, jobs, date, fmt.Sprintf("%s(%s)는 사이트가 예약을 받지 않는 요일입니다", date, weekdayKo(day)), "운영 요일 아님")
				continue
			}
		}
		// 건물 필터 없이 전체를 조회한다. 폴백의 건물/층 선호는 클라이언트에서 거르므로
		// 전체를 받아 두는 편이 지정 대상 검증까지 함께 할 수 있어 낫다.
		rows, err := client.Timetable(ctx, "", "", date)
		if err != nil {
			run.warn("%s 현황 조회 실패: %v (지정 대상으로만 진행)", date, err)
			continue
		}
		open := 0
		for _, r := range rows {
			if r.Bookable {
				open++
			}
		}
		run.info("%s 현황 — 공간 %d곳 (신청 가능 %d곳)", date, len(rows), open)
		if open == 0 {
			run.warn("%s 에 신청을 받는 공간이 하나도 없습니다 — 사이트에서 모두 '예약가능'이 아닌 상태일 수 있습니다", date)
		}
		rowsByDate[date] = rows
	}

	// 건마다 후보를 확정한다 — 조회 값으로 코드를 맞추고, 폴백 후보를 한 개 얹는다.
	for _, j := range jobs {
		if j.skip != "" {
			continue
		}
		rows := rowsByDate[j.date]
		if len(rows) == 0 {
			continue
		}
		verifyTargets(run, j.targets, rows)
		if j.fallback.Enabled {
			if c := findFallback(j.fallback, rows, nil); c != nil {
				j.targets = append(j.targets, targetFromCandidate(c))
				run.info("%s 폴백 후보: %s %s (%d슬롯)", j.label, c.Row.Label(), c.Start(), c.Slots)
			} else {
				run.warn("%s 폴백 조건에 맞는 빈 자리를 찾지 못했습니다", j.label)
			}
		}
	}
	for _, j := range jobs {
		if j.skip == "" && len(j.targets) == 0 {
			j.skip, j.skipCode = "시도할 회의실이 없습니다", "대상 없음"
		}
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
			} else {
				for _, date := range jobDates(jobs) {
					want := countJobsOn(jobs, date)
					n, err := notionClient.CountRowsOn(ctx, dbID, notionSchema, date)
					if err != nil {
						continue
					}
					if n >= want {
						skipDate(run, jobs, date, fmt.Sprintf("Notion 에 이미 %d건 있습니다 (목표 %d건)", n, want), "")
						continue
					}
					if n > 0 {
						run.info("%s 기록이 Notion 에 %d건 있습니다 — 나머지 %d건을 시도합니다", date, n, want-n)
						// 이미 잡힌 만큼 앞에서부터 접는다.
						dropJobs(jobs, date, n)
					}
				}
			}
		}
	}

	if pendingJobs(jobs) == 0 {
		// 왜 하나도 안 남았는지가 중요하다 — 막혀서(요일·창 밖)면 실패로 알려야
		// 사람이 설정을 고친다. 이미 잡혀 있어서면 정상적인 건너뜀이다.
		if why := blockedReason(jobs); why != "" {
			run.fail("시도할 예약이 남지 않았습니다 — %s", why)
			run.finish(StatusFailed, why)
			return
		}
		run.good("시도할 예약이 남지 않았습니다")
		run.finish(StatusSkipped, "이미 처리된 날짜")
		return
	}

	// 드라이런은 여기서 페이로드만 보여주고 끝낸다.
	if dryRun {
		for _, j := range jobs {
			if j.skip != "" {
				run.warn("%s — 건너뜀: %s", j.label, j.skip)
				continue
			}
			for i, t := range j.targets {
				r, err := t.reservation(j.plan, j.date)
				if err != nil {
					run.fail("%s 후보 #%d 페이로드 오류: %v", j.label, i+1, err)
					continue
				}
				run.info("%s 후보 #%d %s — spaceCd=%s bldgCd=%s date=%s slots=%s",
					j.label, i+1, t.label, r.SpaceCd, r.BldgCd, r.ReserveDate, strings.Join(r.SlotTimes, ","))
			}
		}
		if notionSchema != nil {
			run.info("Notion 속성 매칭 — 제목:%s 현황:%s 회의실:%s 시간:%s 예약자:%s 날짜:%s",
				dash(notionSchema.Title), dash(notionSchema.Usage), dash(notionSchema.Room),
				dash(notionSchema.Period), dash(notionSchema.Booker), dash(notionSchema.Date))
		}
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
	gap := time.Duration(cfg.Booking.GapSeconds) * time.Second
	want := len(jobs)

	// taken 은 이번 실행에서 이미 잡은 자리다. 두 건이 같은 자리를 노리지 않게 막는다.
	taken := map[string]bool{}
	var lastPost time.Time
	var lastAlert string

	// 예약 '한 건'마다 재시도 예산을 새로 준다. 건 사이에는 간격을 지켜야 하는데,
	// 그 대기가 앞 건의 예산을 잡아먹으면 두 번째를 아예 못 쏘기 때문이다.
	for _, j := range jobs {
		if ctx.Err() != nil {
			run.fail("중단됨")
			run.finish(StatusFailed, "중단됨")
			return
		}
		if j.skip != "" {
			run.warn("%s — 건너뜀: %s", j.label, j.skip)
			continue
		}
		if !lastPost.IsZero() && gap > 0 {
			if w := gap - time.Since(lastPost); w > 0 {
				run.info("연속 신청 간격 %.0f초 대기", w.Seconds())
				if !sleepCtx(ctx, w) {
					break
				}
			}
		}

		phaseStart := time.Now()
		phaseAttempts := 0

		// Go 에서 switch 안의 break 는 switch 만 빠져나간다. 루프를 끊으려면 라벨이 필요하다.
	phase:
		for phaseAttempts < retry.MaxAttempts && time.Since(phaseStart) < retry.MaxDuration() {
			if ctx.Err() != nil {
				run.fail("중단됨")
				run.finish(StatusFailed, "중단됨")
				return
			}
			pending := j.pending(taken)
			if len(pending) == 0 {
				run.warn("%s — 남은 후보가 없습니다", j.label)
				break phase
			}
			t := pending[phaseAttempts%len(pending)]

			// 이 건을 맡을 계정을 고른다. 건이 예약자를 못박아 뒀으면 그 계정만 쓴다.
			se := pickSeat(seatsFor(seats, j.plan.accountID), t.slots)
			if se == nil {
				run.warn("%s — 쓸 수 있는 계정이 한도에 찼습니다", j.label)
				break phase
			}

			req, err := t.reservation(j.plan, j.date)
			if err != nil {
				run.warn("%s 후보 %s 페이로드 오류: %v", j.label, t.label, err)
				taken[t.key(j.date)] = true // 고칠 수 없는 후보는 빼 둔다
				continue
			}
			phaseAttempts++
			attempt := run.nextAttempt()
			res, err := se.client.Submit(ctx, req)
			if err != nil {
				run.warn("시도 %d — 전송 오류: %v", attempt, err)
				sleepCtx(ctx, retry.Interval())
				continue
			}

			switch {
			case !res.OK():
				lastAlert = res.Message
				if lastAlert == "" {
					lastAlert = fmt.Sprintf("code %d", res.Code)
				}
				run.warn("시도 %d — %s (%s · %s, %dms)",
					attempt, lastAlert, se.acc.Name(), t.label, res.Elapsed.Milliseconds())
				if isLimitMessage(res.Message) {
					// 한도는 계정별이다. 이 계정만 접고 다음 계정으로 같은 자리를 다시 노린다.
					se.exhausted = true
					if pickSeat(seatsFor(seats, j.plan.accountID), t.slots) == nil {
						run.warn("쓸 수 있는 계정이 모두 한도에 걸렸습니다 — 이 건은 포기합니다")
						break phase
					}
					run.info("%s 는 한도에 걸렸습니다 — 다음 계정으로 넘어갑니다", se.acc.Name())
					continue
				}
				sleepCtx(ctx, retry.Interval())

			default:
				run.info("시도 %d — 접수됨 (%s · %s, %dms) — 검증 중",
					attempt, se.acc.Name(), t.label, res.Elapsed.Milliseconds())
				// 최종 근거는 내 예약 목록이다. 신청이 200 을 받아도 승인 흐름이 따로 있으므로
				// "내 예약에 그 자리가 살아 있는가"로 확인한다. 계정별 목록이므로 그 계정으로 묻는다.
				if ok, label := verifyBooked(ctx, se.client, t, j.date); ok {
					j.done = true
					taken[t.key(j.date)] = true
					lastPost = time.Now()
					se.got++
					se.slots += t.slots
					b := Booked{Room: label, Date: j.date,
						TimeRange: jointips.TimeRange(t.start, t.slots), Account: se.acc.Name()}
					run.edit(func(r *Run) { r.Bookings = append(r.Bookings, b) })
					run.good("예약 성공 (%d/%d) — %s %s · %s", doneJobs(jobs), want, j.date, b, se.acc.Name())
					rn.record(ctx, run, cfg, j.plan, notionClient, notionSchema, dbID, t, j.date, label, true, "", se.name)
					break phase
				}
				run.warn("시도 %d — 내 예약 목록에서 확인되지 않음", attempt)
				sleepCtx(ctx, retry.Interval())
			}

			// 3회 연속 실패면 상황이 바뀌었을 수 있으니 조회해서 후보를 보충한다(§6.3)
			if phaseAttempts%3 == 0 && j.fallback.Enabled {
				if rows, err := client.Timetable(ctx, "", "", j.date); err == nil {
					if c := findFallback(j.fallback, rows, run.Snapshot().Bookings); c != nil {
						j.targets = append(j.targets, targetFromCandidate(c))
						run.info("%s 후보 보충: %s %s (%d슬롯)", j.label, c.Row.Label(), c.Start(), c.Slots)
					}
				}
			}
		}
		if !j.done {
			run.warn("%s — 확보하지 못했습니다", j.label)
		}
	}

	got := run.Snapshot().Bookings
	if len(got) > 0 {
		parts := make([]string, 0, len(got))
		for _, b := range got {
			parts = append(parts, b.String())
		}
		summary := strings.Join(parts, " + ")
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
	if len(jobs) > 0 && len(jobs[0].targets) > 0 {
		rn.record(ctx, run, cfg, jobs[0].plan, notionClient, notionSchema, dbID,
			jobs[0].targets[0], jobs[0].date, "", false, msg, "")
	}
	run.finish(StatusFailed, msg)
}

// record 는 Notion 기록을 남긴다. 실패해도 예약 성공 판정을 뒤집지 않는다(§7.7).
func (rn *Runner) record(ctx context.Context, run *Live, cfg config.Config, pl plan,
	c *notion.Client, s *notion.Schema, dbID string, t target, date, roomLabel string, ok bool, reason, booker string) {
	if c == nil || s == nil {
		return
	}
	if !ok && !cfg.Notion.RecordFailure {
		return
	}
	subject := pl.subject
	usage := jointips.UsageLine(subject, t.start, t.slots)
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
		Period:  jointips.TimeRange(t.start, t.slots),
		// 계정을 여러 개 쓰면 실제로 잡은 사람이 계정마다 다르다. 사이트가 알려준 회원
		// 이름을 그대로 쓰고, 못 받았을 때만 설정의 담당자명으로 떨어진다.
		// 지정 예약이 담당자를 적어 뒀으면 그 이름이 먼저다. 다음이 사이트가 알려준
		// 계정 회원명, 마지막이 「예약 설정」의 담당자명.
		Booker: firstNonBlank(pl.manager, booker, cfg.Booking.Manager),
		Date:   date,
	}
	page, err := c.CreatePage(ctx, dbID, s, rec)
	if err != nil {
		run.warn("Notion 기록 실패: %v (예약 결과에는 영향 없음)", err)
		return
	}
	run.info("Notion 에 기록했습니다 — %s", page.URL)
}

// verifyBooked 는 신청이 실제로 잡혔는지 내 예약 목록으로 확인한다(2차 판정 = ground truth).
//
// 예전에는 현황을 재조회해 그 칸이 '내 회의명'으로 차 있는지 봤지만, 새 슬롯 API 는
// 누가 잡았는지 알려주지 않는다(AVAILABLE/UNAVAILABLE 뿐). 그래서 판정 근거를
// "내 예약 목록에 이 공간·이 시각의 살아 있는 예약이 있는가"로 옮겼다.
func verifyBooked(ctx context.Context, client *jointips.Client, t target, date string) (bool, string) {
	mine, err := client.MyReservations(ctx, date, date)
	if err != nil {
		return false, ""
	}
	end, err := jointips.EndTime(t.start, t.slots)
	if err != nil {
		return false, ""
	}
	for _, m := range mine {
		if !m.Live() || m.SpaceCd != t.spaceCd || m.ReserveDate != date {
			continue
		}
		if m.StartTime != t.start || m.EndTime != end {
			continue
		}
		label := jointips.RoomLabel(m.BldgNm, "", m.SpaceNm)
		if label == "" {
			label = t.roomLabel
		}
		return true, label
	}
	return false, ""
}

// checkWindow 는 대상 날짜가 예약 창 안에 있는지 본다.
//
// 기준 시계가 중요하다. 창을 계산하는 건 우리가 아니라 사이트 서버이고, 우리는 자정
// 직후에 쏜다 — 두 시계가 자정 경계를 사이에 두고 갈리면 '아직 안 열린 날짜'를 잡으려
// 들게 된다. 그래서 실측 오차를 얹은 사이트 시각으로 오늘을 정한다.
func checkWindow(run *Live, base, target time.Time, siteOffset time.Duration, measured bool) error {
	siteNow := base
	if measured {
		siteNow = base.Add(siteOffset)
	}
	loc := base.Location()
	siteToday := time.Date(siteNow.In(loc).Year(), siteNow.In(loc).Month(), siteNow.In(loc).Day(), 0, 0, 0, 0, loc)
	want := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, loc)
	days := int(want.Sub(siteToday).Hours() / 24)

	switch {
	case days < 0:
		return fmt.Errorf("%s 는 사이트 기준으로 이미 지난 날짜입니다 (사이트 오늘 %s)",
			want.Format("2006-01-02"), siteToday.Format("2006-01-02"))
	case days > config.BookingWindowDays:
		return fmt.Errorf("%s 는 예약 창 밖입니다 — 사이트는 오늘~+%d일만 엽니다 (사이트 기준 +%d일)",
			want.Format("2006-01-02"), config.BookingWindowDays, days)
	case days == config.BookingWindowDays:
		// 막 열린 날짜를 잡으러 온 정상 경로다. 창 경계라는 것만 알려 둔다.
		run.info("대상은 창 경계(+%d일) — 사이트 자정에 막 열린 날짜입니다", days)
	}
	return nil
}

func firstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// weekdayKo 는 로그에 쓸 한 글자 요일이다.
func weekdayKo(t time.Time) string {
	return string([]rune("일월화수목금토")[int(t.Weekday())])
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

// ── 실행 계획 ──────────────────────────────────────────────────────────────

// plan 은 이번 실행에 실제로 쓸 값들이다.
//
// 「예약 설정」이 바탕이고, 지정 스케줄 항목이 있으면 채워진 필드만 덮어쓴다.
// 비운 필드는 그대로 상속되므로, 날짜만 찍어 둔 항목도 예전과 똑같이 동작한다.
type plan struct {
	targetDay time.Time
	subject   string
	// manager 는 지정 예약이 못박은 담당자명이다. 비어 있으면 Notion 기록에서
	// 계정 회원명 → 「예약 설정」의 담당자명 순으로 떨어진다.
	manager    string
	purposeCd  string
	purposeEtc string
	accountID  string // "" = 등록된 계정을 앞에서부터
}

func resolvePlan(cfg config.Config, e *config.ScheduleEntry, base time.Time, loc *time.Location) (plan, error) {
	p := plan{
		targetDay:  base.AddDate(0, 0, cfg.Booking.DateOffsetDays),
		subject:    cfg.Booking.Subject,
		purposeCd:  cfg.Booking.PurposeCd,
		purposeEtc: cfg.Booking.PurposeEtc,
	}
	if e == nil {
		return p, nil
	}
	t, err := e.Target(cfg.Booking.DateOffsetDays, loc)
	if err != nil {
		return plan{}, fmt.Errorf("지정 예약의 날짜를 읽을 수 없습니다: %w", err)
	}
	p.targetDay = t
	p.subject = firstNonBlank(e.Subject, p.subject)
	p.manager = strings.TrimSpace(e.Manager)
	p.purposeCd = firstNonBlank(e.PurposeCd, p.purposeCd)
	p.accountID = e.AccountID
	return p, nil
}

// accountNote 는 로그에 붙일 "· 예약자 …" 조각이다.
func (p plan) accountNote(cfg config.Config) string {
	if p.accountID == "" {
		return ""
	}
	if a, ok := cfg.Site.Account(p.accountID); ok {
		return " · 예약자 " + a.Name()
	}
	return ""
}

// ── 예약 건(job) ───────────────────────────────────────────────────────────

// job 은 **회의실 하나를 잡는 일** 한 건이다.
//
// targets 는 그 하나를 잡기 위한 후보 목록이고, 위에서부터 돌려 가며 시도해
// 하나가 확인되면 그 건은 끝난다(여러 개를 잡는 게 아니다).
// 후보가 다 실패하면 fallback 이 같은 건물에서 빈 자리를 찾아 후보를 보충한다.
type job struct {
	label    string // 로그용 ("예약 1/2")
	entryID  string // 지정 스케줄 항목 id (요일 반복이면 빈 값)
	date     string // 대상 날짜 "2026-09-22"
	day      time.Time
	plan     plan
	targets  []target
	fallback config.Fallback

	done bool
	skip string // 비어 있지 않으면 시도하지 않는다(사람이 읽을 이유)
	// skipCode 는 실행 전체가 접혔을 때 화면에 남길 짧은 사유다.
	// 빈 값이면 '멱등성으로 건너뜀'(정상)으로 본다.
	skipCode string
}

// describe 는 실행 전 요약 한 줄이다.
func (j *job) describe(cfg config.Config) string {
	b := fmt.Sprintf("%s · %q · 후보 %d개", j.date, j.plan.subject, len(j.targets))
	if j.fallback.Enabled {
		b += " + 폴백"
	}
	return b + j.plan.accountNote(cfg)
}

// pending 은 아직 남은 후보다. 이번 실행에서 이미 잡은 자리는 뺀다.
func (j *job) pending(taken map[string]bool) []target {
	out := make([]target, 0, len(j.targets))
	for _, t := range j.targets {
		if !taken[t.key(j.date)] {
			out = append(out, t)
		}
	}
	return out
}

// key 는 "이 날짜의 이 공간 이 시각" 을 가리키는 값이다.
func (t target) key(date string) string { return date + "|" + t.spaceCd + "|" + t.start }

// buildJobs 는 이번 발사로 잡을 건들을 만든다.
//
// 지정 스케줄이면 항목 하나가 건 하나다(같은 실행일에 여러 건을 넣을 수 있다).
// 요일 반복/수동이면 「예약 설정」의 건수만큼 만들고, 모두 같은 후보 목록을 공유한다.
func buildJobs(cfg config.Config, entries []config.ScheduleEntry, base time.Time, loc *time.Location) ([]*job, error) {
	if len(entries) > 0 {
		jobs := make([]*job, 0, len(entries))
		for i, e := range entries {
			pl, err := resolvePlan(cfg, &e, base, loc)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, &job{
				label:    fmt.Sprintf("예약 %d/%d", i+1, len(entries)),
				entryID:  e.ID,
				date:     pl.targetDay.Format("2006-01-02"),
				day:      pl.targetDay,
				plan:     pl,
				targets:  buildTargets(e.EffectiveTargets(cfg.Booking)),
				fallback: e.EffectiveFallback(cfg.Booking),
			})
		}
		return jobs, nil
	}

	pl, err := resolvePlan(cfg, nil, base, loc)
	if err != nil {
		return nil, err
	}
	want := cfg.Booking.Count
	if want < 1 {
		want = 1
	}
	date := pl.targetDay.Format("2006-01-02")
	jobs := make([]*job, 0, want)
	for i := 0; i < want; i++ {
		jobs = append(jobs, &job{
			label:    fmt.Sprintf("예약 %d/%d", i+1, want),
			date:     date,
			day:      pl.targetDay,
			plan:     pl,
			targets:  buildTargets(cfg.Booking.Targets),
			fallback: cfg.Booking.Fallback,
		})
	}
	return jobs, nil
}

// jobDates 는 건들이 노리는 대상 날짜를 중복 없이 순서대로 돌려준다.
func jobDates(jobs []*job) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		if !seen[j.date] {
			seen[j.date] = true
			out = append(out, j.date)
		}
	}
	return out
}

func indexOfDate(jobs []*job, date string) int {
	for i, j := range jobs {
		if j.date == date {
			return i
		}
	}
	return 0
}

func countJobsOn(jobs []*job, date string) int {
	n := 0
	for _, j := range jobs {
		if j.date == date && j.skip == "" {
			n++
		}
	}
	return n
}

// skipDate 는 그 날짜의 건들을 통째로 접는다. 다른 날짜의 건은 그대로 간다.
// code 는 실행 전체가 접혔을 때 화면에 남길 짧은 사유다.
func skipDate(run *Live, jobs []*job, date, why, code string) {
	run.warn("%s — %s", date, why)
	for _, j := range jobs {
		if j.date == date && j.skip == "" {
			j.skip, j.skipCode = why, code
		}
	}
}

// blockedReason 은 접힌 건들 중 '막혀서' 접힌 첫 사유다. 멱등성으로 건너뛴 건은 세지 않는다.
func blockedReason(jobs []*job) string {
	for _, j := range jobs {
		if j.skip != "" && j.skipCode != "" {
			return j.skipCode
		}
	}
	return ""
}

// dropJobs 는 그 날짜의 건을 앞에서부터 n 개 접는다(Notion 에 이미 있는 만큼).
func dropJobs(jobs []*job, date string, n int) {
	for _, j := range jobs {
		if n <= 0 {
			return
		}
		if j.date == date && j.skip == "" {
			j.skip = "Notion 에 이미 기록된 몫"
			n--
		}
	}
}

func pendingJobs(jobs []*job) int {
	n := 0
	for _, j := range jobs {
		if j.skip == "" {
			n++
		}
	}
	return n
}

func doneJobs(jobs []*job) int {
	n := 0
	for _, j := range jobs {
		if j.done {
			n++
		}
	}
	return n
}

// pinnedAccounts 는 건들이 못박은 계정 id 모음이다. 하나라도 비어 있으면(자동) nil 을
// 돌려줘 모든 계정을 로그인하게 한다.
func pinnedAccounts(jobs []*job) []string {
	var ids []string
	for _, j := range jobs {
		if j.plan.accountID == "" {
			return nil
		}
		ids = append(ids, j.plan.accountID)
	}
	return ids
}

// seatsFor 는 이 건이 쓸 수 있는 좌석만 거른다.
func seatsFor(seats []*seat, accountID string) []*seat {
	if accountID == "" {
		return seats
	}
	out := make([]*seat, 0, 1)
	for _, s := range seats {
		if s.acc.ID == accountID {
			out = append(out, s)
		}
	}
	return out
}

// ── 계정 좌석 ──────────────────────────────────────────────────────────────

// seat 은 로그인해 둔 계정 하나다. 한도에 걸리면 exhausted 로 접고 다음 좌석으로 넘어간다.
type seat struct {
	acc    config.Account
	client *jointips.Client
	name   string // 사이트가 알려준 회원 이름 (Notion 예약자에 쓴다)

	got       int  // 이 좌석으로 확보한 건수
	slots     int  // 이 좌석으로 쓴 슬롯 수 (1일 5시간 = 10칸)
	exhausted bool // 한도 메시지를 받았거나 자체 한도를 채웠다
}

// full 은 이 좌석으로 더 잡을 수 없는지다.
// 사이트가 거절하기 전에 우리가 먼저 알 수 있으면 헛방 한 번을 아낀다.
func (s *seat) full(next int) bool {
	return s.exhausted ||
		s.got >= config.MaxBookingsPerDay ||
		s.slots+next > config.MaxDailySlots
}

// warmup 은 쓸 수 있는 계정을 전부 로그인한다.
// 하나라도 성공하면 진행한다 — 계정 하나가 잠겼다고 나머지까지 포기할 이유는 없다.
func (rn *Runner) warmup(ctx context.Context, run *Live, cfg config.Config, onlyIDs []string) ([]*seat, error) {
	accounts := cfg.Site.ReadyAccounts()
	if len(accounts) == 0 {
		return nil, fmt.Errorf("쓸 수 있는 계정이 없습니다")
	}
	// 모든 건이 예약자를 못박아 뒀으면 그 계정들만 로그인한다 — 쓰지도 않을 계정에
	// 로그인하느라 발사 직전 시간을 버릴 이유가 없다. 하나라도 '자동'이면 전부 연다.
	if len(onlyIDs) > 0 {
		want := map[string]bool{}
		for _, id := range onlyIDs {
			want[id] = true
		}
		var only []config.Account
		for _, a := range accounts {
			if want[a.ID] {
				only = append(only, a)
			}
		}
		if len(only) == 0 {
			return nil, fmt.Errorf("지정한 예약자 계정을 쓸 수 없습니다 (지워졌거나 꺼져 있습니다)")
		}
		accounts = only
	}
	var seats []*seat
	for _, a := range accounts {
		pw, err := rn.store.AccountPassword(a.ID)
		if err != nil || pw == "" {
			run.warn("%s — 비밀번호를 읽을 수 없어 건너뜁니다", a.Name())
			continue
		}
		c, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, cfg.Site.Timeout())
		if err != nil {
			return nil, fmt.Errorf("클라이언트 생성 실패: %w", err)
		}
		run.info("로그인 중… (%s)", a.Name())
		if err := c.Login(ctx, a.Username, pw); err != nil {
			run.warn("%s 로그인 실패: %v — 이 계정은 건너뜁니다", a.Name(), err)
			continue
		}
		nm := c.Member().UserNm
		seats = append(seats, &seat{acc: a, client: c, name: nm})
		run.good("로그인 성공 — %s (%s)", a.Name(), nm)
	}
	if len(seats) == 0 {
		return nil, fmt.Errorf("모든 계정의 로그인에 실패했습니다")
	}
	if len(seats) < len(accounts) {
		run.warn("계정 %d개 중 %d개만 로그인했습니다 — 확보 가능 건수가 줄어듭니다", len(accounts), len(seats))
	}
	return seats, nil
}

// pickSeat 은 다음 건을 맡을 좌석을 고른다. 앞 계정부터 한도까지 쓴다.
func pickSeat(seats []*seat, slots int) *seat {
	for _, s := range seats {
		if !s.full(slots) {
			return s
		}
	}
	return nil
}

// ── 대상 만들기 ────────────────────────────────────────────────────────────

type target struct {
	label     string // 로그용 ("현승빌딩(S3) 5층 회의실A 13:00~15:00")
	roomLabel string // Notion 용 ("현승 5A")
	spaceCd   string
	bldgCd    string
	start     string
	slots     int
}

// reservation 은 이 대상을 신청 페이로드로 펼친다.
func (t target) reservation(pl plan, date string) (jointips.Reservation, error) {
	times, err := jointips.SlotTimes(t.start, t.slots)
	if err != nil {
		return jointips.Reservation{}, err
	}
	return jointips.Reservation{
		BldgCd:      t.bldgCd,
		SpaceCd:     t.spaceCd,
		ReserveDate: date,
		SlotTimes:   times,
		Title:       pl.subject,
		PurposeCd:   pl.purposeCd,
		PurposeEtc:  pl.purposeEtc,
	}, nil
}

func buildTargets(list []config.Target) []target {
	out := make([]target, 0, len(list))
	for _, t := range list {
		label := t.Label
		if label == "" {
			label = t.SpaceCd
		}
		out = append(out, target{
			label:   fmt.Sprintf("%s %s", label, jointips.TimeRange(t.Start, t.DurationSlots)),
			spaceCd: t.SpaceCd,
			bldgCd:  t.BldgCd,
			start:   t.Start,
			slots:   t.DurationSlots,
		})
	}
	return out
}

// verifyTargets 는 설정의 공간 코드가 실제 조회 결과에 있는지 본다.
// 사이트가 공간을 없애거나 코드를 바꾸면 신청은 반드시 실패하므로, 발사 전에 알아야 한다.
// 겸사겸사 건물 코드와 표시 이름을 조회 값으로 맞춘다.
func verifyTargets(run *Live, targets []target, rows []jointips.Row) {
	for i := range targets {
		found := false
		for _, row := range rows {
			if row.SpaceCd != targets[i].spaceCd {
				continue
			}
			found = true
			if targets[i].bldgCd != row.BldgCd {
				run.warn("%s 의 건물 코드가 설정과 다릅니다 (설정 %s → 조회 %s) — 조회 값을 씁니다",
					row.SpaceCd, targets[i].bldgCd, row.BldgCd)
				targets[i].bldgCd = row.BldgCd
			}
			targets[i].label = fmt.Sprintf("%s %s", row.Label(), jointips.TimeRange(targets[i].start, targets[i].slots))
			targets[i].roomLabel = jointips.RoomLabel(row.BldgNm, row.FloorNo, row.SpaceNm)
			if !row.Bookable {
				run.warn("%s 는 이 날짜에 신청을 받지 않습니다 (운영 요일이 아니거나 상태가 '예약가능'이 아님)", row.Label())
			}
			break
		}
		if !found {
			run.warn("공간 코드 %s 를 현황에서 찾지 못했습니다 — 사이트에서 없어졌을 수 있습니다", targets[i].spaceCd)
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
func findFallback(fb config.Fallback, rows []jointips.Row, already []Booked) *jointips.Candidate {
	startMin, err := config.ParseHHMM(fb.Start)
	if err != nil {
		return nil
	}
	c := jointips.MatchWithPreference(rows, fb.BldgCd, fb.PreferFloor,
		startMin, fb.StartWindowMinutes, fb.MinSlots, fb.MaxSlots)
	if c == nil {
		return nil
	}
	cand := Booked{
		Room:      jointips.RoomLabel(c.Row.BldgNm, c.Row.FloorNo, c.Row.SpaceNm),
		TimeRange: jointips.TimeRange(c.Start(), c.Slots),
	}
	for _, b := range already {
		if b == cand {
			return nil
		}
	}
	return c
}

func targetFromCandidate(c *jointips.Candidate) target {
	return target{
		label:     fmt.Sprintf("[폴백] %s %s", c.Row.Label(), jointips.TimeRange(c.Start(), c.Slots)),
		roomLabel: jointips.RoomLabel(c.Row.BldgNm, c.Row.FloorNo, c.Row.SpaceNm),
		spaceCd:   c.Row.SpaceCd,
		bldgCd:    c.Row.BldgCd,
		start:     c.Start(),
		slots:     c.Slots,
	}
}

// isLimitMessage 는 재시도해도 소용없는 '계정 한도' 거절인지 본다.
//
// 새 사이트는 alert() 대신 JSON 의 message 로 사유를 알려준다. 자리 경쟁("해당 시간에
// 이미 예약이 있습니다")은 다시 쏴 볼 가치가 있지만, 계정 한도(1일 2회·5시간,
// 1회 3시간)는 오늘 안에 풀리지 않으므로 즉시 멈춰야 한다.
//
// 그래서 "이미" 같은 두루뭉술한 낱말이 아니라 한도 문구에만 있는 표현으로 가른다 —
// 자리 선점 메시지에도 "이미"가 들어 있어서 한 번 쏘고 포기해 버린 적이 있다.
func isLimitMessage(msg string) bool {
	for _, k := range []string{"1일 최대", "1회 최대", "한도", "초과", "예약하셨습니다", "모두 사용"} {
		if contains(msg, k) {
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
