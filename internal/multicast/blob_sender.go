package multicast

import (
	"context"
	"fmt"
	"os"
)

// BlobStore is the interface used by MakeBlobSenderFunc to fetch image blob paths.
// Implemented by *db.DB.
type BlobStore interface {
	GetBlobPath(ctx context.Context, imageID string) (string, error)
}

// MakeBlobSenderFunc returns a SenderFunc that opens the blob file for the
// session's ImageID and pipes it to the udp-sender binary.
//
// This is the production wire: the scheduler calls the returned SenderFunc
// when a session moves to transmitting.  The Sender handles the actual
// fork/exec of udp-sender.
func MakeBlobSenderFunc(db BlobStore, s *Sender) SenderFunc {
	return func(ctx context.Context, sess Session) error {
		blobPath, err := db.GetBlobPath(ctx, sess.ImageID)
		if err != nil {
			return fmt.Errorf("multicast: get blob path for image %s: %w", sess.ImageID, err)
		}
		if blobPath == "" {
			return fmt.Errorf("multicast: no blob uploaded for image %s", sess.ImageID)
		}

		f, err := os.Open(blobPath)
		if err != nil {
			return fmt.Errorf("multicast: open blob %s: %w", blobPath, err)
		}
		defer f.Close()

		return s.Run(ctx, sess, f)
	}
}
