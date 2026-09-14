// asdof-taker — jointips 회의실 자동 예약 서비스.
//
// 상주 데몬 하나가 두 가지 일을 한다:
//  1. 설정된 시각에 밀리초 정밀도로 예약을 신청한다 (내부 스케줄러, cron 불필요).
//  2. 그 설정을 관리하는 웹 UI 를 서빙한다 (taker.asdof.xyz, 관리자 비밀번호로 잠김).
//
// 자격증명은 웹에서 입력받아 서버에서 암호화 보관한다. 어떤 로그에도 남지 않는다.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"asdof-taker/internal/config"
	"asdof-taker/internal/runner"
	"asdof-taker/internal/web"
)

func main() {
	var (
		addr     = flag.String("addr", env("LISTEN_ADDR", "127.0.0.1:8780"), "listen 주소")
		stateDir = flag.String("state", env("STATE_DIR", "/var/lib/asdof-taker"), "설정·기록 디렉터리")
		logLevel = flag.String("log", env("LOG_LEVEL", "info"), "로그 레벨 (debug|info|warn|error)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(*logLevel)}))
	slog.SetDefault(log)

	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Error("상태 디렉터리를 만들 수 없습니다", "dir", *stateDir, "err", err)
		os.Exit(1)
	}

	vault, err := config.OpenVault(filepath.Join(*stateDir, "secret.key"))
	if err != nil {
		log.Error("암호화 키를 열 수 없습니다", "err", err)
		os.Exit(1)
	}
	store, err := config.Open(filepath.Join(*stateDir, "config.json"), vault)
	if err != nil {
		log.Error("설정을 열 수 없습니다", "err", err)
		os.Exit(1)
	}
	if err := ensureAdminPassword(store, log); err != nil {
		log.Error("관리자 비밀번호를 설정할 수 없습니다", "err", err)
		os.Exit(1)
	}

	run := runner.New(store, log)
	runsPath := filepath.Join(*stateDir, "runs.json")
	run.SetHistory(loadRuns(runsPath, log))
	run.OnFinish(func() { saveRuns(runsPath, run.History(50), log) })

	sched := runner.NewScheduler(store, run, log)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           web.New(store, run, sched, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go sched.Run(ctx)

	go func() {
		cfg := store.Get()
		next, why := sched.Next()
		log.Info("asdof-taker 기동", "addr", *addr, "state", *stateDir,
			"schedule_enabled", cfg.Schedule.Enabled, "dry_run", cfg.Runtime.DryRun,
			"next_fire", next.Format(time.RFC3339), "note", why)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("서버가 죽었습니다", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("종료 중…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	saveRuns(runsPath, run.History(50), log)
}

// ensureAdminPassword 는 관리 페이지 비밀번호가 반드시 있게 만든다.
//
//	저장된 비밀번호가 없으면  → ADMIN_PASSWORD 환경변수, 없으면 무작위 생성 후 로그에 1회 출력
//	저장된 비밀번호가 있으면  → 그대로 둔다
//	ADMIN_PASSWORD_RESET=1   → 환경변수 값으로 강제 재설정 (비밀번호를 잊었을 때)
//
// 환경변수를 매번 반영하지 않는 이유: /etc/asdof-taker.env 는 배포 후에도 남아 있으므로,
// 그러면 화면에서 바꾼 비밀번호가 서비스 재시작마다 옛 값으로 되돌아간다.
func ensureAdminPassword(store *config.Store, log *slog.Logger) error {
	envPW := os.Getenv("ADMIN_PASSWORD")
	cur := store.Get().Admin.PasswordHash
	reset := os.Getenv("ADMIN_PASSWORD_RESET") == "1" && envPW != ""

	if cur != "" && !reset {
		return nil
	}
	pw, generated := envPW, false
	if pw == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		pw = base64.RawURLEncoding.EncodeToString(b)
		generated = true
	}
	hash, err := config.HashPassword(pw)
	if err != nil {
		return err
	}
	if _, err := store.Update(func(c *config.Config) error {
		c.Admin.PasswordHash = hash
		return nil
	}); err != nil {
		return err
	}
	switch {
	case generated:
		// 최초 1회뿐이다. 화면에서 바꾸고 나면 다시는 출력되지 않는다.
		log.Warn("관리자 비밀번호를 새로 만들었습니다 — 로그인 후 반드시 변경하세요", "password", pw)
	case reset:
		log.Warn("ADMIN_PASSWORD_RESET=1 — 관리자 비밀번호를 환경변수 값으로 재설정했습니다")
	default:
		log.Info("ADMIN_PASSWORD 환경변수로 관리자 비밀번호를 설정했습니다")
	}
	return nil
}

func loadRuns(path string, log *slog.Logger) []runner.Run {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var runs []runner.Run
	if err := json.Unmarshal(b, &runs); err != nil {
		log.Warn("실행 기록을 읽을 수 없습니다", "err", err)
		return nil
	}
	// History 는 최신순으로 저장하므로 되돌려서 넣는다.
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	return runs
}

func saveRuns(path string, runs []runner.Run, log *slog.Logger) {
	b, err := json.Marshal(runs)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Warn("실행 기록 저장 실패", "err", err)
		return
	}
	os.Rename(tmp, path)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
