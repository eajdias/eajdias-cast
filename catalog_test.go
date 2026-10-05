package main

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func jsonDecodeBody(rec *httptest.ResponseRecorder, v any) error {
	return json.NewDecoder(rec.Body).Decode(v)
}

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
	groups, _ := scanCinema(nil, root)
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

func TestEmbeddedSubtitleKeepsVideoPath(t *testing.T) {
	media := map[string]VideoMedia{
		"v1": {
			ID:   "v1",
			Path: "/data/media/filmes/Filme.mkv",
			Subtitles: []SubtitleTrack{
				{ID: "emb", Label: "POR", Lang: "por", StreamIndex: 3},
				{ID: "ext", Label: "Legenda", Lang: "pt", Path: "/data/media/filmes/Filme.srt"},
			},
		},
	}
	got := indexSubtitles(media)
	if got["emb"].Path != "" || got["emb"].MediaPath != "/data/media/filmes/Filme.mkv" || got["emb"].StreamIndex != 3 {
		t.Fatalf("embedded: %+v", got["emb"])
	}
	if got["ext"].Path != "/data/media/filmes/Filme.srt" {
		t.Fatalf("external overwritten: %+v", got["ext"])
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
		_ = f.Close()
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

func TestTagsRoundtrip(t *testing.T) {
	s := &server{db: initDB(t.TempDir())}

	add := func(tag string) []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/tags", strings.NewReader(`{"media_id":"m1","tag":"`+tag+`"}`))
		s.handleTagsAdd(rec, req)
		var d struct {
			Tags []string `json:"tags"`
		}
		if err := jsonDecodeBody(rec, &d); err != nil {
			t.Fatal(err)
		}
		return d.Tags
	}

	if got := add("Ação"); len(got) != 1 || got[0] != "ação" {
		t.Fatalf("add: got %v, want [ação]", got)
	}
	if got := add("Ação"); len(got) != 1 {
		t.Fatalf("re-add must be idempotent: %v", got)
	}
	if got := add("  Ficção Top "); len(got) != 2 || got[1] != "ficção_top" {
		t.Fatalf("normalize: %v", got)
	}

	rec := httptest.NewRecorder()
	s.handleTagsList(rec, httptest.NewRequest("GET", "/api/tags?media_id=m1", nil))
	var list struct {
		Tags []string `json:"tags"`
	}
	if err := jsonDecodeBody(rec, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tags) != 2 {
		t.Fatalf("list: %v", list.Tags)
	}

	rec = httptest.NewRecorder()
	s.handleTagsDelete(rec, httptest.NewRequest("DELETE", "/api/tags?media_id=m1&tag=a%C3%A7%C3%A3o", nil))
	var after struct {
		Tags []string `json:"tags"`
	}
	if err := jsonDecodeBody(rec, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Tags) != 1 || after.Tags[0] != "ficção_top" {
		t.Fatalf("delete: %v", after.Tags)
	}
}

func TestAudioCopyCodecs(t *testing.T) {
	for _, c := range []string{"aac", "mp3", "opus", "AAC", "Opus"} {
		if !audioCopyCodecs(c) {
			t.Errorf("%q: want copy", c)
		}
	}
	for _, c := range []string{"", "eac3", "ac3", "dts", "truehd", "flac", "vorbis"} {
		if audioCopyCodecs(c) {
			t.Errorf("%q: want transcode", c)
		}
	}
}

func TestMediaCacheRoundtripAndPrune(t *testing.T) {
	s := initDB(t.TempDir())

	if _, ok := s.getMediaCache("/x", 1, 2, "probe"); ok {
		t.Fatal("miss esperado")
	}
	var nilDB *dbStore
	if _, ok := nilDB.getMediaCache("/x", 1, 2, "probe"); ok {
		t.Fatal("nil db deve dar miss")
	}
	nilDB.setMediaCache("/x", 1, 2, "probe", "{}")
	nilDB.pruneMediaCache(map[string]struct{}{}, "probe")

	s.setMediaCache("/a", 10, 100, "probe", `{"audio":[]}`)
	s.setMediaCache("/b", 10, 100, "probe", `{"audio":[]}`)
	if raw, ok := s.getMediaCache("/a", 10, 100, "probe"); !ok || raw != `{"audio":[]}` {
		t.Fatalf("hit esperado: %q %v", raw, ok)
	}
	if _, ok := s.getMediaCache("/a", 11, 100, "probe"); ok {
		t.Fatal("mtime mudou: miss esperado")
	}
	if _, ok := s.getMediaCache("/a", 10, 101, "probe"); ok {
		t.Fatal("size mudou: miss esperado")
	}

	s.pruneMediaCache(map[string]struct{}{"/a": {}}, "probe")
	if _, ok := s.getMediaCache("/b", 10, 100, "probe"); ok {
		t.Fatal("/b deveria ter sido podado")
	}
	if _, ok := s.getMediaCache("/a", 10, 100, "probe"); !ok {
		t.Fatal("/a deveria permanecer")
	}
}

func TestGenerateThumbs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "poster.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "thumbs"), 0755); err != nil {
		t.Fatal(err)
	}
	s := &server{
		cacheDir:  cache,
		sem:       make(chan struct{}, 2),
		cinemaMap: map[string]VideoMedia{"v1": {ID: "v1", Path: src}},
	}
	if n := s.generateThumbs(480); n != 1 {
		t.Fatalf("got %d warmed, want 1", n)
	}
	if n := s.generateThumbs(480); n != 0 {
		t.Fatalf("existing thumb regenerated: %d", n)
	}
	if _, err := os.Stat(filepath.Join(cache, "thumbs")); err != nil {
		t.Fatal(err)
	}
}

func TestAudioCacheEvictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "audio")
	if err := os.MkdirAll(audio, 0755); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, size int, age time.Duration) {
		p := filepath.Join(audio, name)
		if err := os.WriteFile(p, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-age)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	mk("old.mp4", 10, 3*time.Hour)
	mk("mid.mp4", 10, 2*time.Hour)
	mk("new.mp4", 10, time.Hour)
	mk("stale.tmp", 10, 4*time.Hour)

	cleanupAudioCache(dir, 25)

	for _, name := range []string{"mid.mp4", "new.mp4"} {
		if _, err := os.Stat(filepath.Join(audio, name)); err != nil {
			t.Fatalf("%s deveria permanecer: %v", name, err)
		}
	}
	for _, name := range []string{"old.mp4", "stale.tmp"} {
		if _, err := os.Stat(filepath.Join(audio, name)); err == nil {
			t.Fatalf("%s deveria ter sido removido", name)
		}
	}
}

func TestSaveProgressHalfCountsAsDone(t *testing.T) {
	s := initDB(t.TempDir())
	if err := s.SaveProgress("m1", 60, 100, false); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.GetProgress("m1"); !rec.Completed {
		t.Fatal("60% deveria concluir")
	}
	if err := s.SaveProgress("m2", 40, 100, false); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.GetProgress("m2"); rec.Completed {
		t.Fatal("40% não deveria concluir")
	}
	if err := s.SaveProgress("m3", 5, 0, false); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.GetProgress("m3"); rec.Completed {
		t.Fatal("total 0 não deveria concluir")
	}
}

func TestAllTagsAndMediaIDsWithTags(t *testing.T) {
	s := initDB(t.TempDir())
	s.AddTag("m1", "Ação")
	s.AddTag("m1", "Comédia")
	s.AddTag("m2", "ação")

	got := s.AllTags("", 20)
	if len(got) != 2 || got[0].Tag != "ação" || got[0].Count != 2 {
		t.Fatalf("all: %+v", got)
	}
	if got := s.AllTags("aç", 20); len(got) != 1 || got[0].Tag != "ação" {
		t.Fatalf("prefix: %+v", got)
	}
	ids := s.MediaIDsWithTags([]string{"ação"})
	if len(ids) != 2 || !ids["m1"] || !ids["m2"] {
		t.Fatalf("and-1: %v", ids)
	}
	if ids := s.MediaIDsWithTags([]string{"ação", "comédia"}); len(ids) != 1 || !ids["m1"] {
		t.Fatalf("and-2: %v", ids)
	}
	if ids := s.MediaIDsWithTags([]string{"inexistente"}); len(ids) != 0 {
		t.Fatalf("empty: %v", ids)
	}
}

func TestMangaCoverSetGetClear(t *testing.T) {
	s := initDB(t.TempDir())
	if _, ok := s.GetMangaCover("s1"); ok {
		t.Fatal("miss esperado")
	}
	s.SetMangaCover("s1", "ch9", 4)
	rec, ok := s.GetMangaCover("s1")
	if !ok || rec.ChapterID != "ch9" || rec.Page != 4 {
		t.Fatalf("got %+v ok=%v", rec, ok)
	}
	s.SetMangaCover("s1", "ch10", 0)
	if rec, _ := s.GetMangaCover("s1"); rec.ChapterID != "ch10" {
		t.Fatalf("upsert: %+v", rec)
	}
	s.ClearMangaCover("s1")
	if _, ok := s.GetMangaCover("s1"); ok {
		t.Fatal("clear deveria remover")
	}
}

func TestHandleTagsAll(t *testing.T) {
	s := &server{db: initDB(t.TempDir())}
	s.db.AddTag("m1", "Ação")
	rec := httptest.NewRecorder()
	s.handleTagsAll(rec, httptest.NewRequest("GET", "/api/tags/all?prefix=a", nil))
	var d struct {
		Tags []tagFreq `json:"tags"`
	}
	if err := jsonDecodeBody(rec, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Tags) != 1 || d.Tags[0].Tag != "ação" {
		t.Fatalf("got %+v", d.Tags)
	}
}

func TestHandleMangaCoverSetValidates(t *testing.T) {
	s := &server{
		db:          initDB(t.TempDir()),
		lastScan:    time.Now(),
		cinemaItems: []VideoGroup{},
		mangaChMap:  map[string]MangaChapter{"ch1": {ID: "ch1", Title: "Cap 1", PageCount: 10}},
		mangaItems:  []MangaSeries{{ID: "s1", Title: "S", Chapters: []MangaChapter{{ID: "ch1", Title: "Cap 1", PageCount: 10}}}},
	}
	bad := []string{
		`{"series_id":"s1","chapter_id":"chX","page":0}`,
		`{"series_id":"sX","chapter_id":"ch1","page":0}`,
		`{"series_id":"s1","chapter_id":"ch1","page":99}`,
		`{"series_id":"","chapter_id":"ch1"}`,
	}
	for _, b := range bad {
		rec := httptest.NewRecorder()
		s.handleMangaCoverSet(rec, httptest.NewRequest("POST", "/api/manga/cover", strings.NewReader(b)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", b, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.handleMangaCoverSet(rec, httptest.NewRequest("POST", "/api/manga/cover", strings.NewReader(`{"series_id":"s1","chapter_id":"ch1","page":3}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid: got %d", rec.Code)
	}
	if cov, ok := s.db.GetMangaCover("s1"); !ok || cov.Page != 3 {
		t.Fatalf("not stored: %+v %v", cov, ok)
	}
}

func TestFfSlotsAutoAndOverride(t *testing.T) {
	t.Setenv("FF_SLOTS", "")
	n := ffSlots()
	if n < 1 || n > 8 {
		t.Fatalf("auto fora da faixa: %d", n)
	}
	t.Setenv("FF_SLOTS", "6")
	if got := ffSlots(); got != 6 {
		t.Fatalf("override: got %d, want 6", got)
	}
	t.Setenv("FF_SLOTS", "99")
	if got := ffSlots(); got != n {
		t.Fatalf("99 deve ser ignorado: got %d, want %d", got, n)
	}
	t.Setenv("FF_SLOTS", "0")
	if got := ffSlots(); got != n {
		t.Fatalf("0 deve ser ignorado: got %d, want %d", got, n)
	}
}

func TestVaapiDeviceRespectsEnv(t *testing.T) {
	t.Setenv("FF_VAAPI", "0")
	if got := vaapiDevice(); got != "" {
		t.Fatalf("FF_VAAPI=0: got %q, want vazio", got)
	}
	t.Setenv("FF_VAAPI", "/dev/dri/inexistente-xyz")
	if got := vaapiDevice(); got != "" {
		t.Fatalf("device inválido: got %q, want vazio", got)
	}
}
