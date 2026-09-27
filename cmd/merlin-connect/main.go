// Command merlin-connect manages the content of a Merlin box over WiFi.
//
//	merlin-connect [-data dir] [-addr 127.0.0.1:0] [-box 192.168.4.1:50000] [-no-browser]
//	merlin-connect probe [flags]   (hardware checks, see probe.go)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"merlin-connect/internal/app"
	"merlin-connect/internal/box"
	"merlin-connect/internal/library"
	"merlin-connect/internal/web"
	"merlin-connect/internal/wifi"
)

// version is set by release builds (see .github/workflows/release.yml).
var version = "dev"

// quitAfter is how long the app keeps running once the last browser tab closed.
const quitAfter = 30 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "probe" {
		os.Exit(probe(os.Args[2:]))
	}
	dataDir := flag.String("data", defaultDataDir(), "where the library and box cache live")
	addr := flag.String("addr", "127.0.0.1:0", "listen address (localhost only)")
	boxAddr := flag.String("box", box.DefaultAddr, "box address")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser")
	flag.Parse()
	if err := run(*dataDir, *addr, *boxAddr, !*noBrowser); err != nil {
		log.Fatal(err)
	}
}

func run(dataDir, addr, boxAddr string, browser bool) error {
	lib, err := library.Open(dataDir)
	if err != nil {
		return err
	}
	session := app.New(app.Config{
		Library:  lib,
		Joiner:   wifi.New(),
		CacheDir: filepath.Join(dataDir, "box"),
		Dial: func(ctx context.Context) (*box.Client, error) {
			return box.Dial(ctx, boxAddr)
		},
	})
	defer session.Close()

	handler := web.New(session, lib)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Printf("Merlin Connect %s: %s\n", version, url)
	server := &http.Server{Handler: handler}
	go server.Serve(ln)
	defer server.Close()
	if browser {
		openBrowser(url)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for handler.Idle() < quitAfter {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
	return nil
}

func defaultDataDir() string {
	if runtime.GOOS == "linux" {
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, "merlin-connect")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "merlin-connect")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "merlin-connect")
	}
	return "merlin-connect-data"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println("Open this address in your browser:", url)
	}
}
