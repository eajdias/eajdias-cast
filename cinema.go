package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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
	Path        string // legenda externa (.srt/.vtt); vazio = embutida no vídeo
	MediaPath   string // arquivo de vídeo, para extrair legenda embutida
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

type probeData struct {
	Audio []AudioTrack    `json:"audio"`
	Subs  []SubtitleTrack `json:"subs"`
}

func cachedProbeTracks(cache *dbStore, path string, mtime, size int64) ([]AudioTrack, []SubtitleTrack) {
	if raw, ok := cache.getMediaCache(path, mtime, size, "probe"); ok {
		var pd probeData
		if json.Unmarshal([]byte(raw), &pd) == nil {
			return pd.Audio, pd.Subs
		}
	}
	audio, subs := probeTracks(path)
	if raw, err := json.Marshal(probeData{Audio: audio, Subs: subs}); err == nil {
		cache.setMediaCache(path, mtime, size, "probe", string(raw))
	}
	return audio, subs
}

type VideoGroup struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Type      string   `json:"type"`     // "movie" | "show"
	Category  string   `json:"category"` // "filme" | "serie" | "anime" | "video"
	CoverURL  string   `json:"cover_url,omitempty"`
	Tags      []string `json:"tags,omitempty"`
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
	epRegex1 = regexp.MustCompile(`(?i)[sS]([0-9]{1,2})[eE]([0-9]{1,2})`)
	// O prefixo (^|separador) impede que o "e" de "Dune 2" ou "Se7en"
	// seja lido como marcador de episódio. RE2 não tem lookbehind,
	// por isso o separador faz parte do match (só o grupo 1 importa).
	epRegex2      = regexp.MustCompile(`(?i)(?:^|[\s._\-\[\(])(?:ep|episodio|episode|e)[_.\s-]*([0-9]{1,3})`)
	seasonRegex   = regexp.MustCompile(`(?i)(?:^|[\s._\-\[\(])(?:temporada|season|s)[_.\s-]*([0-9]{1,2})`)
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

func scanCinema(cache *dbStore, dirs ...string) ([]VideoGroup, map[string]VideoMedia) {
	mediaMap := make(map[string]VideoMedia)
	showsMap := make(map[string]*VideoGroup)
	var movies []VideoGroup

	subMap := make(map[string][]SubtitleTrack)
	seen := make(map[string]struct{})

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
			seen[path] = struct{}{}
			audioTracks, embeddedSubs := cachedProbeTracks(cache, path, info.ModTime().UnixNano(), info.Size())
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

	cache.pruneMediaCache(seen, "probe")
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
	cmd := exec.CommandContext(r.Context(), "ffmpeg", "-nostdin", "-v", "error", "-i", sub.MediaPath, "-map", "0:"+strconv.Itoa(sub.StreamIndex), "-f", "webvtt", "pipe:1")
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		http.Error(w, "Não foi possível converter a legenda", http.StatusInternalServerError)
	}
}

// A troca de faixa remuxa uma vez para arquivo em cache e serve via
// ServeContent: duração e seek funcionam. O pipe fragmentado anterior não
// tinha duração nem Range, quebrando tempo total e seek no player.
func (s *server) serveSelectedAudio(w http.ResponseWriter, r *http.Request, v VideoMedia, audio int) {
	if audio < 0 || audio >= len(v.AudioTracks) {
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(s.cacheDir, "audio")
	_ = os.MkdirAll(dir, 0755)
	dst := s.audioCachePath(v, audio)

	if _, err := os.Stat(dst); err != nil {
		s.amu.Lock()
		if ch, ok := s.audioInflight[dst]; ok {
			s.amu.Unlock()
			<-ch
		} else {
			ch := make(chan struct{})
			s.audioInflight[dst] = ch
			s.amu.Unlock()

			s.sem <- struct{}{}
			err := s.buildAudioRemux(v, audio, dst)
			<-s.sem

			s.amu.Lock()
			delete(s.audioInflight, dst)
			s.amu.Unlock()
			close(ch)
			if err != nil {
				log.Printf("[audio] %v", err)
				http.Error(w, "Não foi possível converter o áudio", http.StatusInternalServerError)
				return
			}
		}
	}

	f, err := os.Open(dst)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, _ := f.Stat()
	cleanupAudioCache(s.cacheDir, audioCacheMaxBytes)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, filepath.Base(dst), fi.ModTime(), f)
}

func (s *server) audioCachePath(v VideoMedia, audio int) string {
	return filepath.Join(s.cacheDir, "audio", "audio_"+v.ID+"_"+strconv.Itoa(audio)+"_"+thumbFingerprint(v.Path)+".mp4")
}

func audioCopyCodecs(codec string) bool {
	switch strings.ToLower(codec) {
	case "aac", "mp3", "opus":
		return true
	}
	return false
}

func (s *server) buildAudioRemux(v VideoMedia, audio int, dst string) error {
	tmp := dst + ".tmp"
	acodec := []string{"-c:a", "aac"}
	if audioCopyCodecs(v.AudioTracks[audio].Codec) {
		acodec = []string{"-c:a", "copy"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y", "-v", "error",
		"-i", v.Path,
		"-map", "0:v:0", "-map", "0:a:"+strconv.Itoa(audio),
		"-c:v", "copy", acodec[0], acodec[1],
		"-movflags", "faststart", "-f", "mp4", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("remux %s faixa %d: %v %.200s", v.Path, audio, err, out)
	}
	return os.Rename(tmp, dst)
}

const audioCacheMaxBytes = 10 << 30 // 10GB

func cleanupAudioCache(cacheDir string, maxBytes int64) {
	dir := filepath.Join(cacheDir, "audio")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cached struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []cached
	var total int64
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(p) // resto de conversão interrompida
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.IsDir() {
			continue
		}
		files = append(files, cached{p, fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
}
