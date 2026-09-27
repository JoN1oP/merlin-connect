// Package wifi finds and joins the box's access point.
package wifi

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Box access point credentials (docs/protocol.md).
const (
	SSIDPrefix = "MERLIN_"
	Passphrase = "MERLIN_APP"
)

// ErrManualJoin means this system cannot join networks for us: the user joins
// the box's network, then connects.
var ErrManualJoin = errors.New("wifi: join the MERLIN_ network manually")

// AP is a box access point seen in a scan.
type AP struct {
	SSID   string
	Signal int // 0-100
}

// Joiner scans for and joins box access points.
type Joiner interface {
	Scan(ctx context.Context) ([]AP, error)
	Join(ctx context.Context, ssid string) error
	Leave(ssid string) error
}

// New returns the best joiner for this system.
func New() Joiner {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("nmcli"); err == nil {
			return NMCLI{Run: runNMCLI}
		}
	}
	return Manual{}
}

// Manual is the joiner for systems we cannot drive: the user joins by hand.
type Manual struct{}

func (Manual) Scan(context.Context) ([]AP, error) { return nil, ErrManualJoin }
func (Manual) Join(context.Context, string) error { return ErrManualJoin }
func (Manual) Leave(string) error                 { return nil }

// NMCLI drives NetworkManager on Linux.
type NMCLI struct {
	Run func(ctx context.Context, args ...string) (string, error)
}

func runNMCLI(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "nmcli", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", errors.New("nmcli " + strings.Join(args, " ") + ": " + strings.TrimSpace(string(exit.Stderr)))
	}
	return string(out), err
}

// Scan lists box access points, strongest first. It forces a fresh scan so a
// network raised seconds ago is not missed.
func (n NMCLI) Scan(ctx context.Context) ([]AP, error) {
	out, err := n.Run(ctx, "-t", "-f", "SSID,SIGNAL", "dev", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	return parseScan(out), nil
}

func parseScan(out string) []AP {
	best := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		ssid, signal := splitTerse(line)
		if !strings.HasPrefix(ssid, SSIDPrefix) {
			continue
		}
		level, _ := strconv.Atoi(signal)
		if old, seen := best[ssid]; !seen || level > old {
			best[ssid] = level
		}
	}
	aps := make([]AP, 0, len(best))
	for ssid, level := range best {
		aps = append(aps, AP{SSID: ssid, Signal: level})
	}
	slices.SortFunc(aps, func(a, b AP) int { return b.Signal - a.Signal })
	return aps
}

// Join creates a throwaway profile for the box network and brings it up. The
// profile never becomes the default route (the box has no internet) and never
// autoconnects later.
func (n NMCLI) Join(ctx context.Context, ssid string) error {
	if !strings.HasPrefix(ssid, SSIDPrefix) {
		return errors.New("wifi: refusing to join non-Merlin network " + ssid)
	}
	devices, err := n.Run(ctx, "-t", "-f", "DEVICE,TYPE", "dev")
	if err != nil {
		return err
	}
	iface := pickInterface(devices)
	if iface == "" {
		return errors.New("wifi: no WiFi interface found")
	}
	n.Run(ctx, "con", "delete", ssid) // leftover from an interrupted run
	if _, err := n.Run(ctx, profileArgs(iface, ssid)...); err != nil {
		return err
	}
	if _, err := n.Run(ctx, "--wait", "30", "con", "up", ssid); err != nil {
		n.Run(context.Background(), "con", "delete", ssid)
		return err
	}
	return nil
}

// Leave deletes the profile and lets NetworkManager reconnect the usual network
// in the background.
func (n NMCLI) Leave(ssid string) error {
	ctx := context.Background()
	_, err := n.Run(ctx, "--wait", "10", "con", "delete", ssid)
	if devices, derr := n.Run(ctx, "-t", "-f", "DEVICE,TYPE", "dev"); derr == nil {
		if iface := pickInterface(devices); iface != "" {
			n.Run(ctx, "--wait", "0", "dev", "connect", iface)
		}
	}
	return err
}

func profileArgs(iface, ssid string) []string {
	return []string{
		"con", "add", "type", "wifi",
		"ifname", iface,
		"con-name", ssid,
		"ssid", ssid,
		"wifi-sec.key-mgmt", "wpa-psk",
		"wifi-sec.psk", Passphrase,
		"ipv4.method", "auto",
		"ipv4.never-default", "yes",
		"ipv6.method", "ignore",
		"connection.autoconnect", "no",
	}
}

// pickInterface returns the first real WiFi device (not a wifi-p2p one).
func pickInterface(devices string) string {
	for _, line := range strings.Split(devices, "\n") {
		device, kind := splitTerse(line)
		if kind == "wifi" && device != "" {
			return device
		}
	}
	return ""
}

// splitTerse splits an nmcli terse line on its first unescaped ':'.
func splitTerse(line string) (string, string) {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case ':':
			return strings.ReplaceAll(line[:i], `\:`, ":"), line[i+1:]
		}
	}
	return strings.ReplaceAll(line, `\:`, ":"), ""
}
