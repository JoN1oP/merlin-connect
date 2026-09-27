# Design

How Merlin Connect works and why. Protocol facts are in [protocol.md](protocol.md).

## Goals

- Manage a Merlin box over its WiFi: see what is on it, add folders and stories, sync with
  one button and a progress bar.
- Official content stays read-only. Custom items can go anywhere, including inside
  official folders.
- Custom stories come back with one sync after the official app removed them.
- Go, with as few dependencies as possible; a quiet, minimal UI (in French).
- Never ask for the user's Merlin account.

## Choices

- **Local web UI.** One static binary serves an embedded HTML/CSS/JS page on
  `127.0.0.1:<random port>` and opens the browser. No cgo, no UI toolkit, folder import
  through the browser's folder picker, live status over SSE. Native toolkits (Wails,
  Fyne, webview) were rejected for their cgo and size; a webview wrapper can still be
  added on top later.
- **One dependency: `golang.org/x/image`**, for scaling and WebP/BMP decoding. The ID3
  reader is hand-written.
- **Custom vs official comes from a manifest.** The box cannot tell them apart (both use
  random UUIDs and identical records), so a custom item is one whose UUID is in our
  manifest. Items added by other tools look official.
- **The manifest lives locally and on the box.** `library.json` is the source of truth and
  is uploaded as `merlin-connect.json` on every sync. A fresh install with an empty
  library adopts the box's manifest and downloads its media; otherwise the local library
  wins.

## Layout

```
cmd/merlin-connect/      main: flags, data dir, server, browser; `probe` subcommand
cmd/fakebox/             dev tool: an in-memory box on 127.0.0.1:50001
internal/box/            protocol client: framing, CRC, commands, file transfer
internal/box/boxtest/    in-memory fake box for tests
internal/wifi/           Joiner: nmcli (Linux) and manual
internal/playlist/       playlist.bin codec and tree model
internal/media/          ID3 reader, 128×128 JPEG normalizer, placeholder, import scanner
internal/library/        manifest and on-disk store
internal/syncer/         merge (official + custom), plan, run with progress
internal/app/            session state machine, keepalive, cover cache, events
internal/web/            HTTP API, SSE, embedded UI
```

`box.Client` deliberately has no methods for reset (10), updateFirmware (7),
setWifiConfiguration (16) or setEnablingHours (29).

## Data directory

`$XDG_DATA_HOME/merlin-connect` (`~/.local/share/merlin-connect`) on Linux,
`os.UserConfigDir()/merlin-connect` elsewhere, or `-data`.

```
library.json                     manifest, source of truth
media/<uuid>.mp3|.jpg            normalized custom media
box/playlist.bin                 last playlist read from the box (offline view)
box/covers/<uuid>.jpg            covers of official items
box/backups/playlist-<unix>.bin  copy taken before every sync
```

### Manifest

```json
{
  "version": 1,
  "updated": "2026-09-26T18:00:00Z",
  "items": [
    { "uuid": "…", "kind": "folder", "title": "Contes de papi",
      "parent": "", "position": 5,
      "image": { "sha256": "…", "size": 6120 } },
    { "uuid": "…", "kind": "story", "title": "Le loup",
      "parent": "<folder uuid>", "position": 0,
      "audio": { "sha256": "…", "size": 4821337 },
      "image": { "sha256": "…", "size": 5530 } }
  ]
}
```

- `parent` is `""` for the root, or any folder's UUID, official or custom.
- Custom items come after the official items of their folder. `position` orders them
  among the custom items only, so changes to official lists never scramble them.
- An item whose parent disappeared re-attaches to the root.

## Flows

### Connect

1. If the box address answers already (WiFi joined by hand, any OS), use it.
2. Linux: scan every 2 s for up to 60 s for `MERLIN_*`, then join with a temporary nmcli
   profile (never-default, no autoconnect), then dial for a few seconds while DHCP
   settles.
3. Ping, read firmware, MAC, battery and sizes.
4. Read `playlist.bin` and `merlin-connect.json`, then download missing covers in the
   background, one at a time, top levels first.
5. Ping every 10 s while idle. A failed ping means the connection is lost.

Disconnecting (also on quit and SIGINT) sends endSynchronization, closes the socket and
deletes the nmcli profile. The app quits 30 s after the last browser tab closed.

### Import

Files are posted with their relative paths. Each directory becomes a folder and each
`.mp3` a story.

- Story cover: an image with the same base name, else the ID3 cover, else the folder's
  `cover.*` or `folder.*`, else a generated placeholder.
- Story title: the ID3 title, else the file name without track number
  (`01 - `, `3. `…). Cut at 63 bytes of JSON text, on a rune boundary.
- Media is normalized (JPEG 128×128, center crop, quality 90) and stored under `media/`.
- Importing again merges: a custom folder with the same title is reused, and a story
  whose audio is already in that folder is skipped.

### Sync

Preconditions: connected, box content read, battery ≥ 15 % or charging, at most 400
items (the official app's limits).

1. **Plan.** Official tree = box tree minus every custom UUID. Merged tree = official
   tree + library items. Custom folders with no story beneath them are dropped (the box
   would read them as stories).
2. **Files.** For each custom `.jpg` and `.mp3`: skip it when the box has the same name
   and size and the box manifest records the same hash; otherwise ask the box to hash its
   copy (slow for audio) and upload only on a mismatch. Progress counts bytes.
3. **Manifest.** Upload `merlin-connect.json`.
4. **Playlist.** Upload the merged tree encoded as `playlist.bin` (a copy of the box's
   previous one was saved to `box/backups/` before the sync started). Official records are kept as they were (type, add and expiry times).
   `updatePlaylist` would be the official way, but the box refuses every playlist with
   `FAVORITES_NOT_FOUND` ([details](protocol.md#favorites-investigation-parked-2026-09-27)).
5. **Verify.** Read `playlist.bin` back, check it is byte-identical and lists every
   custom UUID.

The playlist goes last, so an interrupted sync never points at missing files, and the
next sync resumes: files already stored are skipped. Restoring after an official sync is
the same button.

### Delete

Deleting removes the item from the library and the next playlist. The protocol has no
delete command, so its files stay on the SD card until an official sync removes them.

## Testing

- Unit tests per package: framing and CRC, the `playlist.bin` codec, ID3 v2.2 to 2.4,
  image normalization, scanner rules, library operations, merge and plan.
- `boxtest.FakeBox` speaks the real framing in memory. Sync tests run against it end to
  end: fresh sync, re-sync with nothing to do, restore after a wipe, interruption and
  resume.
- `httptest` for the API and SSE.

## Later

macOS and Windows auto-join (joining by hand works today), transcoding non-MP3 audio,
adopting items added by other tools, and reclaiming the space of deleted stories.
