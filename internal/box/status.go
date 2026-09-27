package box

import (
	"errors"
	"fmt"
)

// ErrNotFound reports that the box has no file with the requested name.
var ErrNotFound = errors.New("box: file not found")

// Operations that reply with a status byte.
const (
	OpUpload         = "upload"
	OpSearchFile     = "searchFile"
	OpGetFile        = "getFile"
	OpUpdatePlaylist = "updatePlaylist"
)

// Status codes worth reacting to (full lists in docs/protocol.md).
const (
	UploadNoSpace        = 2
	PlaylistImageMissing = 17
	PlaylistAudioMissing = 18
)

var statusNames = map[string][]string{
	OpUpload: {"SUCCESS", "SHA256_VALID", "NOT_ENOUGH_SPACE", "FILENAME_TOO_LARGE",
		"SHA256_INVALID", "BAD_LENGTH_CMD", "TIMEOUT", "FAIL_CREATE_FILE"},
	OpSearchFile: {"SUCCESS", "FILE_NOT_FOUND", "FAIL_OPEN"},
	OpUpdatePlaylist: {"SUCCESS", "BAD_FILENAME_LEN", "BAD_EXTENSION_FILE", "FILE_NOT_FOUND",
		"FAIL_MINIFIER", "FAIL_OPEN_JSON", "FAIL_OPEN_BINARY", "ROOT_ELEMENT_IS_NOT_ARRAY",
		"BAD_ROOT_CHILD_QUANTITY", "MISSING_FIELD", "BAD_UUID_FIELD", "UUID_TOO_LARGE",
		"BAD_TITLE_FIELD", "TITLE_TOO_LARGE", "FAIL_ADD_ELEMENT", "FAIL_RENAME_BIN_FILE",
		"INVALID_TYPE_IN_BINARY_FILE", "IMAGE_NOT_FOUND", "MUSIC_NOT_FOUND", "FAVORITES_NOT_FOUND",
		"TOO_MANY_FAVORITES", "FAIL_CREATE_FAV_FILE", "FAIL_OPEN_FAV_FILE", "BAD_FAV_FORMAT"},
}

// StatusError is a failure status returned by the box.
type StatusError struct {
	Op   string
	Code byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("box: %s failed: %s (%d)", e.Op, e.Name(), e.Code)
}

// Name is the status's name from the official app, e.g. "FAIL_OPEN_JSON".
func (e *StatusError) Name() string {
	if names := statusNames[e.Op]; int(e.Code) < len(names) {
		return names[e.Code]
	}
	return "UNKNOWN"
}
