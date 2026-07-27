// asdof-saves — 여러 앱/게임의 세이브 파일을 이름으로 저장하는 범용 스토리지 서비스.
//
// 네임스페이스(앱/게임 구분)로 격리. 다른 오리진에서도 쓰도록 CORS 지원.
//   GET    /health                    상태 확인 (인증 없음)
//   GET    /v1/{ns}/saves             목록 (JSON)
//   POST   /v1/{ns}/saves/{name}      저장 (body=바이트, X-Origin-Name=원본파일명(URI인코딩))
//   GET    /v1/{ns}/saves/{name}      다운로드 (응답 헤더 X-Origin-Name)
//   DELETE /v1/{ns}/saves/{name}      삭제
//
// 인증: X-Token 헤더 == SAVE_TOKEN (미설정 시 개방 = 개발용).
// 환경변수: SAVE_TOKEN · SAVE_DIR(기본 ./data) · PORT(기본 8760) · ALLOW_ORIGIN(기본 *)
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxBody = 32 << 20 // 32MB (다양한 게임 세이브/상태저장 여유)

var (
	dataDir     = env("SAVE_DIR", "./data")
	token       = os.Getenv("SAVE_TOKEN")
	addr        = ":" + env("PORT", "8760")
	allowOrigin = env("ALLOW_ORIGIN", "*")
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// 경로 세그먼트(네임스페이스/파일명) 안전성 — 경로 분리자/상위경로/숨김 차단.
func safeSeg(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 200 {
		return "", false
	}
	if strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") || strings.HasPrefix(s, ".") {
		return "", false
	}
	return s, true
}

func setCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", allowOrigin)
	h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "X-Token, X-Origin-Name, Content-Type")
	h.Set("Access-Control-Expose-Headers", "X-Origin-Name")
	h.Set("Access-Control-Max-Age", "86400")
	if allowOrigin != "*" {
		h.Set("Vary", "Origin")
	}
}

func authed(r *http.Request) bool {
	if token == "" {
		return true
	}
	return r.Header.Get("X-Token") == token
}

type entry struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Size   int64  `json:"size"`
	Mtime  string `json:"mtime"`
}

func main() {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("데이터 디렉터리 생성 실패: %v", err)
	}
	if token == "" {
		log.Println("⚠ SAVE_TOKEN 미설정 — 인증 없이 동작합니다 (개발용).")
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		_, _ = w.Write([]byte("ok"))
	})
	http.HandleFunc("/", handle)
	log.Printf("asdof-saves 시작: %s (data=%s, origin=%s)", addr, dataDir, allowOrigin)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handle(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions { // CORS preflight
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 경로: /v1/{ns}/saves  또는  /v1/{ns}/saves/{name}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[2] != "saves" {
		http.NotFound(w, r)
		return
	}
	ns, ok := safeSeg(parts[1])
	if !ok {
		http.Error(w, "bad namespace", http.StatusBadRequest)
		return
	}
	nsDir := filepath.Join(dataDir, ns)

	// 목록
	if len(parts) == 3 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		listSaves(w, nsDir)
		return
	}

	// 항목 (정확히 /v1/{ns}/saves/{name})
	if len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	name, okName := safeSeg(parts[3]) // r.URL.Path 는 이미 디코드됨 → 재디코드 불필요
	if !okName {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(nsDir, name)

	switch r.Method {
	case http.MethodPost, http.MethodPut:
		if err := os.MkdirAll(nsDir, 0o755); err != nil {
			http.Error(w, "mkdir error", http.StatusInternalServerError)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		if o := r.Header.Get("X-Origin-Name"); o != "" {
			_ = os.WriteFile(path+".origin", []byte(o), 0o644)
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodGet:
		body, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if o, err := os.ReadFile(path + ".origin"); err == nil {
			w.Header().Set("X-Origin-Name", string(o))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)

	case http.MethodDelete:
		_ = os.Remove(path)
		_ = os.Remove(path + ".origin")
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func listSaves(w http.ResponseWriter, nsDir string) {
	files, _ := os.ReadDir(nsDir)
	out := []entry{}
	for _, f := range files {
		if f.IsDir() || strings.HasSuffix(f.Name(), ".origin") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		origin, _ := os.ReadFile(filepath.Join(nsDir, f.Name()+".origin"))
		out = append(out, entry{
			Name:   f.Name(),
			Origin: string(origin),
			Size:   info.Size(),
			Mtime:  info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mtime > out[j].Mtime })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"saves": out})
}
