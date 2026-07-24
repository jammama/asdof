// asdof-emulator save-server — 세이브 파일을 이름으로 저장/조회하는 작은 HTTP 서버.
//
// 엔드포인트 (모두 X-Token 헤더로 인증; SAVE_TOKEN 미설정 시 개방=개발용):
//   GET    /api/saves            저장된 세이브 목록 (JSON)
//   POST   /api/saves/{name}     저장 (body=바이트, X-Origin-Name=원본 파일명(URI인코딩))
//   GET    /api/saves/{name}     다운로드 (응답 헤더 X-Origin-Name 포함)
//   DELETE /api/saves/{name}     삭제
//
// 환경변수: SAVE_TOKEN(인증 토큰), SAVE_DIR(저장 경로, 기본 ./data), PORT(기본 8760)
// 같은 오리진(nginx /api/ 프록시)에서만 접근하므로 CORS 는 두지 않는다.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxBody = 16 << 20 // 16MB 상한 (배터리 세이브/상태저장 충분)

var (
	dataDir = env("SAVE_DIR", "./data")
	token   = os.Getenv("SAVE_TOKEN")
	addr    = ":" + env("PORT", "8760")
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func authed(r *http.Request) bool {
	if token == "" {
		return true // 개발용: 토큰 미설정이면 개방
	}
	return r.Header.Get("X-Token") == token
}

// 경로 분리자/상위경로/숨김파일 차단.
func safeName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return "", false
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") || strings.HasPrefix(name, ".") {
		return "", false
	}
	return name, true
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
	http.HandleFunc("/api/saves", handleList)
	http.HandleFunc("/api/saves/", handleItem)
	log.Printf("save-server 시작: %s (data=%s)", addr, dataDir)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleList(w http.ResponseWriter, r *http.Request) {
	if !authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	files, _ := os.ReadDir(dataDir)
	out := []entry{}
	for _, f := range files {
		if f.IsDir() || strings.HasSuffix(f.Name(), ".origin") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		origin, _ := os.ReadFile(filepath.Join(dataDir, f.Name()+".origin"))
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

func handleItem(w http.ResponseWriter, r *http.Request) {
	if !authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/api/saves/")
	name, err := url.PathUnescape(raw)
	if err != nil {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	name, ok := safeName(name)
	if !ok {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(dataDir, name)

	switch r.Method {
	case http.MethodPost, http.MethodPut:
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
