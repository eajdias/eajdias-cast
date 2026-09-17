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

func scanManga(dir string) []MangaSeries {
	var series []MangaSeries
	entries, err := os.ReadDir(dir)
	if err != nil {
		return series
	}

	for _, entry := range entries {
		if !entry.IsDir() && !strings.HasSuffix(strings.ToLower(entry.Name()), ".cbz") && !strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
			continue
		}

		// Se for um diretório de mangá (ex: /data/manga/One Piece)
		if entry.IsDir() {
			mangaDir := filepath.Join(dir, entry.Name())
			chEntries, err := os.ReadDir(mangaDir)
			if err != nil {
				continue
			}

			manga := MangaSeries{
				ID:    hashString(entry.Name()),
				Title: entry.Name(),
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
					if count, err := countZipImages(chPath); err == nil && count > 0 {
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
				manga.CoverURL = fmt.Sprintf("/api/manga/page?ch=%s&p=0", chapters[0].ID)
				series = append(series, manga)
			}
		} else {
			// Arquivo CBZ solto na raiz do manga dir (ex: /data/manga/Berserk_Vol_01.cbz)
			filePath := filepath.Join(dir, entry.Name())
			if count, err := countZipImages(filePath); err == nil && count > 0 {
				cleanTitle := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				ch := MangaChapter{
					ID:        hashString(filePath),
					Title:     cleanTitle,
					Path:      filePath,
					IsArchive: true,
					PageCount: count,
				}
				manga := MangaSeries{
					ID:       hashString(entry.Name()),
					Title:    cleanTitle,
					CoverURL: fmt.Sprintf("/api/manga/page?ch=%s&p=0", ch.ID),
					Chapters: []MangaChapter{ch},
				}
				series = append(series, manga)
			}
		}
	}

	sort.Slice(series, func(i, j int) bool {
		return strings.ToLower(series[i].Title) < strings.ToLower(series[j].Title)
	})
	return series
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
