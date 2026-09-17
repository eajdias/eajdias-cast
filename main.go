package main

import (
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
	cinemaDir string
	mangaDir  string
	musicDir  string
	cacheDir  string
	password  string
	secret    []byte
	db        *dbStore

	mu          sync.RWMutex
	lastScan    time.Time
	cinemaItems []VideoGroup
	cinemaMap   map[string]VideoMedia
	mangaItems  []MangaSeries
	mangaChMap  map[string]MangaChapter
	musicAlbums []AlbumGroup
	musicMap    map[string]Track

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

	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.lastScan) < 30*time.Second && s.cinemaItems != nil {
		return
	}

	cinemaGroups, cinemaMedia := scanCinema(s.cinemaDir)
	mangaList := scanManga(s.mangaDir)
	musicList, trackMap := scanMusic(s.musicDir)

	mangaCh := make(map[string]MangaChapter)
	for _, m := range mangaList {
		for _, ch := range m.Chapters {
			mangaCh[ch.ID] = ch
		}
	}

	s.cinemaItems = cinemaGroups
	s.cinemaMap = cinemaMedia
	s.mangaItems = mangaList
	s.mangaChMap = mangaCh
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

func main() {
	cinemaDir := env("CINEMA_DIR", "/data/cinema")
	mangaDir := env("MANGA_DIR", "/data/manga")
	musicDir := env("MUSIC_DIR", "/data/music")
	cacheDir := env("CACHE_DIR", "/data/cache")
	password := env("PASSWORD", "cast123")
	port := env("PORT", "8080")

	_ = os.MkdirAll(cinemaDir, 0755)
	_ = os.MkdirAll(mangaDir, 0755)
	_ = os.MkdirAll(musicDir, 0755)
	_ = os.MkdirAll(filepath.Join(cacheDir, "thumbs"), 0755)

	db := initDB(cacheDir)
	secret := getOrInitSecret(filepath.Join(cacheDir, ".secret"))

	srv := &server{
		cinemaDir: cinemaDir,
		mangaDir:  mangaDir,
		musicDir:  musicDir,
		cacheDir:  cacheDir,
		password:  password,
		secret:    secret,
		db:        db,
		sem:       make(chan struct{}, 2),
		inflight:  make(map[string]*thumbCall),
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
	mux.HandleFunc("GET /api/manga/page", srv.requireAuth(srv.handleMangaPage))

	// API Música
	mux.HandleFunc("GET /api/music", srv.requireAuth(srv.handleMusicList))
	mux.HandleFunc("GET /api/music/cover", srv.requireAuth(srv.handleMusicCover))
	mux.HandleFunc("GET /api/music/stream", srv.requireAuth(srv.handleMusicStream))

	addr := ":" + port
	log.Printf("[eajdias-cast] Servidor iniciado na porta %s", port)
	log.Printf("[eajdias-cast] Cinema: %s | Manga: %s | Musica: %s", cinemaDir, mangaDir, musicDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html não encontrado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

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
	}

	var continueList []ContinueItem
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
	writeJSON(w, http.StatusOK, s.cinemaItems)
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
	subPath := r.URL.Query().Get("path")
	if subPath == "" {
		http.NotFound(w, r)
		return
	}
	serveSubtitle(w, r, subPath)
}

func (s *server) handleMangaList(w http.ResponseWriter, r *http.Request) {
	s.rescanIfNeeded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, http.StatusOK, s.mangaItems)
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
	writeJSON(w, http.StatusOK, s.musicAlbums)
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
