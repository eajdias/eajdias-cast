package main

import "testing"

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
