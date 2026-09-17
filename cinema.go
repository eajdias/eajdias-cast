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
	Parent    string          `json:"parent,omitempty"` // Série / Anime
	Category  string          `json:"category"`         // "filme" | "serie" | "anime" | "video"
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
	Type     string       `json:"type"`     // "movie" | "show"
	Category string       `json:"category"` // "filme" | "serie" | "anime" | "video"
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

func detectCategory(relPath string) string {
	low := strings.ToLower(relPath)
	switch {
	case strings.Contains(low, "anime"):
		return "anime"
	case strings.Contains(low, "series") || strings.Contains(low, "serie"):
		return "serie"
	case strings.Contains(low, "filmes") || strings.Contains(low, "movie"):
		return "filme"
	case strings.Contains(low, "video"):
		return "video"
	default:
		return "filme"
	}
}

func scanCinema(dirs ...string) ([]VideoGroup, map[string]VideoMedia) {
	mediaMap := make(map[string]VideoMedia)
	showsMap := make(map[string]*VideoGroup)
	var movies []VideoGroup

	subMap := make(map[string][]SubtitleTrack)

	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}

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
			cat := detectCategory(rel)

			isSeriesLike := epRegex1.MatchString(filename) ||
				epRegex2.MatchString(filename) ||
				cat == "serie" || cat == "anime" ||
				strings.Contains(strings.ToLower(rel), "season") ||
				strings.Contains(strings.ToLower(rel), "temporada")

			if len(parts) > 1 && isSeriesLike {
				// Detectar nome da série / anime com precisão
				showTitle := parts[0]
				topFolder := strings.ToLower(parts[0])
				if (topFolder == "anime" || topFolder == "animes" || topFolder == "series" || topFolder == "serie") && len(parts) > 2 {
					showTitle = parts[1]
				}

				season, episode := parseEpisodeInfo(filename, parts[len(parts)-2])

				v := VideoMedia{
					ID:        id,
					Title:     cleanName,
					Parent:    showTitle,
					Category:  cat,
					Season:    season,
					Episode:   episode,
					Type:      "episode",
					Path:      path,
					Size:      info.Size(),
					Subtitles: subs,
				}
				mediaMap[id] = v

				grpKey := cat + ":" + showTitle
				grp, exists := showsMap[grpKey]
				if !exists {
					grp = &VideoGroup{
						ID:       hashString(grpKey),
						Title:    showTitle,
						Type:     "show",
						Category: cat,
						Episodes: []VideoMedia{},
					}
					showsMap[grpKey] = grp
				}
				grp.Episodes = append(grp.Episodes, v)
			} else {
				// Filme ou vídeo avulso
				v := VideoMedia{
					ID:        id,
					Title:     cleanName,
					Category:  cat,
					Type:      "movie",
					Path:      path,
					Size:      info.Size(),
					Subtitles: subs,
				}
				mediaMap[id] = v
				movies = append(movies, VideoGroup{
					ID:       id,
					Title:    cleanName,
					Type:     "movie",
					Category: cat,
					Movie:    &v,
				})
			}
			return nil
		})
	}

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
