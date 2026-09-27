package media

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Story is an MP3 found by Scan.
type Story struct {
	Title string
	Audio string // path of the MP3 inside the scanned file system
	Cover []byte // raw image bytes in any supported format, nil if none found
}

// Folder is a directory found by Scan that holds at least one story below it.
type Folder struct {
	Name    string
	Cover   []byte
	Folders []*Folder
	Stories []*Story
}

var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".bmp": true}

// Scan walks fsys and returns what it would import. The returned root stands for
// the import target itself: its stories and folders are added there.
//
// A story's cover is, in order: an image with the same base name, the MP3's ID3
// cover, then the folder's cover.* or folder.* image. Its title is the ID3 title,
// else the cleaned file name.
func Scan(fsys fs.FS) (*Folder, error) {
	root, err := scanDir(fsys, ".")
	if root == nil && err == nil {
		root = &Folder{Name: "."}
	}
	return root, err
}

func scanDir(fsys fs.FS, dir string) (*Folder, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return naturalCompare(a.Name(), b.Name()) })

	f := &Folder{Name: path.Base(dir)}
	images := map[string]string{} // lower-case base name -> path
	var audios []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "__MACOSX" {
			continue
		}
		p := path.Join(dir, name)
		if e.IsDir() {
			sub, err := scanDir(fsys, p)
			if err != nil {
				return nil, err
			}
			if sub != nil {
				f.Folders = append(f.Folders, sub)
			}
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		stem := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
		switch {
		case ext == ".mp3":
			audios = append(audios, p)
		case imageExts[ext] && images[stem] == "":
			images[stem] = p
		}
	}

	f.Cover = readFile(fsys, images["cover"])
	if f.Cover == nil {
		f.Cover = readFile(fsys, images["folder"])
	}
	for _, audio := range audios {
		stem := strings.TrimSuffix(path.Base(audio), path.Ext(audio))
		tag := readTag(fsys, audio)
		s := &Story{Audio: audio, Title: tag.Title}
		if s.Title == "" {
			s.Title = CleanTitle(stem)
		}
		switch {
		case images[strings.ToLower(stem)] != "":
			s.Cover = readFile(fsys, images[strings.ToLower(stem)])
		case tag.Cover != nil:
			s.Cover = tag.Cover
		default:
			s.Cover = f.Cover
		}
		f.Stories = append(f.Stories, s)
	}
	if len(f.Stories) == 0 && len(f.Folders) == 0 {
		return nil, nil
	}
	return f, nil
}

func readFile(fsys fs.FS, name string) []byte {
	if name == "" {
		return nil
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil
	}
	return data
}

func readTag(fsys fs.FS, name string) Tag {
	file, err := fsys.Open(name)
	if err != nil {
		return Tag{}
	}
	defer file.Close()
	tag, _ := ReadID3(file)
	return tag
}

// trackPrefix matches "01 ", "3 - ", "12. ", "7_" but not "3 petits cochons".
var trackPrefix = regexp.MustCompile(`^(\d{1,3}\s*[-._)]\s*|0\d\s+)`)

// CleanTitle turns a file name into a title: track numbers and underscores go.
func CleanTitle(stem string) string {
	title := trackPrefix.ReplaceAllString(stem, "")
	title = strings.Join(strings.Fields(strings.ReplaceAll(title, "_", " ")), " ")
	if title == "" {
		return strings.TrimSpace(stem)
	}
	return title
}

// naturalCompare orders names case-insensitively with digit runs compared as
// numbers, so "2 x" sorts before "10 x".
func naturalCompare(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da != "" && db != "" {
			na, nb := strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			if c := len(na) - len(nb); c != 0 {
				return c
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
