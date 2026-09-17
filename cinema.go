package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type SubtitleTrack struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Lang  string `json:"lang"`
	Path  string `json:"path"`
}

type VideoMedia struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Parent    string          `json:"parent,omitempty"` // Série
	Season    int             `json:"season,omitempty"`
	Episode   int             `json:"episode,omitempty"`
	Type      string          `json:"type"`             // "movie" | "episode"
	Path      string          `json:"path"`
	Size      int64           `json:"size"`
	Duration  float64         `json:"duration,omitempty"`
	Subtitles []SubtitleTrack `json:"subtitles,omitempty"`
}

type VideoGroup struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Type     string       `json:"type"` // "movie" | "show"
	Episodes []VideoMedia `json:"episodes,omitempty"`
	Movie    *VideoMedia  `json:"movie,omitempty"`
}

func isVideoFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp4" || ext == ".mkv" || ext == ".webm" || ext == ".mov" || ext == ".avi" || ext == ".m4v"
}

func isSubtitleFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".srt" || ext == ".vtt"
}

var (
	epRegex1 = regexp.MustCompile(`(?i)[sS]([0-9]{1,2})[eE]([0-9]{1,2})`)
	epRegex2 = regexp.MustCompile(`(?i)(?:ep|episodio|episode|e)[_.\s-]*([0-9]{1,3})`)
	seasonRegex = regexp.MustCompile(`(?i)(?:temporada|season|s)[_.\s-]*([0-9]{1,2})`)
)

func parseEpisodeInfo(name string, dirName string) (int, int) {
	s, e := 1, 0
	if m := epRegex1.FindStringSubmatch(name); len(m) == 3 {
		s, _ = strconv.Atoi(m[1])
		e, _ = strconv.Atoi(m[2])
		return s, e
	}
	if m := epRegex2.FindStringSubmatch(name); len(m) == 2 {
		e, _ = strconv.Atoi(m[1])
	}
	if m := seasonRegex.FindStringSubmatch(dirName); len(m) == 2 {
		s, _ = strconv.Atoi(m[1])
	}
	return s, e
}

func scanCinema(dir string) ([]VideoGroup, map[string]VideoMedia) {
	mediaMap := make(map[string]VideoMedia)
	showsMap := make(map[string]*VideoGroup)
	var movies []VideoGroup

	// Primeiro, coletar todas as legendas disponíveis indexadas pelo nome base
	subMap := make(map[string][]SubtitleTrack)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if isSubtitleFile(d.Name()) {
			base := strings.TrimSuffix(path, filepath.Ext(path))
			subID := hashString(path)
			label := "Legenda"
			lang := "pt"
			if strings.Contains(strings.ToLower(d.Name()), "en") {
				label = "Inglês"
				lang = "en"
			} else if strings.Contains(strings.ToLower(d.Name()), "pt") {
				label = "Português"
				lang = "pt"
			}
			subMap[base] = append(subMap[base], SubtitleTrack{
				ID:    subID,
				Label: label,
				Lang:  lang,
				Path:  path,
			})
		}
		return nil
	})

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isVideoFile(d.Name()) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(dir, path)
		parts := strings.Split(rel, string(filepath.Separator))
		baseNoExt := strings.TrimSuffix(path, filepath.Ext(path))
		subs := subMap[baseNoExt]

		id := hashString(path)
		filename := d.Name()
		cleanName := strings.TrimSuffix(filename, filepath.Ext(filename))

		if len(parts) > 1 && (epRegex1.MatchString(filename) || strings.Contains(strings.ToLower(rel), "season") || strings.Contains(strings.ToLower(rel), "temporada") || strings.Contains(strings.ToLower(rel), "series")) {
			// Provavelmente é uma Série
			showTitle := parts[0]
			season, episode := parseEpisodeInfo(filename, parts[len(parts)-2])

			v := VideoMedia{
				ID:        id,
				Title:     cleanName,
				Parent:    showTitle,
				Season:    season,
				Episode:   episode,
				Type:      "episode",
				Path:      path,
				Size:      info.Size(),
				Subtitles: subs,
			}
			mediaMap[id] = v

			grp, exists := showsMap[showTitle]
			if !exists {
				grp = &VideoGroup{
					ID:       hashString(showTitle),
					Title:    showTitle,
					Type:     "show",
					Episodes: []VideoMedia{},
				}
				showsMap[showTitle] = grp
			}
			grp.Episodes = append(grp.Episodes, v)
		} else {
			// É um Filme avulso
			v := VideoMedia{
				ID:        id,
				Title:     cleanName,
				Type:      "movie",
				Path:      path,
				Size:      info.Size(),
				Subtitles: subs,
			}
			mediaMap[id] = v
			movies = append(movies, VideoGroup{
				ID:    id,
				Title: cleanName,
				Type:  "movie",
				Movie: &v,
			})
		}
		return nil
	})

	var result []VideoGroup
	// Ordenar episódios das séries
	for _, grp := range showsMap {
		sort.Slice(grp.Episodes, func(i, j int) bool {
			if grp.Episodes[i].Season != grp.Episodes[j].Season {
				return grp.Episodes[i].Season < grp.Episodes[j].Season
			}
			if grp.Episodes[i].Episode != grp.Episodes[j].Episode {
				return grp.Episodes[i].Episode < grp.Episodes[j].Episode
			}
			return naturalLess(grp.Episodes[i].Title, grp.Episodes[j].Title)
		})
		result = append(result, *grp)
	}

	result = append(result, movies...)
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title)
	})

	return result, mediaMap
}

// serveSubtitle converte SRT para VTT em voo se necessário
func serveSubtitle(w http.ResponseWriter, r *http.Request, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")

	if ext == ".vtt" {
		http.ServeFile(w, r, path)
		return
	}

	// Converte SRT simples para VTT
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Write([]byte("WEBVTT\n\n"))
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// SRT usa vírgula para milissegundos (00:00:20,000 --> 00:00:24,400)
		// VTT usa ponto (00:00:20.000 --> 00:00:24.400)
		if strings.Contains(line, "-->") {
			line = strings.ReplaceAll(line, ",", ".")
		}
		fmt.Fprintf(w, "%s\n", line)
	}
}
