package main

// 메일 트리아지 — Outlook "trackbot" 폴더 메일을 Claude 가 MCP 로 읽고 요약만 올린다. 원문은 저장하지 않는다.
// 메일마다 메일#번호를 매긴다. 신규요청 메일은 '확인필요' 로 올라가고, 사람이 요청하면 Claude 가
// 노션 이슈로 나눠 등록한 뒤 /api/mail-register 로 등록한 노션 번호를 붙인다.
// 이미 올린 메일이 재전달된 것이면 dup_of 로 원 메일을 가리키고 화면에는 따로 띄우지 않는다.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 메일 판정 종류 — 화면 필터/배지와 1:1
var mailVerdicts = map[string]bool{
	"request": true, // 신규요청 — 개발·수정 요청이 담김 → 확인필요 (노션 등록 대상)
	"action": true, // 조치 필요 — 개발·수정·자료 작성 등 우리 쪽 작업이 생김
	"reply":  true, // 회신 필요 — 작업은 없고 답장만 필요
	"info":   true, // 참고 — 공유·감사·일정 안내 등
	"done":   true, // 처리됨 — 이미 회신했거나 노션 이슈로 처리 중
}

type Mail struct {
	ID          string   `json:"id"` // internetMessageId (폴더 복사·이동에도 유지)
	No          int      `json:"no"` // 메일#번호 — 서버가 등록 순서대로 매긴다
	Subject     string   `json:"subject"`
	From        string   `json:"from"`
	ReceivedAt  string   `json:"received_at"`
	WebLink     string   `json:"web_link"`    // Outlook 웹에서 원문 열기 (본인 계정만 열림)
	Attachments []string `json:"attachments"` // 첨부 파일명
	IssueNos    []string `json:"issue_nos"`   // 언급된 노션 이슈 번호 (예: "168")

	Verdict   string   `json:"verdict"`
	Priority  string   `json:"priority"`
	Summary   string   `json:"summary"`
	Analysis  string   `json:"analysis"`
	Actions   []Action `json:"actions"`
	TriagedAt string   `json:"triaged_at"`

	DupOf      string   `json:"dup_of"`     // 재전달된 같은 메일이면 원 메일 id (화면에 따로 띄우지 않음)
	Registered []string `json:"registered"` // 이 메일로 등록·재요청한 노션 이슈 번호
	Archived   bool     `json:"archived"`   // 규칙 개편 전 메일 (이력)

	Checked   bool    `json:"checked"`
	CheckedAt string  `json:"checked_at"`
	History   []Event `json:"history"`
}

func (s *store) findMail(id string) *Mail {
	for _, m := range s.data.Mails {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (s *store) findIssueByNo(no string) *Issue {
	for _, it := range s.data.Issues {
		if it.No == no {
			return it
		}
	}
	return nil
}

func (in *Mail) validate() error {
	if strings.TrimSpace(in.ID) == "" {
		return fmt.Errorf("mail id 필수")
	}
	if !mailVerdicts[in.Verdict] {
		return fmt.Errorf("%s: verdict 는 request|reply|info|action|done 중 하나", in.ID)
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

// syncMail 은 메일 요약 하나를 반영한다. 반환값: "new" | "updated"
// 등록한 노션 번호(registered)·보관 여부는 유지한다.
func (s *store) syncMail(in *Mail) string {
	m := s.findMail(in.ID)
	result := "updated"
	if m == nil {
		m = &Mail{ID: in.ID, No: s.nextMailNo(), Registered: []string{}}
		s.data.Mails = append(s.data.Mails, m)
		result = "new"
	}
	prevVerdict := m.Verdict
	m.Subject = clamp(in.Subject, 300)
	m.From = clamp(in.From, 200)
	m.ReceivedAt = clamp(in.ReceivedAt, 40)
	m.WebLink = clamp(in.WebLink, 1000)
	m.Attachments = orEmpty(in.Attachments)
	m.IssueNos = orEmpty(in.IssueNos)
	m.DupOf = clamp(in.DupOf, 300)
	m.Verdict = in.Verdict
	m.Priority = in.Priority
	m.Summary = clamp(in.Summary, 500)
	m.Analysis = clamp(in.Analysis, 8000)
	m.Actions = in.Actions
	if m.Actions == nil {
		m.Actions = []Action{}
	}
	m.TriagedAt = now()

	if result == "new" {
		m.History = append(m.History, Event{At: now(), Text: "신규 등록 — " + m.Verdict})
	} else if prevVerdict != m.Verdict {
		m.History = append(m.History, Event{At: now(), Text: fmt.Sprintf("판정 변경 %s → %s", prevVerdict, m.Verdict)})
	}
	return result
}

func (s *store) nextMailNo() int {
	n := 0
	for _, m := range s.data.Mails {
		if m.No > n {
			n = m.No
		}
	}
	return n + 1
}

// registerMail 은 메일#번호에 노션 이슈 번호를 붙인다 (중복 제외). 반환값: "registered" | "skipped"
func (s *store) registerMail(no int, nos []string) string {
	for _, m := range s.data.Mails {
		if m.No != no {
			continue
		}
		for _, n := range nos {
			if !contains(m.Registered, n) {
				m.Registered = append(m.Registered, n)
			}
		}
		m.History = append(m.History, Event{At: now(), Text: "노션 등록 #" + strings.Join(nos, ", #")})
		return "registered"
	}
	return "skipped"
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (sv *server) handleMailSync(w http.ResponseWriter, r *http.Request) {
	if !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "sync 토큰 필요")
		return
	}
	var body struct {
		Note  string  `json:"note"`
		Mails []*Mail `json:"mails"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "JSON 파싱 실패: "+err.Error())
		return
	}
	for _, in := range body.Mails {
		if err := in.validate(); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	result := map[string][]string{}
	for _, in := range body.Mails {
		k := sv.st.syncMail(in)
		result[k] = append(result[k], in.ID)
	}
	sv.st.data.LastMailSync = now()
	if body.Note != "" {
		sv.st.data.LastMailNote = clamp(body.Note, 1000)
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (sv *server) handleMailCheck(w http.ResponseWriter, r *http.Request) {
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
	m := sv.st.findMail(r.URL.Query().Get("id"))
	if m == nil {
		fail(w, http.StatusNotFound, "메일 없음")
		return
	}
	m.Checked = body.Checked
	if body.Checked {
		m.CheckedAt = now()
	} else {
		m.CheckedAt = ""
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleMailRegister 는 확인필요 메일을 노션 이슈로 등록한 결과를 붙인다.
// body: {"items":[{"no":12,"notion":["240","241"]}]}
func (sv *server) handleMailRegister(w http.ResponseWriter, r *http.Request) {
	if !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "sync 토큰 필요")
		return
	}
	var body struct {
		Items []struct {
			No     int      `json:"no"`
			Notion []string `json:"notion"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "JSON 파싱 실패: "+err.Error())
		return
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	result := map[string][]int{}
	for _, it := range body.Items {
		k := sv.st.registerMail(it.No, it.Notion)
		result[k] = append(result[k], it.No)
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
