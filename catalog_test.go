package main

import (
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCategoryUsesMediaRoot(t *testing.T) {
	cases := []struct{ root, want string }{
		{"/data/media/filmes", "filme"},
		{"/data/media/series", "serie"},
		{"/data/media/anime", "anime"},
		{"/data/media/videos", "video"},
	}
	for _, tc := range cases {
		if got := detectCategory(tc.root, "Obra/S01E01.mkv"); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.root, got, tc.want)
		}
	}
}

func TestNormalizeMediaName(t *testing.T) {
	if got, want := normalizeMediaName("Boruto_ Naruto Next Generations"), "borutonarutonextgenerations"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDisplayVideoTitle(t *testing.T) {
	if got, want := displayVideoTitle("Avatar.Aang.2026.1080p.WEB-DL.x264.DUAL"), "Avatar Aang 2026"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMoviesWithEDigitNamesStayMovies(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Dune 2", "Se7en"} {
		dir := filepath.Join(root, d)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, d+".mp4"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	groups, _ := scanCinema(root)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	for _, g := range groups {
		if g.Type != "movie" {
			t.Errorf("%q: got type %q, want %q", g.Title, g.Type, "movie")
		}
	}
}

func TestEpisodeMarkersStillDetected(t *testing.T) {
	if s, e := parseEpisodeInfo("Show - S02E05.mkv", "Show"); s != 2 || e != 5 {
		t.Fatalf("S02E05: got (%d,%d), want (2,5)", s, e)
	}
	if _, e := parseEpisodeInfo("Show - E05.mp4", "Show"); e != 5 {
		t.Fatalf("E05: got episode %d, want 5", e)
	}
	if _, e := parseEpisodeInfo("Dune 2.mp4", "Dune 2"); e != 0 {
		t.Fatalf("Dune 2: got episode %d, want 0", e)
	}
	if s, _ := parseEpisodeInfo("Show - E05.mp4", "Temporada 01"); s != 1 {
		t.Fatalf("Temporada 01: got season %d, want 1", s)
	}
}

func TestEmptyListsEncodeAsArrays(t *testing.T) {
	s := &server{}
	cases := map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/cinema": s.handleCinemaList,
		"/api/manga":  s.handleMangaList,
		"/api/music":  s.handleMusicList,
	}
	for path, fn := range cases {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest("GET", path, nil))
		if body := strings.TrimSpace(rec.Body.String()); body == "null" {
			t.Errorf("%s: encodes null, want []", path)
		}
	}
}

func TestSaveProgressCompletedSticks(t *testing.T) {
	s := initDB(t.TempDir())
	if err := s.SaveProgress("m1", 100, 100, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveProgress("m1", 5, 100, false); err != nil {
		t.Fatal(err)
	}
	rec, ok := s.GetProgress("m1")
	if !ok || !rec.Completed {
		t.Fatalf("completed was clobbered: %+v ok=%v", rec, ok)
	}
}

func TestDeleteProgress(t *testing.T) {
	s := initDB(t.TempDir())
	if err := s.SaveProgress("m1", 10, 100, false); err != nil {
		t.Fatal(err)
	}
	s.DeleteProgress("m1")
	if _, ok := s.GetProgress("m1"); ok {
		t.Fatal("progress still present after delete")
	}
}

func TestFrameBrightnessBlackVsWhite(t *testing.T) {
	write := func(c uint8) string {
		img := image.NewRGBA(image.Rect(0, 0, 32, 32))
		for i := range img.Pix {
			img.Pix[i] = c
		}
		p := filepath.Join(t.TempDir(), string([]byte{'f', '0' + c/255})+".jpg")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := jpeg.Encode(f, img, nil); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	black, ok := frameBrightness(write(0))
	if !ok || black >= 12 {
		t.Fatalf("black frame luma=%v ok=%v, want <12", black, ok)
	}
	white, ok := frameBrightness(write(255))
	if !ok || white < 200 {
		t.Fatalf("white frame luma=%v ok=%v, want >200", white, ok)
	}
}
