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
	CREATE TABLE IF NOT EXISTS progress (
		media_id TEXT PRIMARY KEY,
		position REAL NOT NULL,
		total REAL NOT NULL,
		completed INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS favorites (
		media_id TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL
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
	defer rows.Close()

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

func (s *dbStore) ToggleFavorite(mediaID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM favorites WHERE media_id = ?`, mediaID).Scan(&exists)
	if err == nil {
		s.db.Exec(`DELETE FROM favorites WHERE media_id = ?`, mediaID)
		return false
	}
	s.db.Exec(`INSERT INTO favorites (media_id, created_at) VALUES (?, ?)`, mediaID, time.Now().Unix())
	return true
}

func (s *dbStore) IsFavorite(mediaID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM favorites WHERE media_id = ?`, mediaID).Scan(&exists)
	return err == nil
}

func (s *dbStore) GetAllFavorites() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make(map[string]bool)
	rows, err := s.db.Query(`SELECT media_id FROM favorites`)
	if err != nil {
		return res
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			res[id] = true
		}
	}
	return res
}
