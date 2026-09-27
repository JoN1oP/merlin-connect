package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"merlin-connect/internal/box"
	"merlin-connect/internal/library"
	"merlin-connect/internal/syncer"
)

func TestMessage(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("Loup: %w", &box.StatusError{Op: box.OpUpload, Code: box.UploadNoSpace}), "Plus assez de place sur Merlin."},
		{&box.StatusError{Op: box.OpUpdatePlaylist, Code: box.PlaylistAudioMissing}, "Merlin n'a pas trouvé un fichier audio. Relancez la synchronisation."},
		{&box.StatusError{Op: box.OpUpdatePlaylist, Code: 5}, "Merlin a refusé l'opération (FAIL_OPEN_JSON)."},
		{&box.StatusError{Op: box.OpUpdatePlaylist, Code: 19}, "Merlin a refusé l'opération (FAVORITES_NOT_FOUND)."},
		{fmt.Errorf("Loup: %w", io.ErrClosedPipe), "Connexion avec Merlin perdue."},
		{&net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}, "Connexion avec Merlin perdue."},
		{io.ErrUnexpectedEOF, "Connexion avec Merlin perdue."},
		{syncer.ErrBoxUnread, "Le contenu de Merlin n'a pas pu être lu. Déconnectez puis reconnectez Merlin avant de synchroniser."},
		{context.Canceled, "Opération annulée."},
		{syncer.ErrTooManyItems, "Trop d'éléments : Merlin en accepte 400 au maximum."},
		{library.ErrEmptyTitle, "Le titre ne peut pas être vide."},
		{errors.New("other"), "other"},
	}
	for _, c := range cases {
		if got := Message(c.err); got != c.want {
			t.Errorf("Message(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
