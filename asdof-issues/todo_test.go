package main

import "testing"

func todo(id, text string) *todoIn {
	return &todoIn{Todo: Todo{ID: id, Text: text, Refs: []Ref{{Kind: "issue", ID: "168"}}}}
}

func TestSyncTodo_신규는_미완료로_등록된다(t *testing.T) {
	st := newTestStore(t)
	if got := st.syncTodo(todo("t1", "사진 장수 수정")); got != "new" {
		t.Fatalf("got %s", got)
	}
	if _, td := st.findTodo("t1"); td.Done {
		t.Fatal("신규는 미완료여야 함")
	}
}

func TestSyncTodo_문구를_갱신해도_완료_상태는_유지된다(t *testing.T) {
	st := newTestStore(t)
	st.syncTodo(todo("t1", "사진 장수 수정"))
	_, td := st.findTodo("t1")
	td.Done = true

	st.syncTodo(todo("t1", "사진 2~4장으로 수정"))

	_, td = st.findTodo("t1")
	if !td.Done || td.Text != "사진 2~4장으로 수정" {
		t.Fatalf("완료 유지·문구 갱신이어야 함: %+v", td)
	}
}

func TestSyncTodo_remove면_삭제된다(t *testing.T) {
	st := newTestStore(t)
	st.syncTodo(todo("t1", "a"))
	if got := st.syncTodo(&todoIn{Todo: Todo{ID: "t1"}, Remove: true}); got != "removed" {
		t.Fatalf("got %s", got)
	}
	if _, td := st.findTodo("t1"); td != nil {
		t.Fatal("삭제되어야 함")
	}
}

func TestTodoValidate_잘못된_ref는_거부한다(t *testing.T) {
	in := todo("t1", "a")
	in.Refs = []Ref{{Kind: "notion", ID: "x"}}
	if err := in.validate(); err == nil {
		t.Fatal("거부되어야 함")
	}
}

func TestSyncTodo_보낼_문구를_저장한다(t *testing.T) {
	st := newTestStore(t)
	in := todo("t1", "회신")
	in.Actions = []Action{{Kind: "person", To: "황호성", Text: "본문"}}
	st.syncTodo(in)

	if _, td := st.findTodo("t1"); len(td.Actions) != 1 || td.Actions[0].Text != "본문" {
		t.Fatalf("문구가 저장되어야 함: %+v", td)
	}
}

func TestSyncTodo_단위를_저장하고_빈_단위면_유지한다(t *testing.T) {
	st := newTestStore(t)
	in := todo("t1", "문구 추가")
	in.Kind = "fix"
	st.syncTodo(in)

	st.syncTodo(todo("t1", "문구 추가 (갱신)"))

	if _, td := st.findTodo("t1"); td.Kind != "fix" {
		t.Fatalf("단위가 유지되어야 함: %+v", td)
	}
}

func TestTodoValidate_모르는_단위는_거부한다(t *testing.T) {
	in := todo("t1", "a")
	in.Kind = "etc"
	if err := in.validate(); err == nil {
		t.Fatal("거부되어야 함")
	}
}
