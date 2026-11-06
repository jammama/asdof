package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *store {
	st, err := newStore(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// progress 는 노션 '진행 중' 이슈를 수정필요로 올리는 동기화 항목
func progress(id, rev string) *syncItem {
	return &syncItem{Issue: Issue{ID: id, Revision: rev, NotionStatus: "진행 중", Stage: "fix", Target: "web", Priority: "mid"}}
}

func meta(id, rev, status string) *syncItem {
	return &syncItem{Issue: Issue{ID: id, Revision: rev, NotionStatus: status}, MetaOnly: true}
}

func TestSync_진행중_이슈는_수정필요로_등록된다(t *testing.T) {
	st := newTestStore(t)
	if got := st.syncIssue(progress("a", "r1")); got != "new" {
		t.Fatalf("got %s", got)
	}
	if st.find("a").Stage != "fix" {
		t.Fatalf("수정필요여야 함: %+v", st.find("a"))
	}
}

func TestSync_노션이_테스트중이_되면_수정완료로_옮긴다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(progress("a", "r1"))

	if got := st.syncIssue(meta("a", "r2", "테스트 중")); got != "done" {
		t.Fatalf("got %s", got)
	}
	if st.find("a").Stage != "done" {
		t.Fatalf("수정완료여야 함: %+v", st.find("a"))
	}
}

func TestSync_노션이_해결이_되면_수정완료로_옮긴다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(progress("a", "r1"))

	st.syncIssue(meta("a", "r2", "해결"))
	if st.find("a").Stage != "done" {
		t.Fatalf("수정완료여야 함: %+v", st.find("a"))
	}
}

func TestSync_노션이_재요청이_되면_수정필요에서_보관으로_뺀다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(progress("a", "r1"))

	if got := st.syncIssue(meta("a", "r2", "재요청")); got != "archived" {
		t.Fatalf("got %s", got)
	}
	if st.find("a").Stage != "" {
		t.Fatalf("보관이어야 함: %+v", st.find("a"))
	}
}

func TestSync_진행중_meta_갱신은_단계와_내용을_유지한다(t *testing.T) {
	st := newTestStore(t)
	in := progress("a", "r1")
	in.Summary = "요약"
	st.syncIssue(in)

	if got := st.syncIssue(meta("a", "r2", "진행 중")); got != "meta" {
		t.Fatalf("got %s", got)
	}
	it := st.find("a")
	if it.Stage != "fix" || it.Summary != "요약" || it.Revision != "r2" {
		t.Fatalf("유지돼야 함: %+v", it)
	}
}

func TestSync_보관된_이슈가_다시_진행중이_되면_수정필요로_올린다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(progress("a", "r1"))
	st.syncIssue(meta("a", "r2", "재요청"))

	if got := st.syncIssue(progress("a", "r3")); got != "updated" {
		t.Fatalf("got %s", got)
	}
	if st.find("a").Stage != "fix" {
		t.Fatalf("수정필요여야 함: %+v", st.find("a"))
	}
}

func TestSync_없는_이슈의_meta는_건너뛴다(t *testing.T) {
	st := newTestStore(t)
	if got := st.syncIssue(meta("a", "r1", "진행 중")); got != "skipped" {
		t.Fatalf("got %s", got)
	}
}

func TestValidate_stage가_없으면_거부한다(t *testing.T) {
	in := progress("a", "r1")
	in.Stage = ""
	if err := in.validate(); err == nil {
		t.Fatal("에러가 나야 함")
	}
}

func TestValidate_알수없는_target은_거부한다(t *testing.T) {
	in := progress("a", "r1")
	in.Target = "ios"
	if err := in.validate(); err == nil {
		t.Fatal("에러가 나야 함")
	}
}

func TestNewStore_개편전_데이터는_보관하고_옛_할일은_지운다(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	old := db{
		Issues: []*Issue{{ID: "a", Verdict: "fix"}},
		Mails:  []*Mail{{ID: "<m2>", ReceivedAt: "2026-10-02"}, {ID: "<m1>", ReceivedAt: "2026-10-01"}},
		Todos:  []*Todo{{ID: "t1", Text: "옛 할일"}},
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := newStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.data.Version != 2 || len(st.data.Todos) != 0 || st.find("a").Stage != "" {
		t.Fatalf("개편 반영 안 됨: %+v", st.data)
	}
	m1, m2 := st.findMail("<m1>"), st.findMail("<m2>")
	if !m1.Archived || !m2.Archived || m1.No != 1 || m2.No != 2 {
		t.Fatalf("메일 보관·번호 이상: %+v %+v", m1, m2)
	}
}
