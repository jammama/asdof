package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fileServer(t *testing.T) (*server, *http.ServeMux) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-10-13 정리.docx"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	sv := &server{password: "pw", syncToken: "tok", filesDir: dir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/{name}", sv.handleFile)
	return sv, mux
}

func get(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestFile_비밀번호가_있으면_첨부로_내려준다(t *testing.T) {
	_, mux := fileServer(t)
	rec := get(mux, "/files/2026-10-13%20%EC%A0%95%EB%A6%AC.docx?pw=pw")
	if rec.Code != http.StatusOK || rec.Body.String() != "doc" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Fatal("첨부 헤더 없음")
	}
}

func TestFile_비밀번호가_없으면_401(t *testing.T) {
	_, mux := fileServer(t)
	if rec := get(mux, "/files/2026-10-13%20%EC%A0%95%EB%A6%AC.docx"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestFile_없는_파일이나_숨김_파일은_404(t *testing.T) {
	_, mux := fileServer(t)
	for _, target := range []string{"/files/none.docx?pw=pw", "/files/.env?pw=pw"} {
		if rec := get(mux, target); rec.Code != http.StatusNotFound {
			t.Fatalf("%s code=%d", target, rec.Code)
		}
	}
}
