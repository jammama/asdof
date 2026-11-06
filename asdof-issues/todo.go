package main

// 할일 — Claude 가 이슈·메일 판정에서 뽑은 짧은 작업 목록. 완료 체크는 사람이 한다.
// 각 할일은 refs 로 출처(이슈·메일 카드)를 가리켜 화면에서 바로 원 요청으로 이동할 수 있다.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Ref struct {
	Kind string `json:"kind"` // issue | mail
	ID   string `json:"id"`   // issue: 노션 page id 또는 노션 번호(no), mail: internetMessageId
}

type Todo struct {
	ID   string `json:"id"` // Claude 가 정하는 안정 키 (예: "168-photo-count")
	Text string `json:"text"`
	// Kind 는 할일 단위: check(내용확인) | fix(수정) | design(설계) | share(공유). 화면에서 색으로 구분한다
	Kind string `json:"kind"`
	Refs []Ref  `json:"refs"`
	// Actions 는 이 할일을 하려면 그대로 복사해 쓸 문구 (메일 초안·전달 문구·AI 프롬프트)
	Actions   []Action `json:"actions"`
	CreatedAt string   `json:"created_at"`
	Done      bool     `json:"done"`
	DoneAt    string   `json:"done_at"`
}

type todoIn struct {
	Todo
	Remove bool `json:"remove"` // true 면 삭제 (요청 취소 등으로 할일이 없어짐)
}

// todoKinds 는 허용하는 할일 단위
var todoKinds = map[string]bool{"check": true, "fix": true, "design": true, "share": true}

func (s *store) findTodo(id string) (int, *Todo) {
	for i, t := range s.data.Todos {
		if t.ID == id {
			return i, t
		}
	}
	return -1, nil
}

func (in *todoIn) validate() error {
	if strings.TrimSpace(in.ID) == "" {
		return fmt.Errorf("todo id 필수")
	}
	if in.Remove {
		return nil
	}
	if strings.TrimSpace(in.Text) == "" {
		return fmt.Errorf("%s: text 필수", in.ID)
	}
	if in.Kind != "" && !todoKinds[in.Kind] {
		return fmt.Errorf("%s: kind 는 check|fix|design|share", in.ID)
	}
	for _, r := range in.Refs {
		if r.Kind != "issue" && r.Kind != "mail" {
			return fmt.Errorf("%s: ref.kind 는 issue|mail", in.ID)
		}
	}
	for _, a := range in.Actions {
		if a.Kind != "prompt" && a.Kind != "person" {
			return fmt.Errorf("%s: action.kind 는 prompt|person", in.ID)
		}
	}
	return nil
}

// syncTodo 는 할일 하나를 반영한다. 완료 상태는 건드리지 않는다. 반환값: "new" | "updated" | "removed" | "skipped"
func (s *store) syncTodo(in *todoIn) string {
	i, t := s.findTodo(in.ID)
	if in.Remove {
		if t == nil {
			return "skipped"
		}
		s.data.Todos = append(s.data.Todos[:i], s.data.Todos[i+1:]...)
		return "removed"
	}
	result := "updated"
	if t == nil {
		t = &Todo{ID: clamp(in.ID, 100), CreatedAt: now()}
		s.data.Todos = append(s.data.Todos, t)
		result = "new"
	}
	t.Text = clamp(in.Text, 200)
	if in.Kind != "" { // 단위 없이 문구만 갱신하는 동기화는 기존 단위를 유지
		t.Kind = in.Kind
	}
	t.Refs = in.Refs
	if t.Refs == nil {
		t.Refs = []Ref{}
	}
	t.Actions = in.Actions
	if t.Actions == nil {
		t.Actions = []Action{}
	}
	return result
}

func (sv *server) handleTodoSync(w http.ResponseWriter, r *http.Request) {
	if !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "sync 토큰 필요")
		return
	}
	var body struct {
		Todos []*todoIn `json:"todos"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "JSON 파싱 실패: "+err.Error())
		return
	}
	for _, in := range body.Todos {
		if err := in.validate(); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	result := map[string][]string{}
	for _, in := range body.Todos {
		k := sv.st.syncTodo(in)
		result[k] = append(result[k], in.ID)
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (sv *server) handleTodoCheck(w http.ResponseWriter, r *http.Request) {
	if !sv.isAdmin(r) {
		fail(w, http.StatusUnauthorized, "인증 필요")
		return
	}
	var body struct {
		Done bool `json:"done"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "잘못된 요청")
		return
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	_, t := sv.st.findTodo(r.URL.Query().Get("id"))
	if t == nil {
		fail(w, http.StatusNotFound, "할일 없음")
		return
	}
	t.Done = body.Done
	t.DoneAt = ""
	if body.Done {
		t.DoneAt = now()
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}
