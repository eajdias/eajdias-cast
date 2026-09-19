package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

type server struct {
	mediaDir   string
	cinemaDirs []string
	mangaDir   string
	musicDir   string
	cacheDir   string
	password   string
	secret     []byte
	db         *dbStore

	mu                sync.RWMutex
	lastScan          time.Time
	cinemaItems       []VideoGroup
	cinemaMap         map[string]VideoMedia
	mangaItems        []MangaSeries
	mangaChMap        map[string]MangaChapter
	mangaCoverMap     map[string]string
	mangaChapterCover map[string]string
	subtitleMap       map[string]subtitleRef
	musicAlbums       []AlbumGroup
	musicMap          map[string]Track

	sem      chan struct{}
	tmu      sync.Mutex
	inflight map[string]*thumbCall
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *server) rescanIfNeeded() {
	s.mu.RLock()
	if time.Since(s.lastScan) < 30*time.Second && s.cinemaItems != nil {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	// I/O pesado (ffprobe por arquivo, zips, tags ID3) fora do lock de
	// escrita: antes o Lock() envolvia o scan inteiro e travava todas
	// as APIs e thumbs. cinemaDirs/mangaDir/musicDir são fixos no startup.
	cinemaGroups, cinemaMedia := scanCinema(s.cinemaDirs...)
	mangaList, mangaCovers := scanManga(s.mangaDir)
	musicList, trackMap := scanMusic(s.musicDir)

	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.lastScan) < 30*time.Second && s.cinemaItems != nil {
		return // outra goroutine já atualizou enquanto escaneávamos
	}

	// Listas nunca nil: biblioteca vazia deve serializar como [] e
	// manter o throttle do rescan funcionando.
	if cinemaGroups == nil {
		cinemaGroups = []VideoGroup{}
	}
	if mangaList == nil {
		mangaList = []MangaSeries{}
	}
	if musicList == nil {
		musicList = []AlbumGroup{}
	}

	mangaCh := make(map[string]MangaChapter)
	mangaChapterCover := make(map[string]string)
	subtitles := make(map[string]subtitleRef)
	for _, video := range cinemaMedia {
		for _, sub := range video.Subtitles {
			subtitles[sub.ID] = subtitleRef{Path: sub.Path, StreamIndex: sub.StreamIndex}
		}
	}
	for _, m := range mangaList {
		for _, ch := range m.Chapters {
			mangaCh[ch.ID] = ch
			mangaChapterCover[ch.ID] = m.CoverURL
		}
	}

	s.cinemaItems = cinemaGroups
	s.cinemaMap = cinemaMedia
	s.mangaItems = mangaList
	s.mangaChMap = mangaCh
	s.mangaCoverMap = mangaCovers
	s.mangaChapterCover = mangaChapterCover
	s.subtitleMap = subtitles
	s.musicAlbums = musicList
	s.musicMap = trackMap
	s.lastScan = time.Now()
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			if r.Header.Get("X-Requested-With") == "XMLHttpRequest" || len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "não autenticado"})
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func main() {
	mediaDir := env("MEDIA_DIR", "/data/media")
	cacheDir := env("CACHE_DIR", "/data/cache")
	password := env("PASSWORD", "cast123")
	port := env("PORT", "8080")

	// Autodetectar estrutura dentro de MEDIA_DIR se fornecido
	cinemaDirEnv := env("CINEMA_DIR", "")
	mangaDirEnv := env("MANGA_DIR", "")
	musicDirEnv := env("MUSIC_DIR", "")

	var cinemaDirs []string
	var mangaDir string
	var musicDir string

	if mangaDirEnv != "" {
		mangaDir = mangaDirEnv
	} else if dirExists(filepath.Join(mediaDir, "manga")) {
		mangaDir = filepath.Join(mediaDir, "manga")
	} else if dirExists(filepath.Join(mediaDir, "mangas")) {
		mangaDir = filepath.Join(mediaDir, "mangas")
	} else {
		mangaDir = "/data/manga"
	}

	if musicDirEnv != "" {
		musicDir = musicDirEnv
	} else if dirExists(filepath.Join(mediaDir, "musica")) {
		musicDir = filepath.Join(mediaDir, "musica")
	} else if dirExists(filepath.Join(mediaDir, "music")) {
		musicDir = filepath.Join(mediaDir, "music")
	} else {
		musicDir = "/data/music"
	}

	if cinemaDirEnv != "" {
		cinemaDirs = []string{cinemaDirEnv}
	} else {
		// Checar se há subpastas de vídeo na raiz
		for _, cand := range []string{"filmes", "series", "anime", "videos", "cinema", "movies", "tv"} {
			p := filepath.Join(mediaDir, cand)
			if dirExists(p) {
				cinemaDirs = append(cinemaDirs, p)
			}
		}
		if len(cinemaDirs) == 0 {
			cinemaDirs = []string{filepath.Join(mediaDir, "cinema")}
		}
	}

	_ = os.MkdirAll(cacheDir, 0755)
	_ = os.MkdirAll(filepath.Join(cacheDir, "thumbs"), 0755)

	db := initDB(cacheDir)
	secret := getOrInitSecret(filepath.Join(cacheDir, ".secret"))

	srv := &server{
		mediaDir:          mediaDir,
		cinemaDirs:        cinemaDirs,
		mangaDir:          mangaDir,
		musicDir:          musicDir,
		cacheDir:          cacheDir,
		password:          password,
		secret:            secret,
		db:                db,
		mangaCoverMap:     make(map[string]string),
		mangaChapterCover: make(map[string]string),
		subtitleMap:       make(map[string]subtitleRef),
		sem:               make(chan struct{}, 2),
		inflight:          make(map[string]*thumbCall),
	}

	// Pré-indexar mídias no startup em background
	go srv.rescanIfNeeded()

	mux := http.NewServeMux()

	// Auth
	mux.HandleFunc("GET /login", srv.handleLogin)
	mux.HandleFunc("POST /login", srv.handleLogin)
	mux.HandleFunc("POST /logout", srv.handleLogout)

	// Static & SPA Root
	mux.HandleFunc("GET /{$}", srv.requireAuth(srv.handleIndex))
	mux.HandleFunc("GET /static/{file}", srv.handleStatic)

	// API Gerais
	mux.HandleFunc("GET /api/overview", srv.requireAuth(srv.handleOverview))
	mux.HandleFunc("POST /api/progress", srv.requireAuth(srv.handleProgress))
	mux.HandleFunc("POST /api/favorite", srv.requireAuth(srv.handleFavorite))
	mux.HandleFunc("GET /api/thumb", srv.requireAuth(srv.handleThumb))

	// API Cinema
	mux.HandleFunc("GET /api/cinema", srv.requireAuth(srv.handleCinemaList))
	mux.HandleFunc("GET /api/cinema/stream", srv.requireAuth(srv.handleCinemaStream))
	mux.HandleFunc("GET /api/cinema/sub", srv.requireAuth(srv.handleCinemaSub))

	// API Manga
	mux.HandleFunc("GET /api/manga", srv.requireAuth(srv.handleMangaList))
	mux.HandleFunc("GET /api/manga/cover", srv.requireAuth(srv.handleMangaCover))
	mux.HandleFunc("GET /api/manga/page", srv.requireAuth(srv.handleMangaPage))

	// API Música
	mux.HandleFunc("GET /api/music", srv.requireAuth(srv.handleMusicList))
	mux.HandleFunc("GET /api/music/cover", srv.requireAuth(srv.handleMusicCover))
	mux.HandleFunc("GET /api/music/stream", srv.requireAuth(srv.handleMusicStream))

	addr := ":" + port
	log.Printf("[eajdias-cast] Servidor iniciado na porta %s", port)
	log.Printf("[eajdias-cast] Cinema Pastas: %v | Manga: %s | Musica: %s", cinemaDirs, mangaDir, musicDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html não encontrado", http.StatusInternalServerError)
		return
	}
	assetHash := hashString(string(mustStatic("static/app.js")) + string(mustStatic("static/style.css")))
	data = bytes.ReplaceAll(data, []byte("/static/style.css"), []byte("/static/style.css?v="+assetHash))
	data = bytes.ReplaceAll(data, []byte("/static/app.js"), []byte("/static/app.js?v="+assetHash))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func mustStatic(name string) []byte { b, _ := staticFS.ReadFile(name); return b }

func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	data, err := staticFS.ReadFile("static/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if filepath.Ext(file) == ".css" {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else if filepath.Ext(file) == ".js" {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Write(data)
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	s.mu.RLock()
	defer s.mu.RUnlock()

	allProgress := s.db.GetAllProgress()
	allFavs := s.db.GetAllFavorites()

	// Coletar itens com progresso ("Continuar Assistindo" / "Continuar Lendo")
	type ContinueItem struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Parent    string  `json:"parent,omitempty"`
		Type      string  `json:"type"` // movie, episode, manga
		Position  float64 `json:"position"`
		Total     float64 `json:"total"`
		UpdatedAt int64   `json:"updated_at"`
		CoverURL  string  `json:"cover_url,omitempty"`
	}

	continueList := []ContinueItem{}
	for mediaID, prog := range allProgress {
		if prog.Completed || prog.Position <= 0 {
			continue
		}
		if v, ok := s.cinemaMap[mediaID]; ok {
			continueList = append(continueList, ContinueItem{
				ID:        v.ID,
				Title:     v.Title,
				Parent:    v.Parent,
				Type:      v.Type,
				Position:  prog.Position,
				Total:     prog.Total,
				UpdatedAt: prog.UpdatedAt,
			})
		} else if ch, ok := s.mangaChMap[mediaID]; ok {
			continueList = append(continueList, ContinueItem{
				ID:        ch.ID,
				Title:     ch.Title,
				Type:      "manga",
				Position:  prog.Position,
				Total:     prog.Total,
				UpdatedAt: prog.UpdatedAt,
			})
		}
	}
	for i := range continueList {
		if v, ok := s.cinemaMap[continueList[i].ID]; ok {
			continueList[i].CoverURL = "/api/thumb?id=" + v.ID
		} else if cover := s.mangaChapterCover[continueList[i].ID]; cover != "" {
			continueList[i].CoverURL = cover
		}
	}

	resp := map[string]any{
		"counts": map[string]int{
			"cinema": len(s.cinemaMap),
			"manga":  len(s.mangaItems),
			"music":  len(s.musicMap),
		},
		"continue":  continueList,
		"favorites": allFavs,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleCinemaList(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := append([]VideoGroup{}, s.cinemaItems...)
	for i := range items {
		items[i].CoverURL = "/api/thumb?id=" + items[i].ID
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) handleCinemaStream(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	id := r.URL.Query().Get("id")
	s.mu.RLock()
	v, ok := s.cinemaMap[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if audio, err := strconv.Atoi(r.URL.Query().Get("audio")); err == nil && audio >= 0 && audio < len(v.AudioTracks) {
		s.serveSelectedAudio(w, r, v.Path, audio)
		return
	}

	f, err := os.Open(v.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, _ := f.Stat()
	w.Header().Set("Cache-Control", "private, max-age=86400")
	// http.ServeContent lida com range requests automaticamente
	http.ServeContent(w, r, filepath.Base(v.Path), fi.ModTime(), f)
}

func (s *server) handleCinemaSub(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	subID := r.URL.Query().Get("id")
	s.mu.RLock()
	sub, ok := s.subtitleMap[subID]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if sub.Path != "" {
		serveSubtitle(w, r, sub.Path)
		return
	}
	serveEmbeddedSubtitle(w, r, sub)
}

func (s *server) handleMangaList(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.mangaItems
	if items == nil {
		items = []MangaSeries{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) handleMangaPage(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	chID := r.URL.Query().Get("ch")
	pStr := r.URL.Query().Get("p")
	pIdx, _ := strconv.Atoi(pStr)

	s.mu.RLock()
	ch, ok := s.mangaChMap[chID]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	s.serveMangaPage(w, r, ch, pIdx)
}

func (s *server) handleMusicList(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.musicAlbums
	if items == nil {
		items = []AlbumGroup{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) handleMusicCover(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	id := r.URL.Query().Get("id")
	s.mu.RLock()
	t, ok := s.musicMap[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveAudioCover(w, r, t)
}

func (s *server) handleMusicStream(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	id := r.URL.Query().Get("id")
	s.mu.RLock()
	t, ok := s.musicMap[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(t.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, _ := f.Stat()
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, filepath.Base(t.Path), fi.ModTime(), f)
}

func (s *server) handleProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MediaID   string  `json:"media_id"`
		Position  float64 `json:"position"`
		Total     float64 `json:"total"`
		Completed bool    `json:"completed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}

	if body.MediaID == "" {
		http.Error(w, "media_id obrigatório", http.StatusBadRequest)
		return
	}

	_ = s.db.SaveProgress(body.MediaID, body.Position, body.Total, body.Completed)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MediaID string `json:"media_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MediaID == "" {
		http.Error(w, "media_id obrigatório", http.StatusBadRequest)
		return
	}
	fav := s.db.ToggleFavorite(body.MediaID)
	writeJSON(w, http.StatusOK, map[string]bool{"fav": fav})
}

func (s *server) handleMangaCover(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	id := r.URL.Query().Get("id")
	s.mu.RLock()
	coverPath, ok := s.mangaCoverMap[id]
	s.mu.RUnlock()
	if !ok || coverPath == "" {
		http.NotFound(w, r)
		return
	}
	s.serveMangaCover(w, r, coverPath)
}
