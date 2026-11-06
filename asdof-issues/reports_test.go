package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReports_최신_날짜순으로_docx만_메일본문과_함께_내려준다(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "2026-10-13 정리.docx"), []byte("doc"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-10-12 정리.docx"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dir, "2026-10-13 정리.mail.json"), []byte(`{"subject":"정리 송부","body":"안녕하세요","done":2,"ask":1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "메모.txt"), []byte("x"), 0o644)
	sv := &server{password: "pw", syncToken: "tok", filesDir: dir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/reports", sv.handleReports)

	req := httptest.NewRequest(http.MethodGet, "/api/reports", nil)
	req.Header.Set("X-Admin-Password", "pw")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Index(body, "2026-10-13") > strings.Index(body, "2026-10-12") || strings.Contains(body, "메모") || !strings.Contains(body, `"subject":"정리 송부"`) {
		t.Fatalf("code=%d body=%s", rec.Code, body)
	}
}

func TestReports_비밀번호가_없으면_401(t *testing.T) {
	sv := &server{password: "pw", syncToken: "tok", filesDir: t.TempDir()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/reports", sv.handleReports)
	if rec := get(mux, "/api/reports"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", rec.Code)
	}
}
