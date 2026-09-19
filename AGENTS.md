# eajdias-cast — Agent Notes

Single-binary Go media server (cinema + manga + music). `package main`, all `*.go` in root. Frontend is vanilla JS in `static/` embedded via `//go:embed static` in `main.go`.

## Commands

```bash
go test ./...          # only catalog_test.go today; fast
go vet ./...           # clean; keep it clean
go run .               # local dev (needs env below)
go build -o /tmp/opencode/cast .
docker compose up -d --build   # canonical deploy path
```

No lint config, no CI, no subpackages. Single focused check is `go test -run TestName ./...`.

## Run locally

```bash
PORT=8080 PASSWORD=cast123 MEDIA_DIR=/tmp/opencode/media CACHE_DIR=/tmp/opencode/cache go run .
```

- `PORT` default `8080` (compose maps `${PORT:-8095}:8080`); `PASSWORD` default `cast123`, empty = auth disabled.
- `MEDIA_DIR` autodetects `filmes|series|anime|videos|cinema|movies|tv`, `manga|mangas`, `musica|music`; overrides: `CINEMA_DIR`, `MANGA_DIR`, `MUSIC_DIR`. Fallbacks are `/data/*` (see `main.go:131-179`).
- `CACHE_DIR` holds `cast.db` (SQLite WAL, `busy_timeout=5000`), `.secret` (`0600` session key, auto-created), `thumbs/` (auto-created). Never commit it.
- `.env` is gitignored. Compose consumes `PORT`, `PASSWORD`, `MEDIA_BIND`, `CACHE_BIND` — keep `.env.example` to those four keys.

## Architecture quirks

- `main.go`: routing (`net/http` ServeMux with `GET /{$}` pattern), 30s rescan throttle (`rescanIfNeeded`, I/O outside the write lock), background pre-index at startup.
- `cinema.go`: video scan/grouping, `ffprobe` for audio/subtitle tracks, `ffmpeg` for embedded-subtitle extraction. `http.ServeContent` handles Range/`206` — do not reimplement.
- `thumbs.go`: video thumbs shell out to `ffmpeg`, max 2 concurrent (`s.sem`). Frame candidates (ss 30/10/5/1) pass a brightness check — first non-black wins, v4 cache key includes mtime+size. Requires `ffmpeg`+`ffprobe` on PATH locally (Docker image installs them); without them video thumbs/subs fail.
- `manga.go`: reads `.cbz`/`.zip`/image dirs without extracting; natural sort (`Cap 2` < `Cap 10`).
- `music.go`: `github.com/dhowden/tag` for ID3 + embedded covers.
- `db.go`: pure-Go `modernc.org/sqlite`, `CGO_ENABLED=0` (see `Dockerfile`). Progress + favorites tables only. Progress `completed` sticks (reopen+close won't un-complete); dismiss via `DELETE /api/progress?media_id=`; video saves <3s are skipped client-side.
- `auth.go`: HMAC cookie `cast_session` (30d, HttpOnly, Lax). `/api/*` returns `401` JSON; browser paths redirect to `/login`.
- `static/` changes require rebuild (embedded). `handleIndex` cache-busts `app.js`/`style.css` via content hash.
- Container runs as uid `20000` (`cast`); media mount is `:ro`.

## Conventions

- User-facing strings are pt-BR; keep new messages/errors in pt-BR.
- Keep it dependency-light (stdlib + `tag`, `x/image`, `sqlite`). No frameworks.
