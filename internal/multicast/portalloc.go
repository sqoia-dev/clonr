package multicast

import (
	"fmt"
	"net"
	"sync"
)

// portAlloc allocates sender ports from the UDP range 9000–9999 as defined by
// udpcast convention (D1).  It checks the active session set to avoid collisions
// within a single serverd lifetime.
//
// Ports are released when a session transitions to a terminal state.
type portAlloc struct {
	mu   sync.Mutex
	used map[int]string // port → session ID
}

func newPortAlloc() *portAlloc {
	return &portAlloc{used: make(map[int]string)}
}

const (
	portRangeMin = 9000
	portRangeMax = 9999
)

// Acquire returns a free port in [9000, 9999].
// Returns an error when all ports in the range are in use.
func (a *portAlloc) Acquire(sessionID string) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for p := portRangeMin; p <= portRangeMax; p++ {
		if _, taken := a.used[p]; !taken {
			a.used[p] = sessionID
			return p, nil
		}
	}
	return 0, fmt.Errorf("multicast/portalloc: all ports %d–%d in use", portRangeMin, portRangeMax)
}

// Release frees a port previously acquired for sessionID.
// No-op if the port is not held.
func (a *portAlloc) Release(port int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.used, port)
}

// groupForSession derives a multicast group address for a session from the
// session ID using a hash mod 254 (+1) algorithm so the group is deterministic
// per session but spread evenly across the /24.
//
// groupBase must be a valid IPv4 address (e.g. "239.255.42.0").  The last octet
// is replaced; the first three octets are kept.
func groupForSession(sessionID string, groupBase string) (string, error) {
	ip := net.ParseIP(groupBase).To4()
	if ip == nil {
		return "", fmt.Errorf("multicast/portalloc: invalid group base %q", groupBase)
	}
	// Hash the session ID bytes to an octet in [1, 254].
	var sum int
	for _, b := range []byte(sessionID) {
		sum += int(b)
	}
	octet := (sum % 254) + 1
	return fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], octet), nil
}
