# Merlin box WiFi protocol

Reference for talking to the Merlin box over its own WiFi access point.

Source: official Android app `com.hellomerlin` v1.20.8, decompiled with jadx on
2026-09-26. Class names below are the obfuscated names in that build (`lb3` = protocol
client, `tp4` = speaker repository, `ka5` = sync worker).

Evidence levels:

- **HW**: exercised against a real box (`merlin-connect probe` and live sessions of
  2026-09-26/27, firmware 2.1.16).
- **Code**: traced in the app's code, not yet run against a box.
- **Guess**: inferred, not yet confirmed on hardware.

## Access point

| Property | Value | Evidence |
|----------|-------|----------|
| SSID | `MERLIN_` + per-box suffix | HW |
| Security | WPA2-PSK, passphrase `MERLIN_APP` | HW |
| Box address | `192.168.4.1`, DHCP for clients | HW |
| Service | TCP `50000`, plain, no TLS | HW |

The access point exists only after the transfer button is held for about 5 seconds.
With a ping every 10 s the link stayed up for the whole 3-minute probe hold (HW).
Linux join recipe (proven): create an explicit `nmcli` profile with `wifi-sec.key-mgmt
wpa-psk`, `ipv4.never-default yes`, `connection.autoconnect no`, bring it up, and delete
it afterwards (`internal/wifi`).

## Framing (HW)

```
[len u8] [cmd u8] [payload ...] [crc32 u32 LE]
len = payload length + 5
crc = CRC-32/MPEG-2 over cmd + payload (poly 0x04C11DB7, init 0xFFFFFFFF, MSB-first,
      no reflection, no final xor)
```

- `len` is one byte, so a payload is at most 250 bytes. File names must fit.
- Replies use the same framing and echo the request's command id.
- A reply with command id `255` means "unknown command"; its payload byte 0 is a status.
- A reply with a bad CRC is logged and dropped by the app.
- File **contents** are never framed: they follow a reply as a raw byte stream (see
  getFile and upload).

## Commands

| id | SDK method | Request payload | Reply payload | Evidence |
|----|------------|-----------------|---------------|----------|
| 1 | sendFile / uploadPlaylist / sendCommandFile | `[nameLen][name][size u32 LE][sha256 32B]` | `[status d16]` (see upload) | Code |
| 2 | ping | none | none | HW |
| 3 | getAvailableSpace | none | `u32 LE` bytes | HW |
| 4 | getTotalSize | none | `u32 LE` bytes (capped `0xFFFFFFFE`) | HW |
| 5 | getFirmwareVersion | none | `major u8, minor u8, patch u16 LE` | HW |
| 6 | updatePlaylist | file name (UTF-8, no length prefix) | `[status x06]` | Code |
| 7 | updateFirmware | file name | status | Code (**never send**) |
| 8 | setDate | date bytes | none | Code |
| 9 | endSynchronization | none | none (ack) | Code |
| 10 | reset | none | none | Code (**never send**) |
| 13 | getFile | file name | `[status][nameLen][name][size u32 LE][sha256 32B]`, then `size` raw bytes | HW (`playlist.bin`, covers) |
| 14 | getBatteryState | none | `[level %][charging u8 != 0]` | Code |
| 16 | setWifiConfiguration | ssid/pass | `[status nu4]` | Code (not needed) |
| 27 | getDate | none | `u32 LE` unix seconds | Code |
| 29 | setEnablingHours | 4 × `(hour, minute)` bytes | `[status ku4]` | Code (not needed) |
| 30 | getMacAddress | none | 6 bytes | HW |
| 31 | searchFile | `[flag u8][name]`, flag=1 asks for the hash (the official app sends 0 for a plain existence check; the reply to 0 is unverified on hardware) | `[status wp4][nameLen][name][size u32 LE][sha256 32B]` | Code |

Easy to get wrong: **9 is endSynchronization**, not a connect-time status. **Upload
starts with 1**, not 31. 31 is searchFile.

### Status enums

- `d16` (upload): `0 SUCCESS` (go ahead, stream now), `1 SHA256_VALID` (file stored and
  verified, or already present), `2 NOT_ENOUGH_SPACE`, `3 FILENAME_TOO_LARGE`,
  `4 SHA256_INVALID`, `5 BAD_LENGTH_CMD`, `6 TIMEOUT`, `7 FAIL_CREATE_FILE`.
- `wp4` (searchFile): `0 SUCCESS` (found), `1 FILE_NOT_FOUND`, `2 FAIL_OPEN`.
- `x06` (updatePlaylist): `0 SUCCESS`, `1 BAD_FILENAME_LEN`, `2 BAD_EXTENSION_FILE`,
  `3 FILE_NOT_FOUND`, `4 FAIL_MINIFIER`, `5 FAIL_OPEN_JSON`, `6 FAIL_OPEN_BINARY`,
  `7 ROOT_ELEMENT_IS_NOT_ARRAY`, `8 BAD_ROOT_CHILD_QUANTITY`, `9 MISSING_FIELD`,
  `10 BAD_UUID_FIELD`, `11 UUID_TOO_LARGE`, `12 BAD_TITLE_FIELD`, `13 TITLE_TOO_LARGE`,
  `14 FAIL_ADD_ELEMENT`, `15 FAIL_RENAME_BIN_FILE`, `16 INVALID_TYPE_IN_BINARY_FILE`,
  `17 IMAGE_NOT_FOUND`, `18 MUSIC_NOT_FOUND`, `19 FAVORITES_NOT_FOUND`,
  `20 TOO_MANY_FAVORITES`, `21 FAIL_CREATE_FAV_FILE`, `22 FAIL_OPEN_FAV_FILE`,
  `23 BAD_FAV_FORMAT` (names recovered with jadx's simple mode; the default pass mangles
  15 to 23).

## Flows

### Upload a file (Code)

1. Send `1` with `[nameLen][name][size][sha256(contents)]`.
2. Wait for the reply `1`:
   - `SUCCESS (0)`: write the raw contents to the socket (the app writes 100 KB chunks,
     no per-chunk ack), then wait for a second reply `1`, which must be
     `SHA256_VALID (1)`.
   - `SHA256_VALID (1)`: nothing to send, done.
   - anything else: error.

### Download a file (Code)

1. Send `13` with the file name.
2. The reply `13` gives status (0 = ok), size and sha256.
3. Read exactly `size` raw bytes, then check their sha256.

### Check a file is already on the box (Code)

Send `31` with `[1][name]`. On `SUCCESS` compare the returned sha256 with the local
file's. The official sync does this before every upload and logs "File was found on
device! Skipping".

### Official sync, as the app does it (Code)

1. Connect, ping. Read firmware, MAC, battery, sizes, date.
2. Refuse if battery < 15 %, content items > 400, or not enough free space.
3. For each file in the backend's `fileList`: searchFile with the hash, upload if missing.
4. Upload the playlist JSON as `playlist-<syncId>.json` (command 1).
5. `updatePlaylist("playlist-<syncId>.json")` (command 6).
6. Set enabling hours and date (errors ignored).
7. `endSynchronization` (command 9), then disconnect.

The app has **no delete command**. An official sync replaces the playlist, but files it
does not reference are presumably left on the SD card (Guess).

## Playlist JSON (HW, partly)

The box builds its own `playlist.bin` from a JSON file. The official app uploads the
backend's `playlistText` verbatim, so the app code does not show the schema; the probe
found it on the real box (2026-09-26):

```json
[
  { "uuid": "…", "title": "Histoires", "add_time": 1787835495, "limit_time": 0, "child": [
      { "uuid": "…", "title": "Au lit !", "add_time": 1787835495, "limit_time": 0 }
  ]}
]
```

- `uuid`, `title`, `add_time`, `limit_time` are required on every node: without the
  two dates the box answers `MISSING_FIELD` (HW).
- The 64-byte title limit applies to the title's **JSON text**: an `&` escaped as
  `\u0026` pushed a 64-byte official title over it (`TITLE_TOO_LARGE`, HW). Write the
  JSON without HTML escaping.

- The root is an array (`ROOT_ELEMENT_IS_NOT_ARRAY`).
- A node with `child` is a folder. A node without one is a story that needs
  `<uuid>.mp3`. Every node needs `<uuid>.jpg` (errors 17/18). (Guess on the exact rule.)
- How the favorites folder must appear is unknown: see
  [Favorites investigation](#favorites-investigation-parked-2026-09-27).

## playlist.bin (HW, via SD card)

A sequence of 152-byte little-endian records:

| Offset | Size | Field |
|--------|------|-------|
| 0 | 2 | id |
| 2 | 2 | parent_id (0 = none) |
| 4 | 2 | order among siblings |
| 6 | 2 | nb_children |
| 8 | 2 | fav_order |
| 10 | 2 | type: 1 root, 2 folder, 4 story, 10 favorites, 36 story with image |
| 12 | 4 | limit_time |
| 16 | 4 | add_time (unix seconds) |
| 20 | 1 + 64 | uuid (length byte + zero-padded) |
| 85 | 1 + 66 | title (length byte + zero-padded, UTF-8) |

Files on the card: `playlist.bin`, `<uuid>.mp3`, `<uuid>.jpg` (128×128 JPEG), `*.cfg`.

Observed on the reference box over WiFi (`getFile("playlist.bin")` works, HW):

- 292 records: 1 root, 12 folders, 1 favorites folder, 278 stories (163 of type 4,
  115 of type 36). UUIDs are 36-character UUID strings.
- **Favorites are not child records.** `Merlin_favorite` (type 10) has `nb_children`
  12 but no record points to it; each favorite story carries `fav_order` 1..n. A story
  filed in two folders carries its `fav_order` on both records. The codec therefore
  lists favorites as references under the favorites folder, and the box JSON sends
  them as that folder's children (Guess until the probe round trip confirms it).
- Titles on that box were plain ASCII, but only because another tool had rewritten
  the card: after an official sync the titles have accents ("Le rêve de Noé"). The box
  keeps UTF-8 titles.
- `limit_time` is a Unix expiry date (2027-01-01, 2027-07-01) on subscription content
  ("Une histoire et...Oli", "Dernier ajouts"). The JSON has no field for it; it is the
  box/backend's business.
- The same story can be filed in several folders (9 UUIDs appear twice).

## Media rules

- Audio: MP3 only (the box looks for `<uuid>.mp3`).
- Image: JPEG, 128×128, center-crop fill, quality 90.
- Titles keep their accents (official titles have them); the limit is 63 bytes of JSON
  text, cut on a rune boundary.

## Live session findings (2026-09-26, reference box, firmware 2.1.16)

- **One client at a time.** A second TCP connection is accepted but never answered.
  Transfer mode survives dropping the connection and dialling again.
- **Scan results can be stale.** NetworkManager listed `MERLIN_…` before the box's
  network was up, so the first join failed ("network could not be found" / timeout);
  retrying the join works.
- **searchFile with the hash flag hashes the file on the box:** ~7 s for a 10 MB MP3,
  instant for a cover.
- **Title limit: 63 bytes** of JSON text, measured without changing anything (a test
  playlist with an N-byte title followed by a node missing its dates is refused with
  `TITLE_TOO_LARGE` or `MISSING_FIELD`).
- **Validation order:** fields and titles while parsing, node by node; then images and
  audio for every node (`IMAGE_NOT_FOUND` / `MUSIC_NOT_FOUND`), which a trailing invalid
  node never reaches; then favorites.
- **Every node needs `<uuid>.jpg`.** 115 stories added earlier by another tool had
  none; placeholder covers were uploaded for them.
- **An official sync deletes files the new playlist does not reference** (audio,
  covers and folder covers of stories added outside the official app were gone
  afterwards). Custom
  media must be re-uploaded after an official sync.
- **Writing `playlist.bin` directly is not enough:** the box reads the new structure
  (menus appear) but the entries have no title, cover or sound, even after a restart.
  `updatePlaylist` looked required, but it is blocked (next point), so the sync
  currently writes `playlist.bin` directly anyway (see [design](design.md#sync)).
  Whether the box then plays the custom stories is not yet confirmed.
- **Open blocker: `FAVORITES_NOT_FOUND` (19).** Every `updatePlaylist` of a complete
  playlist (all assets present) is refused with 19. Parked on 2026-09-27; see
  [Favorites investigation](#favorites-investigation-parked-2026-09-27).

## Favorites investigation (parked 2026-09-27)

What is known:

- **The box builds the new playlist before refusing it.** A failed update leaves
  `playlist.bin.tmp` on the SD card: our tree converted to records (root 1, folders 2,
  stories 4), readable with getFile. Only the favorites step fails.
- **Our `Merlin_favorite` node always becomes a plain folder (type 2), never 10.** The
  box ignores a `type` field (tested: `10`, numeric types on every node, `"favorite"`,
  `"favorites"`, `"FAVORITE"`, `"FAVORITES"`, `"MERLIN_FAVORITE"`). Title and UUID
  identical to the official node, empty or listing the favorites, moved last, or left
  out (with or without "Dernier ajouts"): always 19. Adding `Merlin_discover`: 19.
- **No favorites file exists.** A full SD card dump holds only `playlist.bin`, covers,
  audio, `sleep.cfg`, `wifi.cfg`, `sta_wifi.json` (`[]`), and our leftover probe JSON
  and `playlist.bin.tmp`. Favorites live in `playlist.bin` only (`fav_order`, type-10
  node). About 50 guessed favorites file names are all missing on the box.
- **The official app has no playlist logic.** Its sync (`POST
  https://api.hello-merlin.com/api/v1/synchros`, body `{"status":"PENDING",
  "type":"TRANSFERT",…}`, Keycloak password login with `client_id=app`) returns
  `playlistText`, which the app uploads unchanged as `playlist-<id>.json` and applies
  with `updatePlaylist`. So only Merlin's server knows how the favorites node is written.
- The app reads listening history with a virtual file, `getFile("get_listening.cmd")`
  (43-byte records, also stored as `listening.bin`), and clears it with
  `sendCommandFile("clean_listening.cmd")`.

Leads, in order of cost:

1. Read one `playlistText` from the server (needs the user's Merlin login: deliberately
   not pursued, the app should not ask for Merlin credentials).
2. Get the firmware (URL is only given in the sync answer) and read its JSON parser.
3. Keep guessing keys on the favorites node, checking `playlist.bin.tmp` after each try
   for a type-10 record.
