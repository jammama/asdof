package main

import (
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

func item(id, rev, verdict string) *syncItem {
	return &syncItem{Issue: Issue{ID: id, Revision: rev, Verdict: verdict, Priority: "mid"}}
}

func TestSync_신규는_미체크로_등록된다(t *testing.T) {
	st := newTestStore(t)
	if got := st.syncIssue(item("a", "r1", "fix")); got != "new" {
		t.Fatalf("got %s", got)
	}
	if st.find("a").Checked {
		t.Fatal("신규는 미체크여야 함")
	}
}

func TestSync_체크한_이슈의_revision이_바뀌면_미체크로_되돌린다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(item("a", "r1", "fix"))
	st.find("a").Checked = true

	if got := st.syncIssue(item("a", "r2", "fix")); got != "reopened" {
		t.Fatalf("got %s", got)
	}
	it := st.find("a")
	if it.Checked || !it.Reopened || it.ReopenReason == "" {
		t.Fatalf("재확인 상태가 아님: %+v", it)
	}
}

func TestSync_revision이_같으면_체크가_유지된다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(item("a", "r1", "fix"))
	st.find("a").Checked = true

	if got := st.syncIssue(item("a", "r1", "resolved")); got != "updated" {
		t.Fatalf("got %s", got)
	}
	if !st.find("a").Checked {
		t.Fatal("체크가 유지돼야 함")
	}
}

func TestSync_reopen_플래그면_revision이_같아도_미체크로_되돌린다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(item("a", "r1", "resolved"))
	st.find("a").Checked = true

	in := item("a", "r1", "fix")
	in.Reopen, in.ReopenReason = true, "동일 증상 재발"
	st.syncIssue(in)

	it := st.find("a")
	if it.Checked || it.ReopenReason != "동일 증상 재발" {
		t.Fatalf("재발 반영 안 됨: %+v", it)
	}
}

func TestValidate_알수없는_verdict는_거부한다(t *testing.T) {
	if err := item("a", "r1", "weird").validate(); err == nil {
		t.Fatal("에러가 나야 함")
	}
}

func TestSync_meta_only로_변경일시가_바뀌면_체크를_해제하고_판정은_유지한다(t *testing.T) {
	st := newTestStore(t)
	st.syncIssue(item("a", "r1", "fix"))
	st.find("a").Checked = true

	in := &syncItem{Issue: Issue{ID: "a", NotionStatus: "진행 중", Revision: "r2"}, MetaOnly: true}
	if got := st.syncIssue(in); got != "reopened" {
		t.Fatalf("got %s", got)
	}
	it := st.find("a")
	if it.Checked || it.Verdict != "fix" || it.NotionStatus != "진행 중" {
		t.Fatalf("meta 반영 이상: %+v", it)
	}
}

func TestSync_노션_상태가_해결이면_해결됨으로_옮긴다(t *testing.T) {
	st := newTestStore(t)
	in := item("a", "r1", "testing")
	in.NotifyNeeded = true
	st.syncIssue(in)

	st.syncIssue(&syncItem{Issue: Issue{ID: "a", NotionStatus: "해결", Revision: "r2"}, MetaOnly: true})
	it := st.find("a")
	if it.Verdict != "resolved" || it.NotifyNeeded {
		t.Fatalf("해결됨 전환 안 됨: %+v", it)
	}
}

func TestSync_노션_상태가_해결이_아니면_resolved_판정은_테스트중으로_둔다(t *testing.T) {
	st := newTestStore(t)
	in := item("a", "r1", "resolved")
	in.NotionStatus = "테스트 중"
	st.syncIssue(in)

	if got := st.find("a").Verdict; got != "testing" {
		t.Fatalf("got %s", got)
	}
}
