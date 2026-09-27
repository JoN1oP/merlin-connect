// Command fakebox serves an in-memory Merlin box on localhost, so the UI can be
// tried without the real box:
//
//	go run ./cmd/fakebox &
//	go run ./cmd/merlin-connect -box 127.0.0.1:50001 -data /tmp/merlin-dev
//
// -rate and -read-delay make it about as slow as the real box.
package main

import (
	"flag"
	"log"
	"net"
	"time"

	"merlin-connect/internal/box/boxtest"
	"merlin-connect/internal/media"
	"merlin-connect/internal/playlist"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:50001", "listen address")
	rate := flag.Int("rate", 0, "bytes per second the box receives (0: unlimited)")
	readDelay := flag.Duration("read-delay", 0, "delay before each file download")
	flag.Parse()

	fake := boxtest.New()
	fake.GetFileDelay = *readDelay
	story := func(uuid, title string) *playlist.Node { return &playlist.Node{UUID: uuid, Title: title, Story: true} }
	tree := &playlist.Node{Children: []*playlist.Node{
		{UUID: "histoires", Title: "Histoires", Children: []*playlist.Node{
			story("au-lit", "Au lit !"), story("docteur", "Chez le docteur"), story("pompier", "Le petit pompier")}},
		{UUID: "musique", Title: "Musique", Children: []*playlist.Node{story("pierre", "Pierre et le Loup")}},
		{UUID: "calme", Title: "Calme", Children: []*playlist.Node{story("silence", "La balade du silence")}},
		{UUID: "favoris", Title: "Merlin_favorite"},
	}}
	fake.SetTree(tree)
	tree.Walk(func(n, _ *playlist.Node) {
		fake.Files[n.UUID+".jpg"] = media.Placeholder(n.Title)
		if n.Story {
			fake.Files[n.UUID+".mp3"] = []byte("fake audio")
		}
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("fake Merlin box on", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		if *rate > 0 {
			conn = &throttled{Conn: conn, rate: *rate}
		}
		go fake.Serve(conn)
	}
}

// throttled limits how fast the box reads from the client.
type throttled struct {
	net.Conn
	rate int
}

func (t *throttled) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p[:min(len(p), max(1, t.rate/20))])
	time.Sleep(time.Duration(n) * time.Second / time.Duration(t.rate))
	return n, err
}
