package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Vault 는 설정 파일에 들어가는 비밀값(사이트 비밀번호 · Notion 토큰)을
// AES-256-GCM 으로 봉인한다. 키는 서버에만 있는 키 파일(0600)에서 온다.
//
// 위협 모델: 설정 파일이 백업·실수로 유출돼도 키 파일 없이는 비밀값을 못 읽는다.
// 서버 루트 권한을 가진 사람은 어차피 둘 다 읽을 수 있으므로 그건 막지 않는다.
type Vault struct {
	aead cipher.AEAD
	key  []byte
}

// OpenVault 는 keyPath 의 32바이트 키를 읽고, 없으면 새로 만든다.
// 환경변수 TAKER_SECRET_KEY 가 있으면 그 값을 SHA-256 해서 키로 쓴다(키 파일 무시).
func OpenVault(keyPath string) (*Vault, error) {
	var key []byte
	if env := os.Getenv("TAKER_SECRET_KEY"); env != "" {
		sum := sha256.Sum256([]byte(env))
		key = sum[:]
	} else {
		var err error
		if key, err = loadOrCreateKey(keyPath); err != nil {
			return nil, err
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead, key: key}, nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("키 파일이 손상됐습니다: %s (32바이트 hex 여야 함)", path)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("키 파일 읽기 실패: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, fmt.Errorf("키 파일 생성 실패: %w", err)
	}
	return key, nil
}

// SessionKey 는 웹 세션 쿠키 서명용 파생 키다. 재시작해도 같은 값이라
// 로그인 세션이 유지된다.
func (v *Vault) SessionKey() []byte {
	mac := hmac.New(sha256.New, v.key)
	mac.Write([]byte("asdof-taker/session"))
	return mac.Sum(nil)
}

// Encrypt 는 평문을 봉인해 base64 문자열로 돌려준다. 빈 문자열은 그대로 빈 문자열.
func (v *Vault) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := v.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 는 Encrypt 의 역이다. 빈 문자열은 빈 문자열.
func (v *Vault) Decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("비밀값 형식이 잘못됨")
	}
	ns := v.aead.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("비밀값이 손상됨")
	}
	plain, err := v.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("비밀값을 복호화할 수 없습니다 (키 파일이 바뀌었나요?)")
	}
	return string(plain), nil
}

// ── 관리자 비밀번호 해시 ────────────────────────────────────────────────────

const pbkdf2Iter = 200_000

// HashPassword 는 관리자 비밀번호를 pbkdf2$iter$salt$hash 형태로 만든다.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword 는 상수 시간 비교로 비밀번호를 확인한다.
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SignCookie / VerifyCookie 는 세션 쿠키를 HMAC 으로 서명한다.
func SignCookie(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func VerifyCookie(key []byte, token string) (string, bool) {
	i := strings.LastIndex(token, ".")
	if i < 0 {
		return "", false
	}
	payload, sig := token[:i], token[i+1:]
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return "", false
	}
	return payload, true
}

// Mask 는 로그·화면에 비밀값의 존재만 드러낼 때 쓴다. 원문은 절대 복원되지 않는다.
func Mask(s string) string {
	if s == "" {
		return "(미설정)"
	}
	return "****"
}
