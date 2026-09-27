package playlist

import (
	"reflect"
	"strings"
	"testing"
)

func sample() *Node {
	return &Node{Children: []*Node{
		{UUID: "f1", Title: "Histoires", Children: []*Node{
			{UUID: "s1", Title: "Au lit !", Story: true, Type: TypeStory},
			{UUID: "s2", Title: "Chez le docteur", Story: true, Type: TypeStoryImg},
		}},
		{UUID: "fav", Title: "Merlin_favorite"},
		{UUID: "s3", Title: "Anaïg et les nuages", Story: true, Type: TypeStoryImg},
	}}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	got, err := Decode(Encode(sample()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sample()) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, sample())
	}
}

func TestEncodeKeepsStoryTypes(t *testing.T) {
	// Official stories are type 4 on the box; a direct playlist.bin write must
	// give them back unchanged. New stories get 36.
	tree := sample()
	tree.Children = append(tree.Children, &Node{UUID: "new", Title: "Nouvelle", Story: true})
	raw := Encode(tree)
	records, err := Records(raw)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]uint16{}
	for _, r := range records {
		types[r.UUID] = r.Type
	}
	if types["s1"] != TypeStory || types["s2"] != TypeStoryImg || types["new"] != TypeStoryImg {
		t.Fatalf("types = %v", types)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if again := Encode(back); string(again) != string(raw) {
		t.Fatal("Encode(Decode(x)) differs from x")
	}
}

func TestRecordsLayout(t *testing.T) {
	records, err := Records(Encode(sample()))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("got %d records, want 6", len(records))
	}
	root, folder, fav := records[0], records[1], records[4]
	if root.Type != TypeRoot || root.ParentID != 0 || root.Children != 3 || root.Title != "Root" {
		t.Errorf("root = %+v", root)
	}
	if folder.Type != TypeFolder || folder.ParentID != 1 || folder.Children != 2 {
		t.Errorf("folder = %+v", folder)
	}
	if fav.Type != TypeFavorites {
		t.Errorf("favorites type = %d", fav.Type)
	}
	if records[5].Title != "Anaïg et les nuages" || records[5].Type != TypeStoryImg {
		t.Errorf("story = %+v", records[5])
	}
}

func TestDecodeOrdersChildrenByOrderField(t *testing.T) {
	data := Encode(sample())
	// Swap the order fields of the two stories in "Histoires" (records at index 2 and 3).
	data[2*recordSize+4], data[3*recordSize+4] = 1, 0
	root, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := root.Children[0].Children[0].UUID; got != "s2" {
		t.Fatalf("first child = %s, want s2", got)
	}
}

// favorites is a tree whose favorites folder lists two stories by reference.
func favorites() *Node {
	return &Node{Children: []*Node{
		{UUID: "f1", Title: "Histoires", Children: []*Node{
			{UUID: "s1", Title: "Au lit", Story: true, Type: TypeStoryImg},
			{UUID: "s2", Title: "Docteur", Story: true, Type: TypeStoryImg},
		}},
		{UUID: "fav", Title: "Merlin_favorite", Children: []*Node{
			{UUID: "s2", Title: "Docteur", Story: true, Type: TypeStoryImg},
			{UUID: "s1", Title: "Au lit", Story: true, Type: TypeStoryImg},
		}},
	}}
}

func TestFavoritesAreStoredAsFavOrder(t *testing.T) {
	// The real box (probe, 2026-09-26) links no child to Merlin_favorite: each
	// favorite story carries fav_order 1..n and the folder only counts them.
	records, err := Records(Encode(favorites()))
	if err != nil {
		t.Fatal(err)
	}
	fav := map[string]uint16{}
	for _, r := range records {
		fav[r.UUID] = r.FavOrder
		if r.Title == "Merlin_favorite" && r.Children != 2 {
			t.Errorf("favorites folder counts %d children, want 2", r.Children)
		}
	}
	if len(records) != 5 || fav["s2"] != 1 || fav["s1"] != 2 || fav["f1"] != 0 {
		t.Fatalf("records = %+v", records)
	}
	got, err := Decode(Encode(favorites()))
	if err != nil || !reflect.DeepEqual(got, favorites()) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
}

func TestDecodeListsEachFavoriteOnce(t *testing.T) {
	// On the real box a story filed in two folders carries its fav_order twice.
	var raw []byte
	for _, r := range []Record{
		{ID: 1, Type: TypeRoot, Children: 3},
		{ID: 2, ParentID: 1, Type: TypeFolder, UUID: "a", Title: "A", Children: 1},
		{ID: 3, ParentID: 2, Type: TypeStoryImg, UUID: "s", Title: "Adele", FavOrder: 1},
		{ID: 4, ParentID: 1, Order: 1, Type: TypeFolder, UUID: "b", Title: "B", Children: 1},
		{ID: 5, ParentID: 4, Type: TypeStoryImg, UUID: "s", Title: "Adele", FavOrder: 1},
		{ID: 6, ParentID: 1, Order: 2, Type: TypeFavorites, UUID: "fav", Title: FavoritesTitle, Children: 1},
	} {
		raw = appendRecord(raw, r)
	}
	tree, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if favs := tree.Children[2].Children; len(favs) != 1 || favs[0].UUID != "s" {
		t.Fatalf("favorites = %+v, want one reference to s", favs)
	}
}

func TestTimesSurviveEncodeDecode(t *testing.T) {
	tree := &Node{Children: []*Node{
		{UUID: "s", Title: "Oli", Story: true, AddTime: 1758900000, LimitTime: 1798758000, Type: TypeStoryImg},
	}}
	got, err := Decode(Encode(tree))
	if err != nil || !reflect.DeepEqual(got, tree) {
		t.Fatalf("got %+v, %v", got.Children[0], err)
	}
}

func TestDecodeRejectsTruncatedData(t *testing.T) {
	if _, err := Decode(make([]byte, recordSize+1)); err == nil {
		t.Fatal("want error")
	}
}

func TestDecodeRejectsMissingRoot(t *testing.T) {
	data := Encode(sample())[recordSize:] // drop the root record
	if _, err := Decode(data); err == nil {
		t.Fatal("want error")
	}
}

func TestBoxJSON(t *testing.T) {
	got, err := sample().BoxJSON()
	if err != nil {
		t.Fatal(err)
	}
	// The real box requires add_time and limit_time on every node (probe, 2026-09-26).
	want := `[{"uuid":"f1","title":"Histoires","add_time":0,"limit_time":0,"child":[` +
		`{"uuid":"s1","title":"Au lit !","add_time":0,"limit_time":0},` +
		`{"uuid":"s2","title":"Chez le docteur","add_time":0,"limit_time":0}]},` +
		`{"uuid":"fav","title":"Merlin_favorite","add_time":0,"limit_time":0,"child":[]},` +
		`{"uuid":"s3","title":"Anaïg et les nuages","add_time":0,"limit_time":0}]`
	if string(got) != want {
		t.Fatalf("BoxJSON =\n%s\nwant\n%s", got, want)
	}
	plain := sample()
	plain.Walk(func(n, _ *Node) { n.Type = 0 }) // not part of the JSON
	back, err := ParseBoxJSON(got)
	if err != nil || !reflect.DeepEqual(back, plain) {
		t.Fatalf("ParseBoxJSON = %+v, %v", back, err)
	}
}

func TestBoxJSONKeepsTitlesRaw(t *testing.T) {
	// The box counts a title's JSON text: "&" escaped as \u0026 pushed a
	// 64-byte official title over the limit (TITLE_TOO_LARGE on the real box).
	title := "'Knots on a Counting Rope' read by Bonnie Bartlett & William Dan"
	tree := &Node{Children: []*Node{{UUID: "s", Title: title, Story: true, AddTime: 7, LimitTime: 9}}}
	got, err := tree.BoxJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"title":"`+title+`"`) || !strings.Contains(string(got), `"add_time":7,"limit_time":9`) {
		t.Fatalf("BoxJSON = %s", got)
	}
	back, _ := ParseBoxJSON(got)
	if !reflect.DeepEqual(back, tree) {
		t.Fatalf("ParseBoxJSON = %+v", back.Children[0])
	}
}

func TestFindCountClone(t *testing.T) {
	root := sample()
	if root.Find("s2").Title != "Chez le docteur" || root.Find("nope") != nil {
		t.Fatal("Find")
	}
	if root.Count() != 5 {
		t.Fatalf("Count = %d", root.Count())
	}
	c := root.Clone()
	c.Children[0].Title = "changed"
	if root.Children[0].Title != "Histoires" {
		t.Fatal("Clone shares nodes")
	}
}

func TestNormalizeTitle(t *testing.T) {
	// The box keeps accents (official titles like "Le rêve de Noé") and accepts at
	// most 63 bytes of title text (measured on the real box, 2026-09-26).
	cases := map[string]string{
		"  Le   loup ":       "Le loup",
		"Le rêve de Noé":     "Le rêve de Noé",
		"L’œuf\tde Pâques\n": "L’œuf de Pâques",
		"":                   "",
	}
	for in, want := range cases {
		if got := NormalizeTitle(in); got != want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
	long := NormalizeTitle(strings.Repeat("é", 40)) // 80 bytes
	if len(long) > MaxTitleBytes || long != strings.Repeat("é", 31) {
		t.Fatalf("long title = %q (%d bytes), want 31 é (62 bytes)", long, len(long))
	}
}
