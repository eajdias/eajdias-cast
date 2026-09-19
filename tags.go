package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func normTag(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_")
}

func (s *dbStore) GetTags(mediaID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tags := []string{}
	rows, err := s.db.Query(`SELECT tag FROM tags WHERE media_id = ? ORDER BY tag`, mediaID)
	if err != nil {
		return tags
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			tags = append(tags, t)
		}
	}
	return tags
}

func (s *dbStore) AddTag(mediaID, tag string) []string {
	if tag = normTag(tag); tag == "" {
		return s.GetTags(mediaID)
	}
	s.mu.Lock()
	_, _ = s.db.Exec(`INSERT OR IGNORE INTO tags (media_id, tag) VALUES (?, ?)`, mediaID, tag)
	s.mu.Unlock()
	return s.GetTags(mediaID)
}

func (s *dbStore) RemoveTag(mediaID, tag string) []string {
	s.mu.Lock()
	_, _ = s.db.Exec(`DELETE FROM tags WHERE media_id = ? AND tag = ?`, mediaID, normTag(tag))
	s.mu.Unlock()
	return s.GetTags(mediaID)
}

func (s *server) handleTagsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tags": s.db.GetTags(r.URL.Query().Get("media_id"))})
}

func (s *server) handleTagsAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MediaID string `json:"media_id"`
		Tag     string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MediaID == "" {
		http.Error(w, "media_id obrigatório", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": s.db.AddTag(body.MediaID, body.Tag)})
}

func (s *server) handleTagsDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("media_id") == "" {
		http.Error(w, "media_id obrigatório", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": s.db.RemoveTag(q.Get("media_id"), q.Get("tag"))})
}
