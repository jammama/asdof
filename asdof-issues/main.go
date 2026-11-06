// asdof-issues — PMS 노션 이슈 트리아지 대시보드의 파일 서버.
//
// 모든 일의 기준은 노션이다. Claude 는 30분마다
//   - Outlook "trackbot" 폴더 메일을 요약해 메일 탭에 올리고 (/api/mail-sync, 신규요청은 '확인필요')
//   - 노션 '진행 중' 이슈를 할일 탭 '수정필요' 로 올린다 (/api/sync, stage=fix)
//
// 노션 상태가 '테스트 중'·'해결' 이 되면 서버가 할일을 '수정완료' 로 옮겨 이력에 남긴다.
//
// DB 없이 JSON 파일 하나(DATA_FILE)에 저장한다. 임시파일 + rename 으로 원자적 교체.
// 고객사 이슈 내용이라 읽기도 비밀번호(X-Admin-Password)가 필요하다.
//
// 환경변수
//
//	LISTEN_ADDR     기본 127.0.0.1:8800  (nginx 리버스 프록시 앞단)
//	STATIC_DIR      기본 ./static
//	DATA_FILE       기본 ./data.json
//	FILES_DIR       기본 ./files  (일일 정리 문서, /files/{name} 로 내려줌 — static 과 달리 배포 때 지워지지 않음)
//	ADMIN_PASSWORD  화면 비밀번호 (필수)
//	SYNC_TOKEN      Claude 동기화용 토큰 (필수)
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- 모델 ----------

// 옛 판정 종류 (2026-10-19 규칙 개편 전) — 이력 카드의 배지 표시에만 쓴다
var verdicts = map[string]bool{
	"fix":          true, // 수정 필요 (내가/AI 가 코드 수정)
	"app":          true, // 앱 개발자에게 전달
	"insufficient": true, // 내용 미비 — 작성자에게 추가 정보 요청
	"invalid":      true, // 잘못된 이슈 (정상 동작/오해/운영 문제)
	"testing":      true, // 테스트중 — 코드상 완료(푸시됨), 노션 '해결' 전
	"review":       true, // 확인필요 — 파악 비용이 커서 판정 보류 (사람이 재확인 요청)
	"resolved":     true, // 해결됨 — 노션 상태가 '해결' 일 때만 (서버가 자동 지정)
}

// 할일 단계 (Issue.Stage)
const (
	stageFix  = "fix"  // 수정필요 — 노션 '진행 중'
	stageDone = "done" // 수정완료 — 노션 '테스트 중'·'해결' 로 바뀜 → 이력
	// "" 는 보관 — 규칙 개편 전 판정 이력이거나 진행이 멈춘(취소·재요청 등) 할일
)

// 노션 상태
const (
	progressStatus = "진행 중"
	testingStatus  = "테스트 중"
	resolvedStatus = "해결"
)

// isDoneStatus 는 개발이 끝난 노션 상태(테스트 중·해결)인지 본다.
func isDoneStatus(s string) bool { return s == resolvedStatus || s == testingStatus }

// 할일 대상 — 무엇을 누구에게 넘길지 (web·server = 에이전트 프롬프트, app = 앱 개발자 요약)
var targets = map[string]bool{"web": true, "server": true, "app": true, "mixed": true}

// Action 은 내가 직접 보내거나 붙여넣을 문구 하나 — 화면에서 클릭으로 복사한다.
type Action struct {
	Kind  string `json:"kind"`  // prompt(AI 에게) | person(사람에게)
	To    string `json:"to"`    // 대상 (예: "Claude Code(pms-back)", "앱 개발자", "hoseng hwong")
	Label string `json:"label"` // 버튼 옆 한 줄 설명
	Text  string `json:"text"`  // 복사될 본문
}

type Event struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

type Issue struct {
	// 노션 원문 메타 (Claude 가 채움)
	ID             string `json:"id"` // 노션 page id
	No             string `json:"no"` // 노션 ID 속성 (예: 160)
	Title          string `json:"title"`
	URL            string `json:"url"`
	Author         string `json:"author"`
	CreatedAt      string `json:"created_at"`
	NotionStatus   string `json:"notion_status"`
	NotionEdited   string `json:"notion_edited"`   // 노션 변경 일시
	NotionPriority string `json:"notion_priority"` // 노션 우선순위 (높음·보통·낮음)
	Due            string `json:"due"`             // 노션 마감일 (YYYY-MM-DD)
	Revision       string `json:"revision"`        // 변경 기준: "<변경 일시>|c<댓글 수>"

	// 할일 (Claude 가 채움)
	Stage   string `json:"stage"`    // fix(수정필요) | done(수정완료) | ""(보관)
	StageAt string `json:"stage_at"` // 단계가 바뀐 시각
	Target  string `json:"target"`   // web | server | app | mixed

	// 옛 판정 (규칙 개편 전 이력 표시용)
	Verdict  string   `json:"verdict"`
	Priority string   `json:"priority"` // high | mid | low
	Summary  string   `json:"summary"`  // 한 줄 요약
	Analysis string   `json:"analysis"` // 판단 근거
	Evidence []string `json:"evidence"` // 코드 위치·확인한 사실
	Actions  []Action `json:"actions"`
	// NotifyNeeded: 코드는 완료됐는데 노션에 '수정 완료' 류 안내 댓글이 없음 → 요청자에게 전달 필요
	NotifyNeeded bool   `json:"notify_needed"`
	TriagedAt    string `json:"triaged_at"`

	// 옛 확인 상태 (규칙 개편 전)
	Checked         bool    `json:"checked"`
	CheckedAt       string  `json:"checked_at"`
	CheckedRevision string  `json:"checked_revision"`
	Reopened        bool    `json:"reopened"`
	ReopenReason    string  `json:"reopen_reason"`
	History         []Event `json:"history"`
}

type db struct {
	// Version 2 = 노션 기준 규칙(2026-10-19). 1 이하 데이터는 로드 시 migrateV2 로 보관 처리한다
	Version      int           `json:"version"`
	LastSync     string        `json:"last_sync"`
	LastNote     string        `json:"last_note"`
	Issues       []*Issue      `json:"issues"`
	LastMailSync string        `json:"last_mail_sync"`
	LastMailNote string        `json:"last_mail_note"`
	Mails        []*Mail       `json:"mails"`
	Todos        []*Todo       `json:"todos"`
	Attachments  []*Attachment `json:"attachments"`
}

// ---------- 저장소 ----------

type store struct {
	mu   sync.RWMutex
	path string
	data db
}

func newStore(path string) (*store, error) {
	s := &store{path: path, data: db{Issues: []*Issue{}, Mails: []*Mail{}, Todos: []*Todo{}, Attachments: []*Attachment{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.flush()
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("%s 파싱 실패: %w", path, err)
		}
	}
	if s.data.Issues == nil {
		s.data.Issues = []*Issue{}
	}
	if s.data.Mails == nil {
		s.data.Mails = []*Mail{}
	}
	if s.data.Todos == nil {
		s.data.Todos = []*Todo{}
	}
	if s.data.Attachments == nil {
		s.data.Attachments = []*Attachment{}
	}
	if s.data.Version < 2 {
		s.migrateV2()
		if err := s.flush(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// migrateV2 는 규칙 개편 전 데이터를 보관한다: 이슈 판정 → 할일 이력(보관), 메일 → 메일 이력, 옛 할일 목록 → 삭제.
// 메일에는 받은 순서대로 메일#번호를 매긴다.
func (s *store) migrateV2() {
	for _, it := range s.data.Issues {
		it.Stage = ""
	}
	sort.SliceStable(s.data.Mails, func(i, j int) bool { return s.data.Mails[i].ReceivedAt < s.data.Mails[j].ReceivedAt })
	for i, m := range s.data.Mails {
		m.Archived = true
		if m.No == 0 {
			m.No = i + 1
		}
	}
	s.data.Todos = []*Todo{}
	s.data.Version = 2
}

// flush 는 호출자가 쓰기 락을 잡고 있어야 한다.
func (s *store) flush() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) find(id string) *Issue {
	for _, it := range s.data.Issues {
		if it.ID == id {
			return it
		}
	}
	return nil
}

func now() string { return time.Now().Format(time.RFC3339) }

func addEvent(it *Issue, text string) {
	it.History = append(it.History, Event{At: now(), Text: text})
	if len(it.History) > 30 {
		it.History = it.History[len(it.History)-30:]
	}
}

// syncIssue 는 Claude 가 보낸 할일(또는 meta 갱신) 하나를 반영한다.
// 반환값: "new" | "updated" | "meta" | "done" | "archived" | "skipped"
//
// 단계 전환은 노션 상태로 정한다:
//   - 수정필요 중에 노션이 '테스트 중'·'해결' 이 되면 → 수정완료(이력)
//   - 수정필요 중에 노션이 '진행 중' 이 아닌 다른 상태(재요청·요청 취소 등)가 되면 → 보관(이력)
func (s *store) syncIssue(in *syncItem) string {
	it := s.find(in.ID)
	if it == nil {
		if in.MetaOnly {
			return "skipped"
		}
		it = &Issue{ID: in.ID}
		s.data.Issues = append(s.data.Issues, it)
		in.applyMeta(it)
		in.applyTodo(it)
		addEvent(it, "수정필요 등록")
		return "new"
	}
	prevStatus, prevStage := it.NotionStatus, it.Stage
	in.applyMeta(it)
	if prevStatus != it.NotionStatus {
		addEvent(it, fmt.Sprintf("노션 상태 %s → %s", prevStatus, it.NotionStatus))
	}
	result := "meta"
	if !in.MetaOnly {
		in.applyTodo(it)
		result = "updated"
	}
	if it.Stage == stageFix && it.NotionStatus != progressStatus {
		if isDoneStatus(it.NotionStatus) {
			it.setStage(stageDone)
			addEvent(it, "수정완료 — 노션 "+it.NotionStatus)
			return "done"
		}
		it.setStage("")
		addEvent(it, "보관 — 노션 "+it.NotionStatus)
		return "archived"
	}
	if prevStage != stageFix && it.Stage == stageFix {
		addEvent(it, "수정필요 등록")
	}
	return result
}

func (it *Issue) setStage(st string) {
	it.Stage = st
	it.StageAt = now()
}

// ---------- 동기화 입력 ----------

type syncItem struct {
	Issue
	// MetaOnly 는 할일 내용 없이 노션 메타(상태·제목·변경 일시·revision)만 갱신한다.
	MetaOnly bool `json:"meta_only"`
}

// applyMeta 는 노션 원문 메타를 덮어쓴다.
func (in *syncItem) applyMeta(it *Issue) {
	set := func(dst *string, v string, n int) {
		if v != "" {
			*dst = clamp(v, n)
		}
	}
	set(&it.No, in.No, 20)
	set(&it.Title, in.Title, 300)
	set(&it.URL, in.URL, 500)
	set(&it.Author, in.Author, 100)
	set(&it.CreatedAt, in.CreatedAt, 40)
	set(&it.NotionStatus, in.NotionStatus, 50)
	set(&it.NotionEdited, in.NotionEdited, 40)
	set(&it.NotionPriority, in.NotionPriority, 20)
	set(&it.Due, in.Due, 40)
	set(&it.Revision, in.Revision, 200)
}

// applyTodo 는 Claude 가 정리한 할일 내용과 단계를 덮어쓴다.
func (in *syncItem) applyTodo(it *Issue) {
	if in.Stage != it.Stage {
		it.setStage(in.Stage)
	}
	it.Target = in.Target
	it.Priority = in.Priority
	it.Summary = clamp(in.Summary, 500)
	it.Analysis = clamp(in.Analysis, 8000)
	it.Evidence = in.Evidence
	if it.Evidence == nil {
		it.Evidence = []string{}
	}
	it.Actions = in.Actions
	if it.Actions == nil {
		it.Actions = []Action{}
	}
	it.TriagedAt = now()
}

func clamp(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}

func (in *syncItem) validate() error {
	if strings.TrimSpace(in.ID) == "" {
		return errors.New("id 필수")
	}
	if in.MetaOnly {
		return nil
	}
	if in.Stage != stageFix && in.Stage != stageDone {
		return fmt.Errorf("%s: stage 는 fix|done", in.ID)
	}
	if in.Target != "" && !targets[in.Target] {
		return fmt.Errorf("%s: target 는 web|server|app|mixed", in.ID)
	}
	switch in.Priority {
	case "high", "mid", "low":
	case "":
		in.Priority = "mid"
	default:
		return fmt.Errorf("%s: priority 는 high|mid|low", in.ID)
	}
	for _, a := range in.Actions {
		if a.Kind != "prompt" && a.Kind != "person" {
			return fmt.Errorf("%s: action.kind 는 prompt|person", in.ID)
		}
	}
	return nil
}

// ---------- HTTP ----------

type server struct {
	st        *store
	password  string
	syncToken string
	filesDir  string
}

func eq(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (sv *server) isAdmin(r *http.Request) bool {
	return eq(r.Header.Get("X-Admin-Password"), sv.password)
}

func (sv *server) isSync(r *http.Request) bool {
	return eq(r.Header.Get("X-Sync-Token"), sv.syncToken)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (sv *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !eq(body.Password, sv.password) {
		fail(w, http.StatusUnauthorized, "비밀번호가 틀렸습니다")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (sv *server) handleList(w http.ResponseWriter, r *http.Request) {
	if !sv.isAdmin(r) && !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "인증 필요")
		return
	}
	sv.st.mu.RLock()
	defer sv.st.mu.RUnlock()
	writeJSON(w, http.StatusOK, sv.st.data)
}

func (sv *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	if !sv.isAdmin(r) {
		fail(w, http.StatusUnauthorized, "인증 필요")
		return
	}
	var body struct {
		Checked bool `json:"checked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "잘못된 요청")
		return
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	it := sv.st.find(r.PathValue("id"))
	if it == nil {
		fail(w, http.StatusNotFound, "이슈 없음")
		return
	}
	it.Checked = body.Checked
	if body.Checked {
		it.CheckedAt = now()
		it.CheckedRevision = it.Revision
		it.Reopened = false
		it.ReopenReason = ""
		addEvent(it, "확인 체크")
	} else {
		it.CheckedAt = ""
		addEvent(it, "체크 해제(수동)")
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (sv *server) handleSync(w http.ResponseWriter, r *http.Request) {
	if !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "sync 토큰 필요")
		return
	}
	var body struct {
		Note   string      `json:"note"`
		Issues []*syncItem `json:"issues"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "JSON 파싱 실패: "+err.Error())
		return
	}
	for _, in := range body.Issues {
		if err := in.validate(); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	result := map[string][]string{}
	for _, in := range body.Issues {
		k := sv.st.syncIssue(in)
		result[k] = append(result[k], in.ID)
	}
	sv.st.data.LastSync = now()
	sv.st.data.LastNote = clamp(body.Note, 1000)
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleExport 는 현재 상태를 마크다운 한 장으로 내려준다 (보관/공유용).
func (sv *server) handleExport(w http.ResponseWriter, r *http.Request) {
	if !sv.isAdmin(r) && !sv.isSync(r) && !eq(r.URL.Query().Get("pw"), sv.password) {
		fail(w, http.StatusUnauthorized, "인증 필요")
		return
	}
	sv.st.mu.RLock()
	defer sv.st.mu.RUnlock()
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(renderMarkdown(sv.st.data)))
}

var verdictLabel = map[string]string{
	"fix": "수정 필요", "app": "앱 개발자 전달", "insufficient": "내용 미비", "invalid": "잘못된 이슈",
	"testing": "테스트중", "review": "확인필요", "resolved": "해결됨",
}

func renderMarkdown(d db) string {
	items := append([]*Issue(nil), d.Issues...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Checked != items[j].Checked {
			return !items[i].Checked
		}
		return items[i].CreatedAt > items[j].CreatedAt
	})
	var b strings.Builder
	fmt.Fprintf(&b, "# PMS 노션 이슈 트리아지\n\n마지막 동기화: %s\n\n", d.LastSync)
	for _, it := range items {
		mark := "[ ]"
		if it.Checked {
			mark = "[x]"
		}
		notify := ""
		if it.NotifyNeeded {
			notify = " [전달 필요]"
		}
		fmt.Fprintf(&b, "## %s #%s %s — %s%s\n\n", mark, it.No, verdictLabel[it.Verdict], it.Title, notify)
		if it.Reopened {
			fmt.Fprintf(&b, "> ⚠️ 다시 확인: %s\n\n", it.ReopenReason)
		}
		fmt.Fprintf(&b, "- 노션: %s (%s, %s)\n- 요약: %s\n\n%s\n\n", it.URL, it.Author, it.CreatedAt, it.Summary, it.Analysis)
		for _, e := range it.Evidence {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		for _, a := range it.Actions {
			fmt.Fprintf(&b, "\n**%s → %s** (%s)\n\n```\n%s\n```\n", a.Kind, a.To, a.Label, a.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func main() {
	addr := env("LISTEN_ADDR", "127.0.0.1:8800")
	static := env("STATIC_DIR", "./static")
	sv := &server{password: os.Getenv("ADMIN_PASSWORD"), syncToken: os.Getenv("SYNC_TOKEN"), filesDir: env("FILES_DIR", "./files")}
	if sv.password == "" || sv.syncToken == "" {
		log.Fatal("ADMIN_PASSWORD, SYNC_TOKEN 환경변수 필수")
	}
	st, err := newStore(env("DATA_FILE", "./data.json"))
	if err != nil {
		log.Fatal(err)
	}
	sv.st = st

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		fmt.Fprintf(w, "ok %d", len(st.data.Issues))
	})
	mux.HandleFunc("POST /api/auth", sv.handleAuth)
	mux.HandleFunc("GET /api/issues", sv.handleList)
	mux.HandleFunc("PUT /api/issues/{id}/check", sv.handleCheck)
	mux.HandleFunc("POST /api/sync", sv.handleSync)
	mux.HandleFunc("GET /api/export.md", sv.handleExport)
	mux.HandleFunc("POST /api/mail-sync", sv.handleMailSync)
	mux.HandleFunc("PUT /api/mails/check", sv.handleMailCheck)
	mux.HandleFunc("POST /api/mail-register", sv.handleMailRegister)
	mux.HandleFunc("POST /api/todo-sync", sv.handleTodoSync)
	mux.HandleFunc("POST /api/attachment-sync", sv.handleAttachSync)
	mux.HandleFunc("PUT /api/todos/check", sv.handleTodoCheck) // ?id= // ?id=<internetMessageId> (꺾쇠·@ 포함이라 쿼리로)
	mux.HandleFunc("GET /files/{name}", sv.handleFile)
	mux.HandleFunc("GET /api/reports", sv.handleReports)
	mux.Handle("GET /", http.FileServer(http.Dir(filepath.Clean(static))))

	log.Printf("asdof-issues listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
