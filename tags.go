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
	defer func() { _ = rows.Close() }()
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

type tagFreq struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// AllTags devolve tags com contagem de uso (para filtro/autocomplete,
// padrão reaproveitado do zub-vault: prefixo + ordem por frequência).
func (s *dbStore) AllTags(prefix string, limit int) []tagFreq {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT tag, COUNT(*) FROM tags GROUP BY tag ORDER BY COUNT(*) DESC, tag`)
	if err != nil {
		return []tagFreq{}
	}
	defer func() { _ = rows.Close() }()
	prefix = normTag(prefix)
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	out := []tagFreq{}
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err != nil {
			continue
		}
		if prefix != "" && len(t) < len(prefix) {
			continue
		}
		if prefix != "" && t[:len(prefix)] != prefix {
			continue
		}
		out = append(out, tagFreq{Tag: t, Count: c})
		if len(out) >= limit {
			break
		}
	}
	if out == nil {
		out = []tagFreq{}
	}
	return out
}

// MediaIDsWithTags devolve os media_ids que contêm TODAS as tags (AND).
func (s *dbStore) MediaIDsWithTags(tags []string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	norm := []string{}
	for _, t := range tags {
		if t = normTag(t); t != "" {
			norm = append(norm, t)
		}
	}
	if len(norm) == 0 {
		return nil
	}
	place := make([]string, len(norm))
	args := make([]any, len(norm))
	for i, t := range norm {
		place[i] = "?"
		args[i] = t
	}
	rows, err := s.db.Query(`SELECT media_id, COUNT(*) FROM tags WHERE tag IN (`+joinComma(place)+`) GROUP BY media_id HAVING COUNT(*) >= ?`, append(args, len(norm))...)
	if err != nil {
		return map[string]bool{}
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err == nil {
			out[id] = true
		}
	}
	return out
}

func joinComma(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
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

// handleTagsAll serve autocomplete/filtro global (padrão zub-vault).
func (s *server) handleTagsAll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 20
	if n := q.Get("limit"); n != "" {
		for _, c := range n {
			if c < '0' || c > '9' {
				n = ""
				break
			}
		}
		if n != "" {
			var v int
			for _, c := range n {
				v = v*10 + int(c-'0')
			}
			if v > 0 {
				limit = v
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": s.db.AllTags(q.Get("prefix"), limit)})
}
