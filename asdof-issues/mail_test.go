package main

import "testing"

func mail(id, verdict string) *Mail {
	return &Mail{ID: id, Subject: "제목 " + id, Verdict: verdict, Priority: "mid"}
}

func TestSyncMail_신규_메일에_순서대로_메일번호를_매긴다(t *testing.T) {
	st := newTestStore(t)
	st.syncMail(mail("<m1>", "request"))
	st.syncMail(mail("<m2>", "info"))

	if st.findMail("<m1>").No != 1 || st.findMail("<m2>").No != 2 {
		t.Fatalf("번호 이상: %d %d", st.findMail("<m1>").No, st.findMail("<m2>").No)
	}
}

func TestSyncMail_재판정해도_번호와_노션등록은_유지된다(t *testing.T) {
	st := newTestStore(t)
	st.syncMail(mail("<m1>", "request"))
	st.registerMail(1, []string{"240"})

	if got := st.syncMail(mail("<m1>", "reply")); got != "updated" {
		t.Fatalf("got %s", got)
	}
	m := st.findMail("<m1>")
	if m.No != 1 || len(m.Registered) != 1 || m.Verdict != "reply" {
		t.Fatalf("유지·갱신 이상: %+v", m)
	}
}

func TestRegisterMail_같은_노션번호는_한번만_붙인다(t *testing.T) {
	st := newTestStore(t)
	st.syncMail(mail("<m1>", "request"))

	st.registerMail(1, []string{"240", "241"})
	st.registerMail(1, []string{"241"})

	if got := st.findMail("<m1>").Registered; len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestRegisterMail_없는_메일번호는_건너뛴다(t *testing.T) {
	st := newTestStore(t)
	if got := st.registerMail(9, []string{"240"}); got != "skipped" {
		t.Fatalf("got %s", got)
	}
}

func TestMailValidate_잘못된_verdict는_거부한다(t *testing.T) {
	if err := mail("<m1>", "fix").validate(); err == nil {
		t.Fatal("거부되어야 함")
	}
}
