package runner

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"asdof-taker/internal/config"
	"asdof-taker/internal/jointips"
)

// Scheduler 는 설정된 발사 시각마다 Runner 를 깨운다.
//
// cron 을 쓰지 않고 상주 데몬 안에서 대기한다: 초 단위 정밀도를 프로그램이
// 직접 맞춰야 하고(§6.1), 웹에서 설정을 바꾸면 즉시 반영돼야 하기 때문이다.
type Scheduler struct {
	store  *config.Store
	runner *Runner
	log    *slog.Logger

	reload chan struct{}

	mu     sync.Mutex
	next   time.Time     // 다음 발사 예정 시각, 우리 시계 기준 (zero = 예약 없음)
	why    string        // 예약이 없는 이유(화면 표시용)
	offset time.Duration // 대상 사이트 시계 - 우리 시계
	seen   bool
	probed time.Time // 마지막 측정 시각
}

func NewScheduler(store *config.Store, r *Runner, log *slog.Logger) *Scheduler {
	return &Scheduler{store: store, runner: r, log: log, reload: make(chan struct{}, 1)}
}

// Reload 는 설정이 바뀌었음을 알린다. 대기 중이면 즉시 다시 계산한다.
func (s *Scheduler) Reload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

// Next 는 다음 발사 예정 시각과, 없다면 그 이유를 돌려준다.
func (s *Scheduler) Next() (time.Time, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next, s.why
}

// SiteOffset 은 최근 실측한 대상 사이트의 시계 오차와 측정 시각이다.
func (s *Scheduler) SiteOffset() (time.Duration, bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset, s.seen, s.probed
}

// probeOffset 은 대상 사이트의 시계를 잰다. 로그인 없이 HEAD 한 번이면 되므로 가볍다.
// 발사 시각은 이 값으로 환산되므로, 예약이 걸려 있는 동안 주기적으로 갱신해 둔다.
func (s *Scheduler) probeOffset(ctx context.Context, cfg config.Config) {
	client, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, cfg.Site.Timeout())
	if err != nil {
		return
	}
	d, err := client.ClockOffset(ctx)
	if err != nil {
		s.log.Warn("사이트 시계 측정 실패", "err", err)
		return
	}
	s.mu.Lock()
	prev, had := s.offset, s.seen
	s.offset, s.seen, s.probed = d, true, time.Now()
	s.mu.Unlock()
	if !had || (d-prev).Abs() > 5*time.Second {
		s.log.Info("사이트 시계 오차 갱신", "offset", d.String())
	}
}

func (s *Scheduler) setNext(t time.Time, why string) {
	s.mu.Lock()
	s.next, s.why = t, why
	s.mu.Unlock()
}

// Run 은 ctx 가 끝날 때까지 도는 스케줄 루프다.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		cfg := s.store.Get()
		loc := cfg.Runtime.Location()

		wait := time.Hour
		var fireAt time.Time
		// 지정 스케줄이면 그 날짜의 항목 전부를 들고 간다 — 같은 실행일에 여러 건을 넣을 수 있다.
		var entries []config.ScheduleEntry

		switch {
		case !cfg.Schedule.Enabled:
			s.setNext(time.Time{}, "자동 예약이 꺼져 있습니다")
		default:
			// 발사 시각은 우리 시계 기준이다. 사이트 시계는 참고용으로만 재서 화면에 보여준다
			// (그 시계가 예약 창을 여닫으므로, 크게 어긋나면 발사 시각을 손봐야 한다).
			s.probeOffset(ctx, cfg)
			t, es, err := cfg.Schedule.NextRun(time.Now(), loc)
			if err != nil {
				s.setNext(time.Time{}, err.Error())
				break
			}
			entries = es
			if err := cfg.Validate(); err != nil {
				s.setNext(t, "설정 오류: "+err.Error())
				break
			}
			if err := cfg.ReadyToRun(); err != nil {
				s.setNext(t, err.Error())
				break
			}
			fireAt = t
			s.setNext(t, "")
			// 워밍업 선행 시간만큼 앞서 깨어난다(§2 타임라인).
			if w := time.Until(t.Add(-cfg.Schedule.WarmupLead())); w < wait {
				wait = w
			}
		}

		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.reload:
			timer.Stop()
			continue
		case <-timer.C:
		}

		// 워밍업 시각에 도달했는지 확인하고 발사 예약을 건다.
		if fireAt.IsZero() || time.Until(fireAt) > cfg.Schedule.WarmupLead()+time.Second {
			continue
		}
		at := fireAt
		s.log.Info("스케줄 실행 시작", "fire_at", at.Format(time.RFC3339Nano))
		if _, err := s.runner.Start(ctx, Options{Mode: "scheduled", FireAt: &at, Entries: entries}); err != nil {
			s.log.Warn("스케줄 실행을 시작하지 못함", "err", err)
			time.Sleep(time.Minute)
			continue
		}
		// 같은 발사 시각으로 두 번 들어가지 않도록 지나갈 때까지 기다린다.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(at.Add(2 * time.Minute))):
		}
	}
}

// nextSiteFire 는 "사이트 시계로 fire_at" 인 순간을 우리 시계의 절대 시각으로 돌려준다.
// 요일 필터도 사이트 기준 날짜로 판정한다 — 시계가 크게 어긋나면 날짜가 하루 밀릴 수 있다.
