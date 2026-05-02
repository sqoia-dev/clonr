package multicast

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// StubSender is a no-op SenderFunc used in Commit 1 where the actual
// udp-sender binary is not yet wired.  It sleeps 5 seconds to simulate
// a short transmission, then returns nil so the scheduler transitions
// to complete.
//
// Replaced by sender.Run in Commit 2 (#157).
func StubSender(_ context.Context, s Session) error {
	log.Info().
		Str("session_id", s.ID).
		Str("image_id", s.ImageID).
		Msg("multicast: stub sender invoked — no actual transmission (Commit 1 placeholder)")
	time.Sleep(5 * time.Second)
	return nil
}
