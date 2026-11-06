package main

// 첨부파일 리스트 — 이슈 본문이 '출처'로 인용한 요청 문서(pptx·xlsx 등)와 직접 등록한 자료.
// 메일 첨부는 서버에 이미 있으므로(Mail.Attachments) 화면에서 이름으로 합쳐 보여 준다.
// 원본이 어디 있는지(메일·드라이브·로컬)와 인용한 이슈를 함께 관리한다.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Attachment struct {
	ID   string `json:"id"`   // 안정 키 (예: "edge-260601")
	Name string `json:"name"` // 표시 파일명
	Kind string `json:"kind"` // ppt | xlsx | doc | pdf | zip | other
	// Category 는 첨부파일 리스트 탭 분류를 직접 지정할 때만 쓴다: request(요청사항) | plan(기획검토) | summary(내용정리) | etc(기타).
	// 비어 있으면 화면이 파일명 규칙으로 분류한다
	Category string `json:"category"`
	Refs []Ref  `json:"refs"` // 인용한 이슈·메일
	// Where 는 원본을 찾을 곳 한 줄 (예: "10/6 메일 첨부", "구글드라이브 링크"). 비어 있으면 '위치 미확인'
	Where     string `json:"where"`
	Link      string `json:"link"` // 원본 URL (없으면 비움)
	Note      string `json:"note"`
	UpdatedAt string `json:"updated_at"`
}

type attachIn struct {
	Attachment
	Remove bool `json:"remove"`
}

var attachKinds = map[string]bool{"ppt": true, "xlsx": true, "doc": true, "pdf": true, "zip": true, "other": true}

var attachCategories = map[string]bool{"request": true, "plan": true, "summary": true, "etc": true}

func (s *store) findAttachment(id string) (int, *Attachment) {
	for i, a := range s.data.Attachments {
		if a.ID == id {
			return i, a
		}
	}
	return -1, nil
}

func (in *attachIn) validate() error {
	if strings.TrimSpace(in.ID) == "" {
		return fmt.Errorf("attachment id 필수")
	}
	if in.Remove {
		return nil
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%s: name 필수", in.ID)
	}
	if in.Kind != "" && !attachKinds[in.Kind] {
		return fmt.Errorf("%s: kind 는 ppt|xlsx|doc|pdf|zip|other", in.ID)
	}
	if in.Category != "" && !attachCategories[in.Category] {
		return fmt.Errorf("%s: category 는 request|plan|summary|etc", in.ID)
	}
	for _, r := range in.Refs {
		if r.Kind != "issue" && r.Kind != "mail" {
			return fmt.Errorf("%s: ref.kind 는 issue|mail", in.ID)
		}
	}
	return nil
}

// syncAttachment 는 첨부 하나를 반영한다. 반환값: "new" | "updated" | "removed" | "skipped"
func (s *store) syncAttachment(in *attachIn) string {
	i, a := s.findAttachment(in.ID)
	if in.Remove {
		if a == nil {
			return "skipped"
		}
		s.data.Attachments = append(s.data.Attachments[:i], s.data.Attachments[i+1:]...)
		return "removed"
	}
	result := "updated"
	if a == nil {
		a = &Attachment{ID: clamp(in.ID, 100)}
		s.data.Attachments = append(s.data.Attachments, a)
		result = "new"
	}
	a.Name = clamp(in.Name, 200)
	a.Kind = in.Kind
	if a.Kind == "" {
		a.Kind = "other"
	}
	if in.Category != "" { // 자동 집계(attach-sync)는 category 를 보내지 않으므로 직접 지정한 분류를 지우지 않는다
		a.Category = in.Category
	}
	a.Refs = in.Refs
	if a.Refs == nil {
		a.Refs = []Ref{}
	}
	a.Where = clamp(in.Where, 200)
	a.Link = clamp(in.Link, 500)
	a.Note = clamp(in.Note, 500)
	a.UpdatedAt = now()
	return result
}

func (sv *server) handleAttachSync(w http.ResponseWriter, r *http.Request) {
	if !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "sync 토큰 필요")
		return
	}
	var body struct {
		Attachments []*attachIn `json:"attachments"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "JSON 파싱 실패: "+err.Error())
		return
	}
	for _, in := range body.Attachments {
		if err := in.validate(); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	sv.st.mu.Lock()
	defer sv.st.mu.Unlock()
	result := map[string][]string{}
	for _, in := range body.Attachments {
		k := sv.st.syncAttachment(in)
		result[k] = append(result[k], in.ID)
	}
	if err := sv.st.flush(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
