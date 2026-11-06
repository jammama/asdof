package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// handleFile 은 FILES_DIR 의 정리 문서(일일 docx 등)를 내려준다.
// 고객사 이슈 내용이라 export.md 와 같이 비밀번호(헤더 또는 ?pw=)가 필요하다.
// 하위 경로·숨김 파일은 막고, 파일명 그대로(한글 포함) 첨부로 내려준다.
func (sv *server) handleFile(w http.ResponseWriter, r *http.Request) {
	if !sv.isAdmin(r) && !sv.isSync(r) && !eq(r.URL.Query().Get("pw"), sv.password) {
		fail(w, http.StatusUnauthorized, "인증 필요")
		return
	}
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		fail(w, http.StatusNotFound, "없는 파일")
		return
	}
	path := filepath.Join(sv.filesDir, name)
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		fail(w, http.StatusNotFound, "없는 파일")
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeFile(w, r, path)
}
