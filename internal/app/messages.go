package app

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"merlin-connect/internal/box"
	"merlin-connect/internal/library"
	"merlin-connect/internal/syncer"
)

var (
	errNotFound   = errors.New("Merlin introuvable. Maintenez le bouton de l'enceinte 5 secondes, puis réessayez.")
	errLowBattery = errors.New("Batterie de Merlin trop faible (moins de 15 %). Rechargez-la avant de synchroniser.")
	errOffline    = errors.New("Merlin n'est pas connecté.")
)

// Message turns an error into a sentence for the UI.
func Message(err error) string {
	var status *box.StatusError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &status):
		switch {
		case status.Op == box.OpUpload && status.Code == box.UploadNoSpace:
			return "Plus assez de place sur Merlin."
		case status.Op == box.OpUpdatePlaylist && status.Code == box.PlaylistImageMissing:
			return "Merlin n'a pas trouvé une image. Relancez la synchronisation."
		case status.Op == box.OpUpdatePlaylist && status.Code == box.PlaylistAudioMissing:
			return "Merlin n'a pas trouvé un fichier audio. Relancez la synchronisation."
		}
		return "Merlin a refusé l'opération (" + status.Name() + ")."
	case linkLost(err):
		return "Connexion avec Merlin perdue."
	case errors.Is(err, context.Canceled):
		return "Opération annulée."
	case errors.Is(err, syncer.ErrBoxUnread):
		return "Le contenu de Merlin n'a pas pu être lu. Déconnectez puis reconnectez Merlin avant de synchroniser."
	case errors.Is(err, syncer.ErrTooManyItems):
		return "Trop d'éléments : Merlin en accepte 400 au maximum."
	case errors.Is(err, library.ErrEmptyTitle):
		return "Le titre ne peut pas être vide."
	case errors.Is(err, library.ErrBadParent):
		return "Impossible de placer cet élément ici."
	case errors.Is(err, library.ErrNotFound):
		return "Élément introuvable."
	}
	return err.Error()
}

// linkLost reports errors that mean the WiFi link to the box broke.
func linkLost(err error) bool {
	var netErr net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.As(err, &netErr)
}
