package main

import (
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
