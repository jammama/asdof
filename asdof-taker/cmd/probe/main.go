// probe 는 jointips 의 7일 예약 창이 '화면(JS)에서만' 막는 것인지,
// 서버가 실제로 거부하는 것인지 확인하는 일회용 조사 도구다.
//
// 읽기 요청만 보낸다. write_update.php(실제 예약 신청)는 절대 호출하지 않는다.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"asdof-taker/internal/config"
	"asdof-taker/internal/jointips"
)

func main() {
	stateDir := flag.String("state", "/var/lib/asdof-taker", "설정 디렉터리")
	titlesOn := flag.String("titles", "", "이 날짜(YYYY-MM-DD)의 예약 제목만 덤프하고 종료")
	flag.Parse()

	vault, err := config.OpenVault(filepath.Join(*stateDir, "secret.key"))
	must(err)
	store, err := config.Open(filepath.Join(*stateDir, "config.json"), vault)
	must(err)
	cfg := store.Get()
	pw, err := store.Password()
	must(err)

	ctx := context.Background()
	c, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, 20*time.Second)
	must(err)
	must(c.Login(ctx, cfg.Site.Username, pw))
	fmt.Println("로그인 OK:", cfg.Site.Username)

	off, err := c.ClockOffset(ctx)
	if err == nil {
		fmt.Printf("사이트 시계: %+.1f분 (사이트 기준 오늘 = %s)\n",
			off.Minutes(), time.Now().Add(off).Format("2006-01-02 15:04:05"))
	}

	// ── 0) 특정 날짜의 예약 제목 원문 확인 ──
	if *titlesOn != "" {
		fmt.Println("\n=== 0. " + *titlesOn + " 예약 제목 원문 ===")
		rows, err := c.Query(ctx, "", strings.ReplaceAll(*titlesOn, "-", "."))
		must(err)
		seen := map[string]bool{}
		for _, r := range rows {
			for _, cell := range r.Cells {
				if cell.Available || cell.Title == "" || seen[cell.ShareID+cell.Title] {
					continue
				}
				seen[cell.ShareID+cell.Title] = true
				fmt.Printf("  %-22s %-6s %q\n", r.Name+" "+r.Place, cell.Time, cell.Title)
			}
		}
		return
	}

	// ── 1) 예약 페이지 HTML 에서 날짜 경계/검증 흔적 찾기 ──
	fmt.Println("\n=== 1. 예약 페이지의 날짜 관련 코드 ===")
	body := fetchWritePage(ctx, cfg, pw)
	dumpDateHints(body)

	fmt.Println("\n=== 1-b. 7일 검증 로직 원문 ===")
	dumpAround(body, "7일 이내로만", 1400)

	fmt.Println("\n=== 1-c. datepicker 설정 ===")
	dumpAround(body, ".cssDate').datepicker", 700)

	fmt.Println("\n=== 1-d. 폼 제출 검증 함수 (fwrite_submit) ===")
	dumpAround(body, "function fwrite_submit", 2600)

	fmt.Println("\n=== 1-e. 날짜 경계 상수가 또 어디 쓰이나 ===")
	for _, lit := range dateLiterals(body) {
		fmt.Println("  •", lit)
	}

	// ── 2) 여러 날짜로 조회해서 응답이 오는지 ──
	fmt.Println("\n=== 2. location_select_new 조회 (읽기) ===")
	base := time.Now().Add(off)
	for _, d := range []int{0, 6, 7, 8, 14, 30, 90, 365} {
		day := base.AddDate(0, 0, d)
		rows, err := c.Query(ctx, "3", day.Format("2006.01.02"))
		if err != nil {
			fmt.Printf("  +%-4d %s  오류: %v\n", d, day.Format("01-02"), err)
			continue
		}
		free := 0
		for _, r := range rows {
			for _, cell := range r.Cells {
				if cell.Available {
					free++
				}
			}
		}
		fmt.Printf("  +%-4d %s  회의실 %2d곳 · 빈칸 %3d\n", d, day.Format("01-02(Mon)"), len(rows), free)
	}

	// ── 3) 겹침확인 API 가 먼 날짜를 어떻게 다루는지 (읽기) ──
	fmt.Println("\n=== 3. dup_time 겹침확인 (읽기) — \"OK\"=예약불가 ===")
	for _, d := range []int{6, 7, 14, 30} {
		day := base.AddDate(0, 0, d)
		raw := rawAjax(ctx, cfg, pw, url.Values{
			"std": {"dup_time"}, "wr_3": {day.Format("2006.01.02")},
			"wr_4": {"09:00"}, "wr_5": {"09:00"}, "wr_8": {"46"}, "wr_id": {"0"},
		})
		fmt.Printf("  +%-4d %s  응답 %q\n", d, day.Format("01-02"), trunc(raw, 60))
	}
}

// fetchWritePage 는 예약 폼 페이지를 그대로 가져온다.
func fetchWritePage(ctx context.Context, cfg config.Config, pw string) string {
	c := newRaw(ctx, cfg, pw)
	u := fmt.Sprintf("%s/bbs/write.php?bo_table=%s&wr_22=%s",
		cfg.Site.BaseURL, jointips.BoardTable, url.QueryEscape(jointips.ApplyKind))
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", cfg.Site.UserAgent)
	resp, err := c.Do(req)
	must(err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b)
}

func rawAjax(ctx context.Context, cfg config.Config, pw string, form url.Values) string {
	c := newRaw(ctx, cfg, pw)
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Site.BaseURL+"/ajax_common.php",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", cfg.Site.UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.Do(req)
	if err != nil {
		return "오류: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return strings.TrimSpace(string(b))
}

var rawClients = map[string]*http.Client{}

func newRaw(ctx context.Context, cfg config.Config, pw string) *http.Client {
	if c, ok := rawClients["x"]; ok {
		return c
	}
	jc, err := jointips.New(cfg.Site.BaseURL, cfg.Site.UserAgent, 20*time.Second)
	must(err)
	must(jc.Login(ctx, cfg.Site.Username, pw))
	c := jc.HTTPClient()
	rawClients["x"] = c
	return c
}

// dumpDateHints 는 날짜 검증에 관련돼 보이는 줄만 추린다.
func dumpDateHints(body string) {
	pats := []*regexp.Regexp{
		regexp.MustCompile(`(?i).{0,120}(minDate|maxDate|startDate|endDate|datepicker|maxday|max_day|limit_?day).{0,160}`),
		regexp.MustCompile(`(?i).{0,100}(7일|일주일|이내|까지만|예약.{0,10}가능).{0,140}`),
		regexp.MustCompile(`.{0,80}\+\s*7.{0,120}`),
		regexp.MustCompile(`(?i).{0,80}(setDate|addDate|getDate\(\)\s*\+).{0,120}`),
	}
	seen := map[string]bool{}
	n := 0
	for _, p := range pats {
		for _, m := range p.FindAllString(body, -1) {
			t := strings.Join(strings.Fields(m), " ")
			if len(t) < 12 || seen[t] {
				continue
			}
			seen[t] = true
			n++
			if n > 40 {
				return
			}
			fmt.Println("  •", trunc(t, 230))
		}
	}
	if n == 0 {
		fmt.Println("  (날짜 관련 힌트를 못 찾음 — 페이지 길이", len(body), "바이트)")
	}
}

// dumpAround 는 특정 문구 주변 원문을 그대로 보여준다. 검증이 어디서 도는지 보려는 것이다.
func dumpAround(body, needle string, span int) {
	i := strings.Index(body, needle)
	if i < 0 {
		fmt.Println("  (못 찾음:", needle, ")")
		return
	}
	start := i - span/3
	if start < 0 {
		start = 0
	}
	end := i + span
	if end > len(body) {
		end = len(body)
	}
	for _, line := range strings.Split(body[start:end], "\n") {
		if t := strings.TrimSpace(line); t != "" {
			fmt.Println("  |", trunc(t, 190))
		}
	}
}

// dateLiterals 는 페이지에 박힌 YYYY-MM-DD 상수가 등장하는 문맥을 모은다.
// 서버가 경계를 어디어디에 심어 놨는지 보려는 것이다.
func dateLiterals(body string) []string {
	re := regexp.MustCompile(`.{0,110}"20\d\d-\d\d-\d\d".{0,80}`)
	seen, out := map[string]bool{}, []string{}
	for _, m := range re.FindAllString(body, -1) {
		t := strings.Join(strings.Fields(m), " ")
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, trunc(t, 190))
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "!!", err)
		os.Exit(1)
	}
}
