package box

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// DefaultAddr is where the box serves its protocol on its own access point.
const DefaultAddr = "192.168.4.1:50000"

// Command ids (docs/protocol.md). Reset (10), updateFirmware (7),
// setWifiConfiguration (16) and setEnablingHours (29) are deliberately absent.
const (
	cmdUpload         = 1
	cmdPing           = 2
	cmdFreeSpace      = 3
	cmdTotalSize      = 4
	cmdFirmware       = 5
	cmdUpdatePlaylist = 6
	cmdEndSync        = 9
	cmdGetFile        = 13
	cmdBattery        = 14
	cmdMAC            = 30
	cmdSearchFile     = 31
	cmdUnknown        = 255
)

// Upload reply statuses.
const (
	uploadReady    = 0
	uploadVerified = 1
)

const chunkSize = 100 * 1024

// Info describes the connected box.
type Info struct {
	Firmware   string `json:"firmware"`
	MAC        string `json:"mac"`
	Battery    int    `json:"battery"`
	Charging   bool   `json:"charging"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
}

// FileStat is what the box reports about a stored file.
type FileStat struct {
	Size   int64
	SHA256 [32]byte
}

// Client talks to one box over one TCP connection. It is not safe for
// concurrent use: callers serialize access.
type Client struct {
	conn    net.Conn
	r       *bufio.Reader
	Timeout time.Duration
}

// Dial connects to the box at addr.
func Dial(ctx context.Context, addr string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return NewClient(conn), nil
}

// NewClient wraps an established connection.
func NewClient(conn net.Conn) *Client {
	return &Client{conn: conn, r: bufio.NewReader(conn), Timeout: 10 * time.Second}
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) extendDeadline() { c.extendDeadlineBy(c.Timeout) }

func (c *Client) extendDeadlineBy(d time.Duration) { c.conn.SetDeadline(time.Now().Add(d)) }

// readTime is how long the box may take to answer after reading a file of size
// bytes: it hashes about 1.4 MB/s, so a 22 MB story takes some 15 s.
func (c *Client) readTime(size int64) time.Duration {
	return c.Timeout + time.Duration(size/1_000_000)*time.Second
}

// call sends a command and waits for its reply.
func (c *Client) call(cmd byte, payload []byte) ([]byte, error) {
	return c.callWithin(c.Timeout, cmd, payload)
}

// callWithin is call for a reply that may take up to d.
func (c *Client) callWithin(d time.Duration, cmd byte, payload []byte) ([]byte, error) {
	c.extendDeadline()
	if err := WriteFrame(c.conn, cmd, payload); err != nil {
		return nil, err
	}
	return c.reply(cmd, d)
}

// reply reads frames until one answers cmd within d, skipping unrelated or
// corrupt frames.
func (c *Client) reply(cmd byte, d time.Duration) ([]byte, error) {
	for range 8 {
		c.extendDeadlineBy(d)
		got, payload, err := ReadFrame(c.r)
		switch {
		case errors.Is(err, ErrBadCRC):
			continue
		case err != nil:
			return nil, err
		case got == cmdUnknown:
			return nil, fmt.Errorf("box: command %d not supported", cmd)
		case got == cmd:
			return payload, nil
		}
	}
	return nil, fmt.Errorf("box: no reply to command %d", cmd)
}

// Ping checks the box is alive.
func (c *Client) Ping() error {
	_, err := c.call(cmdPing, nil)
	return err
}

// EndSync tells the box the transfer session is over.
func (c *Client) EndSync() error {
	_, err := c.call(cmdEndSync, nil)
	return err
}

// Info reads firmware, MAC address, battery and storage.
func (c *Client) Info() (Info, error) {
	var info Info
	fw, err := c.fixed(cmdFirmware, 4)
	if err != nil {
		return info, err
	}
	info.Firmware = fmt.Sprintf("%d.%d.%d", fw[0], fw[1], binary.LittleEndian.Uint16(fw[2:]))
	mac, err := c.fixed(cmdMAC, 6)
	if err != nil {
		return info, err
	}
	info.MAC = fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
	if info.Battery, info.Charging, err = c.Battery(); err != nil {
		return info, err
	}
	total, err := c.fixed(cmdTotalSize, 4)
	if err != nil {
		return info, err
	}
	free, err := c.fixed(cmdFreeSpace, 4)
	if err != nil {
		return info, err
	}
	info.TotalBytes = uint64(binary.LittleEndian.Uint32(total))
	info.FreeBytes = uint64(binary.LittleEndian.Uint32(free))
	return info, nil
}

// Battery reads the battery level (percent) and whether it is charging.
func (c *Client) Battery() (level int, charging bool, err error) {
	p, err := c.fixed(cmdBattery, 2)
	if err != nil {
		return 0, false, err
	}
	return int(p[0]), p[1] != 0, nil
}

// fixed calls a no-argument command whose reply holds at least n bytes.
func (c *Client) fixed(cmd byte, n int) ([]byte, error) {
	p, err := c.call(cmd, nil)
	if err != nil {
		return nil, err
	}
	if len(p) < n {
		return nil, fmt.Errorf("box: command %d replied %d bytes, want %d", cmd, len(p), n)
	}
	return p, nil
}

// fileHeader parses `[status][nameLen][name][size u32][sha256]`.
func fileHeader(op string, p []byte) (FileStat, error) {
	if len(p) == 0 {
		return FileStat{}, fmt.Errorf("box: empty %s reply", op)
	}
	switch p[0] {
	case 0:
	case 1:
		return FileStat{}, ErrNotFound
	default:
		return FileStat{}, &StatusError{Op: op, Code: p[0]}
	}
	if !hasFileHeader(p) {
		return FileStat{}, fmt.Errorf("box: short %s reply (%d bytes)", op, len(p))
	}
	rest := p[2+int(p[1]):]
	st := FileStat{Size: int64(binary.LittleEndian.Uint32(rest))}
	copy(st.SHA256[:], rest[4:36])
	return st, nil
}

func hasFileHeader(p []byte) bool { return len(p) >= 2 && len(p) >= 2+int(p[1])+36 }

// Stat reports a file's size on the box, or ErrNotFound. It is fast: the box
// does not read the file. Size is -1 when the box does not report it.
func (c *Client) Stat(name string) (FileStat, error) {
	p, err := c.call(cmdSearchFile, append([]byte{0}, name...))
	if err != nil {
		return FileStat{}, err
	}
	if len(p) > 0 && p[0] == 0 && !hasFileHeader(p) {
		return FileStat{Size: -1}, nil
	}
	st, err := fileHeader(OpSearchFile, p)
	st.SHA256 = [32]byte{} // not computed
	return st, err
}

// Hash returns the size and hash of a file on the box, or ErrNotFound. The box
// reads the whole file (about 7 s per 10 MB), so the wait scales with size, the
// expected size in bytes; -1 when unknown allows for a big story.
func (c *Client) Hash(name string, size int64) (FileStat, error) {
	wait := 2 * time.Minute
	if size >= 0 {
		wait = c.readTime(size)
	}
	p, err := c.callWithin(wait, cmdSearchFile, append([]byte{1}, name...))
	if err != nil {
		return FileStat{}, err
	}
	return fileHeader(OpSearchFile, p)
}

// GetFile downloads a file from the box and checks its hash.
func (c *Client) GetFile(name string) ([]byte, error) {
	p, err := c.call(cmdGetFile, []byte(name))
	if err != nil {
		return nil, err
	}
	st, err := fileHeader(OpGetFile, p)
	if err != nil {
		return nil, err
	}
	data := make([]byte, st.Size)
	for off := 0; off < len(data); off += chunkSize {
		c.extendDeadline()
		if _, err := io.ReadFull(c.r, data[off:min(off+chunkSize, len(data))]); err != nil {
			return nil, err
		}
	}
	if sha256.Sum256(data) != st.SHA256 {
		return nil, fmt.Errorf("box: %s: hash mismatch", name)
	}
	return data, nil
}

// Upload stores data on the box under name. progress, if set, receives the
// number of bytes sent so far.
func (c *Client) Upload(name string, data []byte, progress func(sent int64)) error {
	if len(name) > MaxPayload-37 {
		return fmt.Errorf("box: file name %q is too long", name)
	}
	sum := sha256.Sum256(data)
	payload := append([]byte{byte(len(name))}, name...)
	payload = binary.LittleEndian.AppendUint32(payload, uint32(len(data)))
	payload = append(payload, sum[:]...)
	report := func(n int) {
		if progress != nil {
			progress(int64(n))
		}
	}

	// The box may hash its current copy before answering, and hashes the
	// received data before the final answer.
	wait := c.readTime(int64(len(data)))
	status, err := c.uploadStatus(payload, wait)
	if err != nil {
		return err
	}
	switch status {
	case uploadVerified: // already stored with this hash
		report(len(data))
		return nil
	case uploadReady:
	default:
		return &StatusError{Op: OpUpload, Code: status}
	}

	for off := 0; off < len(data); off += chunkSize {
		end := min(off+chunkSize, len(data))
		c.extendDeadline()
		if _, err := c.conn.Write(data[off:end]); err != nil {
			return err
		}
		report(end)
	}
	if status, err = c.uploadStatus(nil, wait); err != nil {
		return err
	}
	if status != uploadVerified {
		return &StatusError{Op: OpUpload, Code: status}
	}
	return nil
}

// uploadStatus sends the upload request (or, with a nil payload, only waits for
// the final reply) and returns the status byte.
func (c *Client) uploadStatus(payload []byte, wait time.Duration) (byte, error) {
	var p []byte
	var err error
	if payload != nil {
		p, err = c.callWithin(wait, cmdUpload, payload)
	} else {
		p, err = c.reply(cmdUpload, wait)
	}
	if err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, errors.New("box: empty upload reply")
	}
	return p[0], nil
}

// UpdatePlaylist makes the box rebuild its playlist from an uploaded JSON file.
func (c *Client) UpdatePlaylist(name string) error {
	p, err := c.call(cmdUpdatePlaylist, []byte(name))
	if err != nil {
		return err
	}
	if len(p) == 0 {
		return errors.New("box: empty updatePlaylist reply")
	}
	if p[0] != 0 {
		return &StatusError{Op: OpUpdatePlaylist, Code: p[0]}
	}
	return nil
}
