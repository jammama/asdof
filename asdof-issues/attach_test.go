package main

import "testing"

func att(id, name string) *attachIn {
	return &attachIn{Attachment: Attachment{ID: id, Name: name, Kind: "ppt"}}
}

func TestSyncAttachment_신규는_등록된다(t *testing.T) {
	st := newTestStore(t)
	if got := st.syncAttachment(att("edge-260601", "Node-B Edge개선 요청사항260601.pptx")); got != "new" {
		t.Fatalf("got %s", got)
	}
	if _, a := st.findAttachment("edge-260601"); a == nil || a.Name == "" || a.Refs == nil {
		t.Fatalf("등록 결과가 이상함: %+v", a)
	}
}

func TestSyncAttachment_같은_id는_갱신한다(t *testing.T) {
	st := newTestStore(t)
	st.syncAttachment(att("a", "이전.pptx"))
	in := att("a", "새이름.pptx")
	in.Where = "10/6 메일 첨부"
	if got := st.syncAttachment(in); got != "updated" {
		t.Fatalf("got %s", got)
	}
	if _, a := st.findAttachment("a"); a.Name != "새이름.pptx" || a.Where != "10/6 메일 첨부" || len(st.data.Attachments) != 1 {
		t.Fatalf("갱신 결과가 이상함: %+v", a)
	}
}

func TestSyncAttachment_remove_는_삭제하고_없는_id는_건너뛴다(t *testing.T) {
	st := newTestStore(t)
	st.syncAttachment(att("a", "a.pptx"))
	if got := st.syncAttachment(&attachIn{Attachment: Attachment{ID: "a"}, Remove: true}); got != "removed" {
		t.Fatalf("got %s", got)
	}
	if got := st.syncAttachment(&attachIn{Attachment: Attachment{ID: "a"}, Remove: true}); got != "skipped" {
		t.Fatalf("got %s", got)
	}
}

func TestAttachValidate_잘못된_입력을_거부한다(t *testing.T) {
	cases := map[string]*attachIn{
		"id 없음":     att("", "a.pptx"),
		"name 없음":   att("a", ""),
		"kind 오류":   {Attachment: Attachment{ID: "a", Name: "a", Kind: "exe"}},
		"ref 종류 오류": {Attachment: Attachment{ID: "a", Name: "a", Refs: []Ref{{Kind: "x", ID: "1"}}}},
	}
	for name, in := range cases {
		if in.validate() == nil {
			t.Errorf("%s: 거부되어야 함", name)
		}
	}
}

func TestSyncAttachment_분류를_보내지_않으면_직접_지정한_분류를_유지한다(t *testing.T) {
	st := newTestStore(t)
	st.syncAttachment(&attachIn{Attachment: Attachment{ID: "a", Name: "자료.zip", Category: "plan"}})

	st.syncAttachment(&attachIn{Attachment: Attachment{ID: "a", Name: "자료.zip"}})

	if _, a := st.findAttachment("a"); a.Category != "plan" {
		t.Fatalf("got %q", a.Category)
	}
}

func TestAttachValidate_알수없는_분류는_거부한다(t *testing.T) {
	in := &attachIn{Attachment: Attachment{ID: "a", Name: "x.pptx", Category: "misc"}}
	if err := in.validate(); err == nil {
		t.Fatal("거부되어야 함")
	}
}
