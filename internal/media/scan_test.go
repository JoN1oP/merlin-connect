package media

import (
	"testing"
	"testing/fstest"
)

func file(data string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(data)} }

func TestScanMatchesImagesAndTitles(t *testing.T) {
	tagged := string(tag(3, 0,
		frame3("TIT2", []byte("\x00Titre ID3")),
		frame3("APIC", apic(frontCover, "ID3COVER"))))
	fsys := fstest.MapFS{
		"Contes/10 - Dixieme.mp3": file("mp3"),
		"Contes/2 - Deuxieme.mp3": file("mp3"),
		"Contes/2 - Deuxieme.PNG": file("SAMENAME"),
		"Contes/tagged.mp3":       file(tagged),
		"Contes/tagged.jpg":       file("FILEWINS"),
		"Contes/id3only.mp3":      file(tagged),
		"Contes/cover.jpg":        file("FOLDERCOVER"),
		"Contes/.hidden.mp3":      file("mp3"),
		"Contes/Vide/notes.txt":   file("x"),
		"Contes/Sous/a.mp3":       file("mp3"),
		"__MACOSX/Contes/a.mp3":   file("mp3"),
		"loose.mp3":               file("mp3"),
	}
	root, err := Scan(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Folders) != 1 || len(root.Stories) != 1 || root.Stories[0].Title != "loose" {
		t.Fatalf("root = %+v", root)
	}
	contes := root.Folders[0]
	if contes.Name != "Contes" || string(contes.Cover) != "FOLDERCOVER" {
		t.Fatalf("folder = %q cover %q", contes.Name, contes.Cover)
	}
	if len(contes.Folders) != 1 || contes.Folders[0].Name != "Sous" {
		t.Fatalf("subfolders = %+v (empty folders must be skipped)", contes.Folders)
	}
	want := []struct{ title, cover string }{
		{"Deuxieme", "SAMENAME"},   // natural order: 2 before 10
		{"Dixieme", "FOLDERCOVER"}, // no own image: folder cover
		{"Titre ID3", "ID3COVER"},  // id3only.mp3: ID3 title and cover
		{"Titre ID3", "FILEWINS"},  // tagged.mp3: same-name file beats ID3 cover
	}
	if len(contes.Stories) != len(want) {
		t.Fatalf("got %d stories", len(contes.Stories))
	}
	for i, w := range want {
		s := contes.Stories[i]
		if s.Title != w.title || string(s.Cover) != w.cover {
			t.Errorf("story %d = %q / %q, want %q / %q", i, s.Title, s.Cover, w.title, w.cover)
		}
	}
}

func TestScanEmpty(t *testing.T) {
	root, err := Scan(fstest.MapFS{"notes.txt": file("x")})
	if err != nil || len(root.Stories)+len(root.Folders) != 0 {
		t.Fatalf("root = %+v, %v", root, err)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"01 - Le loup":     "Le loup",
		"3. Les pirates":   "Les pirates",
		"07 Dodo":          "Dodo",
		"12_la_mer":        "la mer",
		"3 petits cochons": "3 petits cochons",
		"2001":             "2001",
		"01 - ":            "01 -",
	}
	for in, want := range cases {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNaturalCompare(t *testing.T) {
	if naturalCompare("2 x", "10 x") >= 0 || naturalCompare("B", "a") <= 0 || naturalCompare("a01", "a1") != 0 {
		t.Fatal("naturalCompare")
	}
}
