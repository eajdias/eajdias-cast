package main

import (
	"database/sql"
	"log"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type dbStore struct {
	mu sync.RWMutex
	db *sql.DB
}

type ProgressRec struct {
	MediaID   string  `json:"media_id"`
	Position  float64 `json:"position"`
	Total     float64 `json:"total"`
	Completed bool    `json:"completed"`
	UpdatedAt int64   `json:"updated_at"`
}

func initDB(dataDir string) *dbStore {
	dbPath := filepath.Join(dataDir, "cast.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		log.Fatalf("Falha ao abrir SQLite: %v", err)
	}

	schema := `
	DROP TABLE IF EXISTS favorites;
	CREATE TABLE IF NOT EXISTS progress (
		media_id TEXT PRIMARY KEY,
		position REAL NOT NULL,
		total REAL NOT NULL,
		completed INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS tags (
		media_id TEXT NOT NULL,
		tag TEXT NOT NULL,
		PRIMARY KEY (media_id, tag)
	);

	CREATE TABLE IF NOT EXISTS media_cache (
		path TEXT PRIMARY KEY,
		mtime INTEGER NOT NULL,
		size INTEGER NOT NULL,
		kind TEXT NOT NULL,
		data TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS manga_covers (
		series_id TEXT PRIMARY KEY,
		chapter_id TEXT NOT NULL,
		page INTEGER NOT NULL DEFAULT 0
	);
	`
	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("Falha ao criar tabelas: %v", err)
	}

	return &dbStore{db: db}
}

func (s *dbStore) SaveProgress(mediaID string, position, total float64, completed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 50%+ conta como visto/lido: sai do "Continuar" sozinho.
	if !completed && total > 0 && position >= total/2 {
		completed = true
	}

	// Concluído é pegajoso: reabrir e fechar um título terminado não
	// o traz de volta ao "Continuar".
	if !completed {
		var done int
		if err := s.db.QueryRow(`SELECT completed FROM progress WHERE media_id = ?`, mediaID).Scan(&done); err == nil && done == 1 {
			completed = true
		}
	}

	compInt := 0
	if completed {
		compInt = 1
	}

	query := `
	INSERT INTO progress (media_id, position, total, completed, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(media_id) DO UPDATE SET
		position=excluded.position,
		total=excluded.total,
		completed=excluded.completed,
		updated_at=excluded.updated_at
	`
	_, err := s.db.Exec(query, mediaID, position, total, compInt, time.Now().Unix())
	return err
}

func (s *dbStore) GetProgress(mediaID string) (ProgressRec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rec ProgressRec
	var compInt int
	query := `SELECT media_id, position, total, completed, updated_at FROM progress WHERE media_id = ?`
	row := s.db.QueryRow(query, mediaID)
	err := row.Scan(&rec.MediaID, &rec.Position, &rec.Total, &compInt, &rec.UpdatedAt)
	if err != nil {
		return ProgressRec{}, false
	}
	rec.Completed = (compInt == 1)
	return rec, true
}

func (s *dbStore) GetAllProgress() map[string]ProgressRec {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make(map[string]ProgressRec)
	rows, err := s.db.Query(`SELECT media_id, position, total, completed, updated_at FROM progress`)
	if err != nil {
		return res
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var rec ProgressRec
		var compInt int
		if err := rows.Scan(&rec.MediaID, &rec.Position, &rec.Total, &compInt, &rec.UpdatedAt); err == nil {
			rec.Completed = (compInt == 1)
			res[rec.MediaID] = rec
		}
	}
	return res
}

func (s *dbStore) DeleteProgress(mediaID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec(`DELETE FROM progress WHERE media_id = ?`, mediaID)
}

type MangaCoverRec struct {
	SeriesID  string
	ChapterID string
	Page      int
}

func (s *dbStore) SetMangaCover(seriesID, chapterID string, page int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec(`INSERT INTO manga_covers (series_id, chapter_id, page) VALUES (?, ?, ?)
		ON CONFLICT(series_id) DO UPDATE SET chapter_id=excluded.chapter_id, page=excluded.page`,
		seriesID, chapterID, page)
}

func (s *dbStore) GetMangaCover(seriesID string) (MangaCoverRec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var rec MangaCoverRec
	err := s.db.QueryRow(`SELECT series_id, chapter_id, page FROM manga_covers WHERE series_id = ?`, seriesID).Scan(&rec.SeriesID, &rec.ChapterID, &rec.Page)
	if err != nil {
		return MangaCoverRec{}, false
	}
	return rec, true
}

func (s *dbStore) ClearMangaCover(seriesID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec(`DELETE FROM manga_covers WHERE series_id = ?`, seriesID)
}

// getMediaCache devolve o dado cacheado se mtime+tamanho conferem.
// Nil-safe: sem banco, apenas miss.
func (s *dbStore) getMediaCache(path string, mtime, size int64, kind string) (string, bool) {
	if s == nil || s.db == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var m, sz int64
	var k, data string
	err := s.db.QueryRow(`SELECT mtime, size, kind, data FROM media_cache WHERE path = ?`, path).Scan(&m, &sz, &k, &data)
	if err != nil || k != kind || m != mtime || sz != size {
		return "", false
	}
	return data, true
}

func (s *dbStore) setMediaCache(path string, mtime, size int64, kind, data string) {
	if s == nil || s.db == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, _ = s.db.Exec(`INSERT INTO media_cache (path, mtime, size, kind, data) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET mtime=excluded.mtime, size=excluded.size, kind=excluded.kind, data=excluded.data`,
		path, mtime, size, kind, data)
}

func (s *dbStore) pruneMediaCache(keep map[string]struct{}, kind string) {
	if s == nil || s.db == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT path FROM media_cache WHERE kind = ?`, kind)
	if err != nil {
		return
	}
	var drop []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			if _, ok := keep[p]; !ok {
				drop = append(drop, p)
			}
		}
	}
	_ = rows.Close()
	for _, p := range drop {
		_, _ = s.db.Exec(`DELETE FROM media_cache WHERE path = ?`, p)
	}
}
