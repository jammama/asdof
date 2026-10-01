// probe 는 새 jointips API 가 우리 클라이언트로 제대로 도는지 확인하는 조사 도구다.
//
// 읽기 요청만 보낸다. -submit 을 주지 않으면 예약 신청은 절대 하지 않는다.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"io"
	"log/slog"

	"asdof-taker/internal/config"
	"asdof-taker/internal/jointips"
	"asdof-taker/internal/runner"
)

func main() {
	stateDir := flag.String("state", "/var/lib/asdof-taker", "설정 디렉터리")
	date := flag.String("date", "", "슬롯을 볼 날짜 (YYYY-MM-DD, 기본 오늘+6)")
	baseOv := flag.String("base", "", "사이트 주소 덮어쓰기")
	dry := flag.Bool("dryrun", false, "실제 Runner 로 드라이런을 돌려 로그를 찍는다")
	flag.Parse()

	vault, err := config.OpenVault(filepath.Join(*stateDir, "secret.key"))
	must(err)
	store, err := config.Open(filepath.Join(*stateDir, "config.json"), vault)
	must(err)
	cfg := store.Get()
	ready := cfg.Site.ReadyAccounts()
	if len(ready) == 0 {
		must(fmt.Errorf("쓸 수 있는 계정이 없습니다"))
	}
	acc := ready[0]
	pw, err := store.AccountPassword(acc.ID)
	must(err)

	if *dry {
		runDry(store)
		return
	}

	base := cfg.Site.BaseURL
	if *baseOv != "" {
		base = *baseOv
	}
	fmt.Printf("base: %s  계정 %d개 (이번 조사: %s)\n", base, len(ready), acc.Name())
	ctx := context.Background()
	c, err := jointips.New(base, cfg.Site.UserAgent, 20*time.Second)
	must(err)

	fmt.Print("\n로그인… ")
	if err := c.Login(ctx, acc.Username, pw); err != nil {
		fmt.Println("실패:", err)
		os.Exit(1)
	}
	m := c.Member()
	fmt.Printf("성공 — %s <%s>\n", m.UserNm, m.Email)

	if off, err := c.ClockOffset(ctx); err == nil {
		fmt.Printf("사이트 시계 오차: %s\n", off)
	}

	days, err := c.BookableDays(ctx)
	fmt.Println("\n운영 요일:", days, err)

	purposes, err := c.Purposes(ctx)
	must(err)
	fmt.Print("이용목적: ")
	for _, p := range purposes {
		fmt.Printf("%s=%s ", p.DetailCd, p.CodeNm)
	}
	fmt.Println()

	d := *date
	if d == "" {
		d = time.Now().AddDate(0, 0, 6).Format("2006-01-02")
	}
	fmt.Println("\n── 현황", d, "──")
	rows, err := c.Timetable(ctx, "", "", d)
	must(err)
	fmt.Println("공간", len(rows), "곳")
	for _, r := range rows {
		free := 0
		for _, cell := range r.Cells {
			if cell.Available {
				free++
			}
		}
		fmt.Printf("  %-34s %-10s 빈칸 %2d/%2d  bookable=%v  라벨=%s\n",
			r.Label(), r.SpaceCd, free, len(r.Cells), r.Bookable,
			jointips.RoomLabel(r.BldgNm, r.FloorNo, r.SpaceNm))
	}

	if len(rows) > 0 {
		ss, err := c.Slots(ctx, rows[0].SpaceCd, d)
		must(err)
		b, _ := json.Marshal(map[string]any{
			"dailyLimitHour": ss.DailyLimitHour, "dailyLimitCount": ss.DailyLimitCount,
			"singleLimitHour": ss.SingleLimitHour, "myUsedHour": ss.MyUsedHour,
			"myDailyCount": ss.MyDailyCount, "dayBookable": ss.DayBookable,
		})
		fmt.Println("\n한도(로그인 기준):", string(b))
	}

	var rawMine []map[string]any
	if err := c.Raw(ctx, "/api/cms/member/reservation/my?category=COMMON&siteId=4", &rawMine); err == nil && len(rawMine) > 0 {
		b, _ := json.MarshalIndent(rawMine[0], "", " ")
		fmt.Println("\n── 내 예약 원본 1건 ──\n" + string(b))
	}
	mine, err := c.MyReservations(ctx, "", "")
	must(err)
	fmt.Println("\n── 내 예약", len(mine), "건 ──")
	for i, r := range mine {
		if i >= 12 {
			break
		}
		fmt.Printf("  %s %s %s~%s %-22s %s %s\n", r.ReserveDate, r.BldgNm, r.StartTime, r.EndTime,
			r.SpaceNm, r.Status, r.Title)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "오류:", err)
		os.Exit(1)
	}
}

// runDry 는 실제 Runner 를 드라이런으로 돌린다. 신청 POST 는 나가지 않는다.
// runs.json 을 건드리므로 반드시 상태 디렉터리 '사본'을 대상으로 쓴다.
func runDry(store *config.Store) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rn := runner.New(store, log)
	if _, err := rn.Start(context.Background(), runner.Options{Mode: "manual", DryRun: true}); err != nil {
		must(err)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if rn.Current() == nil {
			if h := rn.History(1); len(h) == 1 {
				r := h[0]
				fmt.Printf("결과: %s — %s\n\n", r.Status, r.Message)
				for _, l := range r.Logs {
					fmt.Printf("  [%-7s] %s\n", l.Level, l.Msg)
				}
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("드라이런이 끝나지 않았습니다")
}
