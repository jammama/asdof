package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 대응 내용 정리 문서(FILES_DIR/*.docx) 목록과 같은 이름의 메일 제목·본문(*.mail.json, 복붙용).
// report.py upload 가 함께 올린다.

type reportMail struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Done    int    `json:"done"`
	Ask     int    `json:"ask"`
}

type reportFile struct {
	Name      string      `json:"name"`
	Size      int64       `json:"size"`
	UpdatedAt string      `json:"updated_at"`
	Mail      *reportMail `json:"mail,omitempty"`
}

func readReportMail(dir, docName string) *reportMail {
	b, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(docName, filepath.Ext(docName))+".mail.json"))
	if err != nil {
		return nil
	}
	var m reportMail
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

func listReports(dir string) []reportFile {
	ents, _ := os.ReadDir(dir)
	out := []reportFile{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".docx") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, reportFile{Name: e.Name(), Size: fi.Size(), UpdatedAt: fi.ModTime().UTC().Format(time.RFC3339), Mail: readReportMail(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name }) // 날짜로 시작 → 최신이 위
	return out
}

func (sv *server) handleReports(w http.ResponseWriter, r *http.Request) {
	if !sv.isAdmin(r) && !sv.isSync(r) {
		fail(w, http.StatusUnauthorized, "인증 필요")
		return
	}
	writeJSON(w, http.StatusOK, listReports(sv.filesDir))
}
