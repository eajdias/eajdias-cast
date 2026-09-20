package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type MangaChapter struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	IsArchive bool   `json:"is_archive"`
	PageCount int    `json:"page_count"`
}

type MangaSeries struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	CoverURL string         `json:"cover_url"`
	Chapters []MangaChapter `json:"chapters"`
}

// naturalCompare compara strings de forma que "Cap 2" venha antes de "Cap 10"
func naturalLess(s1, s2 string) bool {
	runes1, runes2 := []rune(s1), []rune(s2)
	i, j := 0, 0
	for i < len(runes1) && j < len(runes2) {
		r1, r2 := runes1[i], runes2[j]
		if unicode.IsDigit(r1) && unicode.IsDigit(r2) {
			num1Start := i
			for i < len(runes1) && unicode.IsDigit(runes1[i]) {
				i++
			}
			num2Start := j
			for j < len(runes2) && unicode.IsDigit(runes2[j]) {
				j++
			}
			val1, _ := strconv.ParseInt(string(runes1[num1Start:i]), 10, 64)
			val2, _ := strconv.ParseInt(string(runes2[num2Start:j]), 10, 64)
			if val1 != val2 {
				return val1 < val2
			}
		} else {
			diff := unicode.ToLower(r1) - unicode.ToLower(r2)
			if diff != 0 {
				return diff < 0
			}
			i++
			j++
		}
	}
	return len(runes1) < len(runes2)
}

func isImageFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" || ext == ".avif"
}

func normalizeMediaName(name string) string {
	name = strings.ToLower(strings.NewReplacer(
		"á", "a", "à", "a", "ã", "a", "â", "a", "ä", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "õ", "o", "ô", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u", "ç", "c",
	).Replace(name))
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func findMangaCover(baseDir, title string, indexed map[string]string) string {
	if c := indexed[normalizeMediaName(title)]; c != "" {
		return c
	}
	candidates := []string{
		filepath.Join(baseDir, "thumbnails", title+".jpg"),
		filepath.Join(baseDir, "thumbnails", title+".png"),
		filepath.Join(baseDir, "thumbnails", title+".webp"),
		filepath.Join(baseDir, title, "cover.jpg"),
		filepath.Join(baseDir, title, "cover.png"),
		filepath.Join(baseDir, title, "folder.jpg"),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

func indexMangaCovers(baseDir string) map[string]string {
	result := make(map[string]string)
	for _, folder := range []string{"thumbnails", "covers"} {
		entries, err := os.ReadDir(filepath.Join(baseDir, folder))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isImageFile(e.Name()) {
				continue
			}
			key := normalizeMediaName(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
			if key != "" {
				result[key] = filepath.Join(baseDir, folder, e.Name())
			}
		}
	}
	return result
}

func scanManga(cache *dbStore, dir string) ([]MangaSeries, map[string]string) {
	var series []MangaSeries
	covers := make(map[string]string)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return series, covers
	}
	coverIndex := indexMangaCovers(dir)
	seen := make(map[string]struct{})

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "thumbnails") || strings.EqualFold(name, "covers") {
			continue
		}
		if !entry.IsDir() && !strings.HasSuffix(strings.ToLower(name), ".cbz") && !strings.HasSuffix(strings.ToLower(name), ".zip") {
			continue
		}

		// Se for um diretório de mangá (ex: /data/manga/One Piece)
		if entry.IsDir() {
			mangaDir := filepath.Join(dir, name)
			chEntries, err := os.ReadDir(mangaDir)
			if err != nil {
				continue
			}

			mangaID := hashString(name)
			manga := MangaSeries{
				ID:    mangaID,
				Title: name,
			}

			var chapters []MangaChapter
			for _, ch := range chEntries {
				chPath := filepath.Join(mangaDir, ch.Name())
				chExt := strings.ToLower(filepath.Ext(ch.Name()))

				if ch.IsDir() {
					// Pasta contendo páginas
					pages, err := os.ReadDir(chPath)
					if err == nil {
						pCount := 0
						for _, p := range pages {
							if !p.IsDir() && isImageFile(p.Name()) {
								pCount++
							}
						}
						if pCount > 0 {
							chapters = append(chapters, MangaChapter{
								ID:        hashString(chPath),
								Title:     ch.Name(),
								Path:      chPath,
								IsArchive: false,
								PageCount: pCount,
							})
						}
					}
				} else if chExt == ".cbz" || chExt == ".zip" {
					// Arquivo CBZ/ZIP
					seen[chPath] = struct{}{}
					if count, err := cachedZipCount(cache, chPath); err == nil && count > 0 {
						cleanTitle := strings.TrimSuffix(ch.Name(), filepath.Ext(ch.Name()))
						chapters = append(chapters, MangaChapter{
							ID:        hashString(chPath),
							Title:     cleanTitle,
							Path:      chPath,
							IsArchive: true,
							PageCount: count,
						})
					}
				}
			}

			if len(chapters) > 0 {
				sort.Slice(chapters, func(i, j int) bool {
					return naturalLess(chapters[i].Title, chapters[j].Title)
				})
				manga.Chapters = chapters

				// Verificar se tem capa própria (ex: manga/thumbnails/{Nome}.jpg)
				if cov := findMangaCover(dir, name, coverIndex); cov != "" {
					covers[mangaID] = cov
					manga.CoverURL = fmt.Sprintf("/api/manga/cover?id=%s", mangaID)
				} else {
					manga.CoverURL = fmt.Sprintf("/api/manga/page?ch=%s&p=0", chapters[0].ID)
				}

				series = append(series, manga)
			}
		} else {
			// Arquivo CBZ solto na raiz do manga dir (ex: /data/manga/Berserk_Vol_01.cbz)
			filePath := filepath.Join(dir, name)
			seen[filePath] = struct{}{}
			if count, err := cachedZipCount(cache, filePath); err == nil && count > 0 {
				cleanTitle := strings.TrimSuffix(name, filepath.Ext(name))
				mangaID := hashString(name)
				ch := MangaChapter{
					ID:        hashString(filePath),
					Title:     cleanTitle,
					Path:      filePath,
					IsArchive: true,
					PageCount: count,
				}
				manga := MangaSeries{
					ID:       mangaID,
					Title:    cleanTitle,
					Chapters: []MangaChapter{ch},
				}

				if cov := findMangaCover(dir, cleanTitle, coverIndex); cov != "" {
					covers[mangaID] = cov
					manga.CoverURL = fmt.Sprintf("/api/manga/cover?id=%s", mangaID)
				} else {
					manga.CoverURL = fmt.Sprintf("/api/manga/page?ch=%s&p=0", ch.ID)
				}

				series = append(series, manga)
			}
		}
	}

	sort.Slice(series, func(i, j int) bool {
		return strings.ToLower(series[i].Title) < strings.ToLower(series[j].Title)
	})
	cache.pruneMediaCache(seen, "zip")
	return series, covers
}

func countZipImages(path string) (int, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	count := 0
	for _, f := range r.File {
		if !f.FileInfo().IsDir() && isImageFile(f.Name) && !strings.HasPrefix(filepath.Base(f.Name), ".") {
			count++
		}
	}
	return count, nil
}

func cachedZipCount(cache *dbStore, path string) (int, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	mt, sz := fi.ModTime().UnixNano(), fi.Size()
	if raw, ok := cache.getMediaCache(path, mt, sz, "zip"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			return n, nil
		}
	}
	n, err := countZipImages(path)
	if err == nil {
		cache.setMediaCache(path, mt, sz, "zip", strconv.Itoa(n))
	}
	return n, err
}

func (s *server) serveMangaPage(w http.ResponseWriter, r *http.Request, ch MangaChapter, pageIdx int) {
	if ch.IsArchive {
		zr, err := zip.OpenReader(ch.Path)
		if err != nil {
			http.Error(w, "Erro ao abrir CBZ/ZIP", http.StatusInternalServerError)
			return
		}
		defer zr.Close()

		var imgFiles []*zip.File
		for _, f := range zr.File {
			if !f.FileInfo().IsDir() && isImageFile(f.Name) && !strings.HasPrefix(filepath.Base(f.Name), ".") {
				imgFiles = append(imgFiles, f)
			}
		}

		sort.Slice(imgFiles, func(i, j int) bool {
			return naturalLess(imgFiles[i].Name, imgFiles[j].Name)
		})

		if pageIdx < 0 || pageIdx >= len(imgFiles) {
			http.NotFound(w, r)
			return
		}

		target := imgFiles[pageIdx]
		rc, err := target.Open()
		if err != nil {
			http.Error(w, "Erro ao ler página", http.StatusInternalServerError)
			return
		}
		defer rc.Close()

		ext := strings.ToLower(filepath.Ext(target.Name))
		mime := "image/jpeg"
		switch ext {
		case ".png":
			mime = "image/png"
		case ".webp":
			mime = "image/webp"
		case ".gif":
			mime = "image/gif"
		case ".avif":
			mime = "image/avif"
		}

		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		io.Copy(w, rc)
		return
	}

	// Caso de pasta no disco
	entries, err := os.ReadDir(ch.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var imgNames []string
	for _, e := range entries {
		if !e.IsDir() && isImageFile(e.Name()) && !strings.HasPrefix(e.Name(), ".") {
			imgNames = append(imgNames, e.Name())
		}
	}

	sort.Slice(imgNames, func(i, j int) bool {
		return naturalLess(imgNames[i], imgNames[j])
	})

	if pageIdx < 0 || pageIdx >= len(imgNames) {
		http.NotFound(w, r)
		return
	}

	pagePath := filepath.Join(ch.Path, imgNames[pageIdx])
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, pagePath)
}

func (s *server) serveMangaCover(w http.ResponseWriter, r *http.Request, coverPath string) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, coverPath)
}
