package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"merlin-connect/internal/app"
	"merlin-connect/internal/box"
	"merlin-connect/internal/library"
	"merlin-connect/internal/wifi"
)

func newServer(t *testing.T) (*httptest.Server, *library.Library) {
	t.Helper()
	lib, err := library.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := app.New(app.Config{
		Library:  lib,
		Joiner:   wifi.Manual{},
		CacheDir: t.TempDir(),
		Dial: func(context.Context) (*box.Client, error) {
			return nil, errors.New("no box in web tests")
		},
	})
	srv := httptest.NewServer(New(session, lib))
	t.Cleanup(srv.Close)
	return srv, lib
}

func call(t *testing.T, method, url, contentType string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("X-Merlin", "1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func state(t *testing.T, srv *httptest.Server) snapshot {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestIndexAndState(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("index: %v %v", resp.Status, err)
	}
	resp.Body.Close()
	if st := state(t, srv).Status.State; st != app.Offline {
		t.Fatalf("state = %s", st)
	}
}

func TestGuards(t *testing.T) {
	srv, _ := newServer(t)
	resp, _ := http.Post(srv.URL+"/api/folders", "application/json", strings.NewReader(`{"title":"x"}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST without X-Merlin: %s", resp.Status)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/state", nil)
	req.Host = "evil.example:80"
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host: %s", resp.Status)
	}
}

func TestFolderRenameMoveDelete(t *testing.T) {
	srv, lib := newServer(t)
	resp := call(t, "POST", srv.URL+"/api/folders", "application/json", []byte(`{"parent":"","title":"Contes"}`))
	var a library.Item
	json.NewDecoder(resp.Body).Decode(&a)
	call(t, "POST", srv.URL+"/api/folders", "application/json", []byte(`{"parent":"","title":"B"}`))

	resp = call(t, "PATCH", srv.URL+"/api/items/"+a.UUID, "application/json", []byte(`{"title":"Contes de papi","position":1}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch: %s", resp.Status)
	}
	tree := state(t, srv).Tree
	if len(tree.Children) != 2 || tree.Children[1].Title != "Contes de papi" || !tree.Children[1].Custom {
		t.Fatalf("tree = %+v", tree.Children)
	}
	if state(t, srv).Status.Pending != 0 {
		t.Fatal("empty folders are not pending")
	}

	resp = call(t, "PATCH", srv.URL+"/api/items/"+a.UUID, "application/json", []byte(`{"title":"  "}`))
	var e map[string]string
	json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != http.StatusBadRequest || e["error"] != "Le titre ne peut pas être vide." {
		t.Fatalf("empty title: %s %v", resp.Status, e)
	}

	call(t, "DELETE", srv.URL+"/api/items/"+a.UUID, "", nil)
	if len(lib.Manifest().Items) != 1 {
		t.Fatal("delete failed")
	}
}

func TestImportKeepsFolderStructure(t *testing.T) {
	srv, lib := newServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for path, data := range map[string]string{"Contes/Loup.mp3": "audio", "Contes/Sous/Dodo.mp3": "audio"} {
		mw.WriteField("path", path)
		fw, _ := mw.CreateFormFile("file", "ignored")
		fw.Write([]byte(data))
	}
	mw.Close()
	resp := call(t, "POST", srv.URL+"/api/import?parent=", mw.FormDataContentType(), body.Bytes())
	var res library.ImportResult
	json.NewDecoder(resp.Body).Decode(&res)
	if res.Folders != 2 || res.Stories != 2 {
		t.Fatalf("result = %+v (%s)", res, resp.Status)
	}
	if st := state(t, srv).Status; st.Pending != 4 {
		t.Fatalf("pending = %d", st.Pending)
	}
	if len(lib.Manifest().Items) != 4 {
		t.Fatal("items not stored")
	}
}

func TestImportRejectsEscapingPaths(t *testing.T) {
	srv, _ := newServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("path", "../evil.mp3")
	fw, _ := mw.CreateFormFile("file", "evil.mp3")
	fw.Write([]byte("x"))
	mw.Close()
	if resp := call(t, "POST", srv.URL+"/api/import", mw.FormDataContentType(), body.Bytes()); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %s", resp.Status)
	}
}

func TestCover(t *testing.T) {
	srv, lib := newServer(t)
	item, _ := lib.AddFolder("", "F", nil)
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	if resp := call(t, "PUT", srv.URL+"/api/items/"+item.UUID+"/cover", "image/png", img.Bytes()); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set cover: %s", resp.Status)
	}
	resp, _ := http.Get(srv.URL + "/covers/" + item.UUID)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("cover: %s", resp.Status)
	}
	resp, _ = http.Get(srv.URL + "/covers/unknown")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown cover: %s", resp.Status)
	}
}

func TestCoverRejectsPathTraversal(t *testing.T) {
	srv, lib := newServer(t)
	// <data>/media/../../secret.jpg: a file outside the library.
	secret := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(lib.ImagePath("x")))), "secret.jpg")
	if err := os.WriteFile(secret, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/covers/..%2F..%2Fsecret")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %s: a file outside the library was served", resp.Status)
	}
}

func TestEventsStreamSnapshots(t *testing.T) {
	srv, _ := newServer(t)
	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	line, _ := r.ReadString('\n')
	if !strings.HasPrefix(line, `data: {"status":{"state":"offline"`) {
		t.Fatalf("first event = %q", line)
	}
	call(t, "POST", srv.URL+"/api/folders", "application/json", []byte(`{"title":"Nouveau"}`))
	for range 10 {
		line, _ = r.ReadString('\n')
		if strings.Contains(line, "Nouveau") {
			return
		}
	}
	t.Fatal("no event after a change")
}
