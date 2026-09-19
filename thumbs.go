package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type thumbCall struct {
	done chan struct{}
}

func thumbKey(name string, width int) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:20] + "_" + strconv.Itoa(width) + ".jpg"
}

func thumbFingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return "|" + strconv.FormatInt(fi.ModTime().Unix(), 10) + "|" + strconv.FormatInt(fi.Size(), 10)
}

func (s *server) handleThumb(w http.ResponseWriter, r *http.Request) {
	mediaID := r.URL.Query().Get("id")
	if mediaID == "" {
		http.NotFound(w, r)
		return
	}
	s.rescanIfNeeded()
	s.mu.RLock()
	mediaPath := ""
	if v, ok := s.cinemaMap[mediaID]; ok {
		mediaPath = v.Path
	}
	if mediaPath == "" {
		for _, g := range s.cinemaItems {
			if g.ID != mediaID {
				continue
			}
			if g.coverPath != "" {
				mediaPath = g.coverPath
			} else if len(g.Episodes) > 0 {
				mediaPath = g.Episodes[0].Path
			} else if g.Movie != nil {
				mediaPath = g.Movie.Path
			}
			break
		}
	}
	s.mu.RUnlock()
	if mediaPath == "" {
		http.NotFound(w, r)
		return
	}

	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	if width < 64 {
		width = 360
	}
	if width > 1280 {
		width = 1280
	}

	// v3 invalida frames antigos; o fingerprint invalida o thumb quando o
	// arquivo muda no mesmo caminho (mediaID é hash do caminho).
	key := thumbKey("v3:"+mediaID+thumbFingerprint(mediaPath), width)
	dst := filepath.Join(s.cacheDir, "thumbs", key)

	if data, err := os.ReadFile(dst); err == nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
		return
	}

	s.tmu.Lock()
	if c, ok := s.inflight[key]; ok {
		s.tmu.Unlock()
		<-c.done
		if data, err := os.ReadFile(dst); err == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(data)
			return
		}
		http.NotFound(w, r)
		return
	}
	c := &thumbCall{done: make(chan struct{})}
	s.inflight[key] = c
	s.tmu.Unlock()

	// Limita a 2 ffmpeg/decoders concorrentes
	s.sem <- struct{}{}
	err := s.buildThumb(mediaPath, dst, width)
	<-s.sem

	s.tmu.Lock()
	delete(s.inflight, key)
	s.tmu.Unlock()
	close(c.done)

	if err != nil {
		log.Printf("[thumb] erro para %s: %v", mediaPath, err)
		http.NotFound(w, r)
		return
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (s *server) buildThumb(srcPath, dst string, width int) error {
	_ = os.MkdirAll(filepath.Dir(dst), 0755)
	tmp := dst + ".tmp"

	// Se for imagem direta
	if isImageFile(srcPath) {
		if f, err := os.Open(srcPath); err == nil {
			img, _, err := image.Decode(f)
			f.Close()
			if err == nil {
				out, err := os.Create(tmp)
				if err != nil {
					return err
				}
				if err := jpeg.Encode(out, scaleFit(img, width), &jpeg.Options{Quality: 82}); err != nil {
					out.Close()
					return err
				}
				out.Close()
				return os.Rename(tmp, dst)
			}
		}
	}

	// Frames iniciais costumam ser logos/tela preta; tenta uma cena já estabelecida.
	for _, ss := range []string{"30", "10", "5", "1"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y", "-v", "error",
			"-ss", ss, "-i", srcPath, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2:force_original_aspect_ratio=decrease", width),
			"-q:v", "4", "-f", "mjpeg", tmp)
		err := cmd.Run()
		cancel()
		if err == nil {
			if fi, err := os.Stat(tmp); err == nil && fi.Size() > 0 {
				return os.Rename(tmp, dst)
			}
		}
	}
	os.Remove(tmp)
	return fmt.Errorf("ffmpeg falhou para gerar thumb de %s", srcPath)
}

func scaleFit(src image.Image, maxW int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW {
		return src
	}
	nh := h * maxW / w
	dst := image.NewRGBA(image.Rect(0, 0, maxW, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}
