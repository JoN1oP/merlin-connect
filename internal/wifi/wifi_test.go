package wifi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeNMCLI records calls and answers from a table keyed by the joined args.
type fakeNMCLI struct {
	calls   []string
	answers map[string]string
	fail    map[string]bool
}

func (f *fakeNMCLI) run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	if f.fail[key] {
		return "", errors.New("failed: " + key)
	}
	return f.answers[key], nil
}

const devices = "eth0:ethernet\np2p-dev-wlan0:wifi-p2p\nwlan0:wifi\nlo:loopback\n"

func TestParseScan(t *testing.T) {
	out := "Home:90\nMERLIN_ABC123:40\nMERLIN_AAAAAA:75\nMERLIN_ABC123:55\n" + `MERLIN\:X:10` + "\n\n"
	got := parseScan(out)
	want := []AP{{"MERLIN_AAAAAA", 75}, {"MERLIN_ABC123", 55}}
	if !slices.Equal(got, want) {
		t.Fatalf("parseScan = %v, want %v", got, want)
	}
}

func TestPickInterface(t *testing.T) {
	if got := pickInterface(devices); got != "wlan0" {
		t.Fatalf("got %q", got)
	}
	if got := pickInterface("eth0:ethernet\n"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitTerse(t *testing.T) {
	if a, b := splitTerse(`MERLIN\:X:42`); a != "MERLIN:X" || b != "42" {
		t.Fatalf("got %q %q", a, b)
	}
}

func TestJoinCreatesSafeProfile(t *testing.T) {
	f := &fakeNMCLI{answers: map[string]string{"-t -f DEVICE,TYPE dev": devices}}
	if err := (NMCLI{Run: f.run}).Join(context.Background(), "MERLIN_ABC123"); err != nil {
		t.Fatal(err)
	}
	add := f.calls[2]
	for _, want := range []string{"ifname wlan0", "wifi-sec.key-mgmt wpa-psk", "wifi-sec.psk MERLIN_APP",
		"ipv4.never-default yes", "connection.autoconnect no"} {
		if !strings.Contains(add, want) {
			t.Errorf("profile %q lacks %q", add, want)
		}
	}
	if f.calls[3] != "--wait 30 con up MERLIN_ABC123" {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestJoinCleansUpOnFailure(t *testing.T) {
	f := &fakeNMCLI{
		answers: map[string]string{"-t -f DEVICE,TYPE dev": devices},
		fail:    map[string]bool{"--wait 30 con up MERLIN_ABC123": true},
	}
	if err := (NMCLI{Run: f.run}).Join(context.Background(), "MERLIN_ABC123"); err == nil {
		t.Fatal("want error")
	}
	if last := f.calls[len(f.calls)-1]; last != "con delete MERLIN_ABC123" {
		t.Fatalf("last call = %q", last)
	}
}

func TestJoinRefusesOtherNetworks(t *testing.T) {
	f := &fakeNMCLI{}
	if err := (NMCLI{Run: f.run}).Join(context.Background(), "Home"); err == nil || len(f.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, f.calls)
	}
}

func TestLeave(t *testing.T) {
	f := &fakeNMCLI{answers: map[string]string{"-t -f DEVICE,TYPE dev": devices}}
	if err := (NMCLI{Run: f.run}).Leave("MERLIN_ABC123"); err != nil {
		t.Fatal(err)
	}
	want := []string{"--wait 10 con delete MERLIN_ABC123", "-t -f DEVICE,TYPE dev", "--wait 0 dev connect wlan0"}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestManual(t *testing.T) {
	if _, err := (Manual{}).Scan(context.Background()); !errors.Is(err, ErrManualJoin) {
		t.Fatal(err)
	}
}
