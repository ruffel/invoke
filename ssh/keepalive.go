package ssh

import (
	"context"
	"time"

	"golang.org/x/crypto/ssh"
)

// probeGraceFloor is the least time a probe gets to answer.
const probeGraceFloor = time.Second

// keepAlive is the loop that probes a connection for as long as it lives,
// so a connection that dies without a close — a dropped link, a NAT
// timeout — is discovered rather than leaving the next operation blocked
// on a socket nobody is serving.
//
// A probe the server does not answer within one interval is that
// discovery. The chain is closed on the spot, which is what unblocks
// everything still waiting on the dead link: running Waits, transfers,
// and Close itself. Without the bound, a probe on a black-holed
// connection would block in its own send until the kernel gave up on
// the socket — and hold Close hostage with it.
//
// One loop covers a whole chain. A probe to the target crosses every hop
// on the way, so a jump host that dies silently strands the probe exactly
// as a dead target does, and is discovered on the same interval.
type keepAlive struct {
	cancel context.CancelFunc

	// done closes once the loop has actually stopped, so Close never
	// outlives its own goroutine.
	done chan struct{}
}

// startKeepAlive begins probing c's target every interval, or returns nil
// when interval disables probing.
func startKeepAlive(c *chain, interval time.Duration) *keepAlive {
	if interval <= 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	k := &keepAlive{cancel: cancel, done: make(chan struct{})}

	// The grace is how long an answer may take before the link is
	// declared dead. It is floored: an answer bound tighter than a
	// second measures scheduling noise, not the link.
	grace := max(interval, probeGraceFloor)

	go func() {
		defer close(k.done)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !probeAlive(ctx, c.target(), grace) {
					// The whole chain goes, not just the target: a link
					// that died takes every connection riding on it with
					// it, and the socket underneath would otherwise be
					// held until Close.
					_ = c.Close()

					return
				}
			}
		}
	}()

	return k
}

// stop ends the loop and waits for it to finish, so no probe outlives the
// caller. The wait is bounded: a probe already in flight answers or times
// out within one grace. A nil keepAlive, for a connection that is not
// probed, has nothing to stop.
func (k *keepAlive) stop() {
	if k == nil {
		return
	}

	k.cancel()
	<-k.done
}

// probeAlive sends one keepalive on client and reports whether the server
// answered within bound. An answer slower than the probing cadence is
// indistinguishable from none; a stopped loop no longer cares either
// way.
func probeAlive(ctx context.Context, client *ssh.Client, bound time.Duration) bool {
	answered := make(chan error, 1)

	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		answered <- err
	}()

	timer := time.NewTimer(bound)
	defer timer.Stop()

	select {
	case err := <-answered:
		return err == nil
	case <-timer.C:
		return false
	case <-ctx.Done():
		// Stopping. Liveness no longer matters, but a probe already on
		// the wire is seen out first — no probe outlives Close — with
		// the same bound, so a dead link cannot hold this open either.
		select {
		case <-answered:
		case <-timer.C:
		}

		return true
	}
}
