package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type SubtitleTrack struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Lang        string `json:"lang"`
	StreamIndex int    `json:"stream_index,omitempty"`
	Path        string `json:"-"`
}

type AudioTrack struct {
	Index int    `json:"index"`
	Label string `json:"label"`
	Lang  string `json:"lang"`
	Codec string `json:"codec"`
}

type subtitleRef struct {
	Path        string
	StreamIndex int
}

type VideoMedia struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Parent      string          `json:"parent,omitempty"` // Série / Anime
	Category    string          `json:"category"`         // "filme" | "serie" | "anime" | "video"
	Season      int             `json:"season,omitempty"`
	Episode     int             `json:"episode,omitempty"`
	Type        string          `json:"type"` // "movie" | "episode"
	Path        string          `json:"path"`
	Size        int64           `json:"size"`
	Duration    float64         `json:"duration,omitempty"`
	Subtitles   []SubtitleTrack `json:"subtitles,omitempty"`
	AudioTracks []AudioTrack    `json:"audio_tracks,omitempty"`
}

func probeTracks(path string) ([]AudioTrack, []SubtitleTrack) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=index,codec_type,codec_name:stream_tags=language,title", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	var probe struct {
		Streams []struct {
			Index int    `json:"index"`
			Type  string `json:"codec_type"`
			Codec string `json:"codec_name"`
			Tags  struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &probe) != nil {
		return nil, nil
	}
	var audio []AudioTrack
	var subs []SubtitleTrack
	for _, stream := range probe.Streams {
		lang := stream.Tags.Language
		if lang == "" {
			lang = "und"
		}
		label := stream.Tags.Title
		if label == "" {
			label = strings.ToUpper(lang)
		}
		switch stream.Type {
		case "audio":
			audio = append(audio, AudioTrack{Index: len(audio), Label: label, Lang: lang, Codec: stream.Codec})
		case "subtitle":
			subs = append(subs, SubtitleTrack{ID: hashString(path + ":" + strconv.Itoa(stream.Index)), Label: label, Lang: lang, StreamIndex: stream.Index})
		}
	}
	return audio, subs
}

type VideoGroup struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Type      string `json:"type"`     // "movie" | "show"
	Category  string `json:"category"` // "filme" | "serie" | "anime" | "video"
	CoverURL  string `json:"cover_url,omitempty"`
	coverPath string
	Episodes  []VideoMedia `json:"episodes,omitempty"`
	Movie     *VideoMedia  `json:"movie,omitempty"`
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
	epRegex1      = regexp.MustCompile(`(?i)[sS]([0-9]{1,2})[eE]([0-9]{1,2})`)
	epRegex2      = regexp.MustCompile(`(?i)(?:ep|episodio|episode|e)[_.\s-]*([0-9]{1,3})`)
	seasonRegex   = regexp.MustCompile(`(?i)(?:temporada|season|s)[_.\s-]*([0-9]{1,2})`)
	releaseSuffix = regexp.MustCompile(`(?i)[. _-]+(?:s\d{1,2}e\d{1,3}|\d{3,4}p|web[ ._-]?dl|webrip|bluray|brrip|x26[45]|h\.?(?:264|265)|hevc|avc|dual|aac|dts|ddp|proper|repack|remux|10bit|5\.1|2\.0).*`)
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

func displayVideoTitle(name string) string {
	name = releaseSuffix.ReplaceAllString(name, "")
	name = strings.ReplaceAll(name, ".", " ")
	name = strings.ReplaceAll(name, "_", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(name), " "))
}

func detectCategory(root, relPath string) string {
	low := strings.ToLower(filepath.Base(root) + "/" + relPath)
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

func coverInDir(dir string) string {
	for _, name := range []string{"poster.jpg", "poster.png", "poster.webp", "cover.jpg", "cover.png", "folder.jpg", "folder.png"} {
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path
		}
	}
	return ""
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
			audioTracks, embeddedSubs := probeTracks(path)
			subs = append(subs, embeddedSubs...)

			id := hashString(path)
			filename := d.Name()
			cleanName := displayVideoTitle(strings.TrimSuffix(filename, filepath.Ext(filename)))
			cat := detectCategory(dir, rel)

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
					ID:          id,
					Title:       cleanName,
					Parent:      showTitle,
					Category:    cat,
					Season:      season,
					Episode:     episode,
					Type:        "episode",
					Path:        path,
					Size:        info.Size(),
					Subtitles:   subs,
					AudioTracks: audioTracks,
				}
				mediaMap[id] = v

				grpKey := cat + ":" + showTitle
				grp, exists := showsMap[grpKey]
				if !exists {
					showDir := filepath.Join(dir, parts[0])
					if (topFolder == "anime" || topFolder == "animes" || topFolder == "series" || topFolder == "serie") && len(parts) > 2 {
						showDir = filepath.Join(showDir, parts[1])
					}
					grp = &VideoGroup{
						ID:        hashString(grpKey),
						Title:     showTitle,
						Type:      "show",
						Category:  cat,
						Episodes:  []VideoMedia{},
						coverPath: coverInDir(showDir),
					}
					showsMap[grpKey] = grp
				}
				grp.Episodes = append(grp.Episodes, v)
			} else {
				// Filme ou vídeo avulso
				v := VideoMedia{
					ID:          id,
					Title:       cleanName,
					Category:    cat,
					Type:        "movie",
					Path:        path,
					Size:        info.Size(),
					Subtitles:   subs,
					AudioTracks: audioTracks,
				}
				mediaMap[id] = v
				movies = append(movies, VideoGroup{
					ID:        id,
					Title:     cleanName,
					Type:      "movie",
					Category:  cat,
					Movie:     &v,
					coverPath: coverInDir(filepath.Dir(path)),
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

func serveEmbeddedSubtitle(w http.ResponseWriter, r *http.Request, sub subtitleRef) {
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	cmd := exec.CommandContext(r.Context(), "ffmpeg", "-nostdin", "-v", "error", "-i", sub.Path, "-map", "0:"+strconv.Itoa(sub.StreamIndex), "-f", "webvtt", "pipe:1")
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		http.Error(w, "Não foi possível converter a legenda", http.StatusInternalServerError)
	}
}

// A troca de faixa produz MP4 fragmentado sob demanda: direct-play continua
// como padrão e a conversão só é usada para a faixa escolhida pelo usuário.
func (s *server) serveSelectedAudio(w http.ResponseWriter, r *http.Request, path string, audio int) {
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	cmd := exec.CommandContext(r.Context(), "ffmpeg", "-nostdin", "-v", "error", "-i", path,
		"-map", "0:v:0", "-map", "0:a:"+strconv.Itoa(audio), "-c:v", "copy", "-c:a", "aac",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "[audio] %v\n", err)
	}
}
