package main

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dhowden/tag"
)

type Track struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	TrackNumber int    `json:"track_number"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	HasCover    bool   `json:"has_cover"`
}

type AlbumGroup struct {
	Album   string  `json:"album"`
	Artist  string  `json:"artist"`
	CoverID string  `json:"cover_id"`
	Tracks  []Track `json:"tracks"`
}

func isAudioFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp3" || ext == ".flac" || ext == ".m4a" || ext == ".ogg" || ext == ".opus" || ext == ".wav" || ext == ".aac"
}

func scanMusic(dir string) ([]AlbumGroup, map[string]Track) {
	trackMap := make(map[string]Track)
	albumsMap := make(map[string]*AlbumGroup)

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isAudioFile(d.Name()) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		trackID := hashString(path)
		t := Track{
			ID:     trackID,
			Title:  strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())),
			Artist: "Artista Desconhecido",
			Album:  "Álbum Desconhecido",
			Path:   path,
			Size:   info.Size(),
		}

		// Tentar ler tags ID3/metadata
		if f, err := os.Open(path); err == nil {
			if m, err := tag.ReadFrom(f); err == nil {
				if m.Title() != "" {
					t.Title = m.Title()
				}
				if m.Artist() != "" {
					t.Artist = m.Artist()
				}
				if m.Album() != "" {
					t.Album = m.Album()
				}
				tr, _ := m.Track()
				t.TrackNumber = tr
				if m.Picture() != nil {
					t.HasCover = true
				}
			}
			f.Close()
		}

		trackMap[trackID] = t

		key := t.Artist + " - " + t.Album
		group, exists := albumsMap[key]
		if !exists {
			group = &AlbumGroup{
				Album:   t.Album,
				Artist:  t.Artist,
				CoverID: t.ID,
				Tracks:  []Track{},
			}
			albumsMap[key] = group
		}
		if t.HasCover {
			group.CoverID = t.ID
		}
		group.Tracks = append(group.Tracks, t)

		return nil
	})

	var result []AlbumGroup
	for _, grp := range albumsMap {
		sort.Slice(grp.Tracks, func(i, j int) bool {
			if grp.Tracks[i].TrackNumber != grp.Tracks[j].TrackNumber && grp.Tracks[i].TrackNumber > 0 && grp.Tracks[j].TrackNumber > 0 {
				return grp.Tracks[i].TrackNumber < grp.Tracks[j].TrackNumber
			}
			return naturalLess(grp.Tracks[i].Title, grp.Tracks[j].Title)
		})
		result = append(result, *grp)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Artist != result[j].Artist {
			return strings.ToLower(result[i].Artist) < strings.ToLower(result[j].Artist)
		}
		return strings.ToLower(result[i].Album) < strings.ToLower(result[j].Album)
	})

	return result, trackMap
}

func (s *server) serveAudioCover(w http.ResponseWriter, r *http.Request, t Track) {
	f, err := os.Open(t.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	if m, err := tag.ReadFrom(f); err == nil && m.Picture() != nil {
		pic := m.Picture()
		w.Header().Set("Content-Type", pic.MIMEType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeContent(w, r, "cover", t.GetModTime(f), bytes.NewReader(pic.Data))
		return
	}

	// Fallback para cover.jpg ou folder.jpg na pasta do arquivo
	dir := filepath.Dir(t.Path)
	for _, cand := range []string{"cover.jpg", "cover.png", "folder.jpg", "front.jpg"} {
		cp := filepath.Join(dir, cand)
		if fi, err := os.Stat(cp); err == nil && !fi.IsDir() {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeFile(w, r, cp)
			return
		}
	}

	http.NotFound(w, r)
}

func (t *Track) GetModTime(f *os.File) time.Time {
	if fi, err := f.Stat(); err == nil {
		return fi.ModTime()
	}
	return time.Now()
}
