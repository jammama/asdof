# asdof-saves

여러 앱/게임의 **세이브 파일을 이름으로 저장**하는 범용 스토리지 서비스 (Go 단일 바이너리).
`asdof-emulator` 뿐 아니라 다른 게임/앱의 세이브 보관에도 재사용한다.

- **네임스페이스**로 앱/게임을 격리 — `asdof-emulator`, `my-other-game` 등.
- **토큰 인증**(공유 토큰) + **CORS**(다른 오리진에서도 호출 가능).
- 원본 파일명을 `.origin` 사이드카로 보존(받을 때 원래 이름 복원).

## API

```
GET    /health                      상태 확인 (인증 없음)
GET    /v1/{ns}/saves               목록 (JSON: {saves:[{name,origin,size,mtime}]})
POST   /v1/{ns}/saves/{name}        저장 (body=바이트, 헤더 X-Origin-Name=원본파일명(URI인코딩))
GET    /v1/{ns}/saves/{name}        다운로드 (응답 헤더 X-Origin-Name)
DELETE /v1/{ns}/saves/{name}        삭제
```

모든 `/v1/...` 요청은 `X-Token` 헤더로 인증(= `SAVE_TOKEN`). 미설정 시 개방(개발용).

## 로컬 실행

```bash
SAVE_TOKEN=test go run .          # http://127.0.0.1:8760
# 확인
curl -H "X-Token: test" http://127.0.0.1:8760/v1/asdof-emulator/saves
```

앱(예: 에뮬레이터)에서 이 로컬 주소를 쓰려면 설정에서 서버 URL 을 `http://localhost:8760` 로.

## 배포 (saves.asdof.xyz)

```bash
SAVE_TOKEN=원하는비밀토큰 ./deploy.sh          # 아키텍처 자동감지 빌드 + systemd 기동
# nginx (1회):
sudo cp nginx-saves.conf /etc/nginx/sites-available/saves.conf
sudo ln -s /etc/nginx/sites-available/saves.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d saves.asdof.xyz
```

> 서비스는 `127.0.0.1:8760` 로만 listen → **외부 포트 개방 불필요**. nginx(443)만 노출.
> DNS 에 `saves.asdof.xyz` A레코드가 서버를 가리켜야 함.

환경변수: `SAVE_TOKEN` · `ALLOW_ORIGIN`(기본 `*`, 특정 오리진만 허용하려면 지정) ·
`SAVE_DIR`(기본 `/var/lib/asdof-saves/data`) · `PORT`(기본 8760).

## 저장 구조

```
{SAVE_DIR}/{namespace}/{name}          세이브 바이트
{SAVE_DIR}/{namespace}/{name}.origin   원본 파일명(옵션)
```
