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
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// ffSlots dimensiona a concorrência do ffmpeg pelo hardware real:
// metade dos núcleos, entre 1 e 8. Num i7 de 8 threads dá 4; numa VPS de
// 1 vCPU dá 1. FF_SLOTS sobrescreve quando definido.
func ffSlots() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	if v, err := strconv.Atoi(os.Getenv("FF_SLOTS")); err == nil && v >= 1 && v <= 16 {
		n = v
	}
	return n
}

// vaapiDevice detecta GPU com VA-API pronta para decode: procura
// /dev/dri/renderD* e valida com um init real via ffmpeg. Sem driver
// funcional (caso atual: devices presentes mas libva sem backend), devolve
// "" e tudo segue em CPU. FF_VAAPI=0 força CPU; FF_VAAPI=/dev/dri/renderD129
// fixa o device.
func vaapiDevice() string {
	if os.Getenv("FF_VAAPI") == "0" {
		return ""
	}
	if d := os.Getenv("FF_VAAPI"); d != "" {
		if probeVAAPI(d) {
			return d
		}
		log.Printf("[ffmpeg] FF_VAAPI=%s sem VA-API funcional, usando CPU", d)
		return ""
	}
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "renderD") {
			continue
		}
		dev := filepath.Join("/dev/dri", e.Name())
		if probeVAAPI(dev) {
			log.Printf("[ffmpeg] VA-API ativa em %s", dev)
			return dev
		}
	}
	return ""
}

// warmWidths cobre o que o frontend pede (thumbW: 480 desktop, 720 DPR alto).
var warmWidths = []int{480, 720}

func probeVAAPI(dev string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Valida o pipeline real de thumbs (decode H.264 + scale_vaapi +
	// hwdownload). O probe usa o primeiro vídeo H.264/HEVC ≤1080p do cinema;
	// sem biblioteca ainda, só valida a criação do device (decoder testado
	// no primeiro thumb real, com fallback CPU por tentativa).
	probeSrc, probeInput := findProbeVideo()
	args := []string{"-nostdin", "-v", "error",
		"-hwaccel", "vaapi", "-hwaccel_device", dev, "-hwaccel_output_format", "vaapi",
		"-ss", "1"}
	args = append(args, probeInput...)
	args = append(args, probeSrc, "-frames:v", "1",
		"-vf", "scale_vaapi=64:-2,hwdownload,format=nv12",
		"-f", "null", "-")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if err := cmd.Run(); err != nil {
		log.Printf("[ffmpeg] VA-API sem decode funcional em %s, usando CPU", dev)
		return false
	}
	return true
}

// findProbeVideo devolve um vídeo real H.264/HEVC para o probe VA-API.
// Sem biblioteca ainda, devolve lavfi (valida só o device; o decoder é
// testado no primeiro thumb real, com fallback CPU por tentativa).
func findProbeVideo() (string, []string) {
	for _, dir := range probeMediaDirs() {
		var found string
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if found != "" || err != nil || d.IsDir() || !isVideoFile(d.Name()) {
				return nil
			}
			if codec, w, h := probeVideoStream(path); (codec == "h264" || codec == "hevc") && w <= 1920 && h <= 1080 {
				found = path
			}
			return nil
		})
		if found != "" {
			return found, []string{"-i"}
		}
	}
	return "color=c=black:s=64x64:d=1", []string{"-f", "lavfi", "-i"}
}

// probeMediaDirs resolve os diretórios de cinema sem depender do server
// (o probe roda antes do primeiro scan).
func probeMediaDirs() []string {
	if v := os.Getenv("CINEMA_DIR"); v != "" {
		return []string{v}
	}
	media := os.Getenv("MEDIA_DIR")
	if media == "" {
		media = "/data/media"
	}
	var dirs []string
	for _, cand := range []string{"filmes", "series", "anime", "videos", "cinema", "movies", "tv"} {
		if p := filepath.Join(media, cand); dirExists(p) {
			dirs = append(dirs, p)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{filepath.Join(media, "cinema")}
	}
	return dirs
}

// probeVideoStream devolve codec/largura/altura do stream de vídeo.
func probeVideoStream(path string) (string, int, int) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height",
		"-of", "csv=p=0", path)
	out, err := cmd.Output()
	if err != nil {
		return "", 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 3 {
		return "", 0, 0
	}
	w, _ := strconv.Atoi(parts[1])
	h, _ := strconv.Atoi(parts[2])
	return parts[0], w, h
}

type thumbJob struct{ id, path string }

// warmThumbs gera em background no boot os thumbs ainda ausentes, para a
// grade abrir pronta. Remux de áudio fica sob demanda de propósito: cada
// faixa custa ~o tamanho do filme em disco.
// Roda com s.sem (ffSlots()), então nunca degrada thumbs sob demanda.
func (s *server) warmThumbs() {
	s.rescanIfNeeded()
	for _, w := range warmWidths {
		n := s.generateThumbs(w)
		log.Printf("[warm] %d thumbs w=%d", n, w)
	}
}

func (s *server) generateThumbs(width int) int {
	s.mu.RLock()
	var jobs []thumbJob
	for id, v := range s.cinemaMap {
		jobs = append(jobs, thumbJob{id, v.Path})
	}
	for _, g := range s.cinemaItems {
		p := g.coverPath
		if p == "" {
			if len(g.Episodes) > 0 {
				p = g.Episodes[0].Path
			} else if g.Movie != nil {
				p = g.Movie.Path
			}
		}
		if p != "" {
			jobs = append(jobs, thumbJob{g.ID, p})
		}
	}
	s.mu.RUnlock()

	warmed := 0
	for _, j := range jobs {
		key := thumbKey("v4:"+j.id+thumbFingerprint(j.path), width)
		dst := filepath.Join(s.cacheDir, "thumbs", key)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		s.sem <- struct{}{}
		err := s.buildThumb(j.path, dst, width)
		<-s.sem
		if err != nil {
			log.Printf("[warm] %s: %v", j.path, err)
			continue
		}
		warmed++
	}
	return warmed
}

func frameBrightness(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, false
	}
	b := img.Bounds()
	var sum, n uint64
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += uint64(r>>8) + uint64(g>>8) + uint64(bl>>8)
			n += 3
		}
	}
	if n == 0 {
		return 0, false
	}
	return float64(sum) / float64(n), true
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

	// v4 invalida thumbs antigos (inclui pretos aceitos pelo seletor anterior);
	// o fingerprint invalida o thumb quando o arquivo muda no mesmo caminho
	// (mediaID é hash do caminho).
	key := thumbKey("v4:"+mediaID+thumbFingerprint(mediaPath), width)
	dst := filepath.Join(s.cacheDir, "thumbs", key)

	if data, err := os.ReadFile(dst); err == nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
		return
	}

	s.tmu.Lock()
	if c, ok := s.inflight[key]; ok {
		s.tmu.Unlock()
		<-c.done
		if data, err := os.ReadFile(dst); err == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(data)
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
	_, _ = w.Write(data)
}

func (s *server) buildThumb(srcPath, dst string, width int) error {
	_ = os.MkdirAll(filepath.Dir(dst), 0755)
	tmp := dst + ".tmp"

	// Teto 1080p: thumbs nunca precisam de mais que a largura pedida;
	// o width já vem limitado a 1280 no handleThumb.
	if width > 960 {
		width = 960
	}

	// Se for imagem direta
	if isImageFile(srcPath) {
		if f, err := os.Open(srcPath); err == nil {
			img, _, err := image.Decode(f)
			_ = f.Close()
			if err == nil {
				out, err := os.Create(tmp)
				if err != nil {
					return err
				}
				if err := jpeg.Encode(out, scaleFit(img, width), &jpeg.Options{Quality: 82}); err != nil {
					_ = out.Close()
					return err
				}
				_ = out.Close()
				return os.Rename(tmp, dst)
			}
		}
	}

	// Fast-seek costuma cair em fade/tela preta mesmo com saída OK do
	// ffmpeg; avalia o brilho e fica com o primeiro frame não-preto.
	best, bestLuma := "", -1.0
	// Com VA-API o decode vai pra GPU e o scale usa scale_vaapi; sem GPU,
	// caminho CPU de antes. A saída volta para mjpeg em disco (CPU lê de
	// volta via hwdownload quando usa VA-API). Se o caminho GPU falhar
	// (codec sem suporte, ex. AV1 na Polaris), cai para CPU na mesma
	// tentativa — thumb nunca se perde por causa da GPU.
	va := s.vaapiDev
	for _, ss := range []string{"30", "10", "5", "1"} {
		tmpSS := tmp + "." + ss
		if img := tryThumbAttempt(srcPath, tmpSS, width, ss, va); img != "" {
			luma, ok := frameBrightness(img)
			if !ok {
				_ = os.Remove(img)
				continue
			}
			if luma > bestLuma {
				if best != "" {
					_ = os.Remove(best)
				}
				best, bestLuma = img, luma
			} else {
				_ = os.Remove(img)
			}
			if bestLuma >= 12 {
				break
			}
			continue
		}
		if va != "" {
			log.Printf("[thumb] VA-API falhou para %s, tentando CPU", srcPath)
			va = ""
			if img := tryThumbAttempt(srcPath, tmpSS, width, ss, ""); img != "" {
				luma, ok := frameBrightness(img)
				if !ok {
					_ = os.Remove(img)
					continue
				}
				if luma > bestLuma {
					if best != "" {
						_ = os.Remove(best)
					}
					best, bestLuma = img, luma
				} else {
					_ = os.Remove(img)
				}
				if bestLuma >= 12 {
					break
				}
			}
			continue
		}
	}
	if best == "" {
		return fmt.Errorf("ffmpeg falhou para gerar thumb de %s", srcPath)
	}
	return os.Rename(best, dst)
}

// tryThumbAttempt extrai um frame via GPU (va != "") ou CPU, devolvendo o
// caminho do arquivo gerado ou "" em falha.
func tryThumbAttempt(srcPath, tmpSS string, width int, ss, va string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if va != "" {
		cmd = exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y", "-v", "error",
			"-hwaccel", "vaapi", "-hwaccel_device", va, "-hwaccel_output_format", "vaapi",
			"-ss", ss, "-i", srcPath, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale_vaapi=%d:-2,hwdownload,format=nv12", width),
			"-q:v", "4", "-f", "mjpeg", tmpSS)
	} else {
		cmd = exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y", "-v", "error",
			"-ss", ss, "-i", srcPath, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:-2:force_original_aspect_ratio=decrease", width),
			"-q:v", "4", "-f", "mjpeg", tmpSS)
	}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpSS)
		return ""
	}
	fi, err := os.Stat(tmpSS)
	if err != nil || fi.Size() == 0 {
		_ = os.Remove(tmpSS)
		return ""
	}
	return tmpSS
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
