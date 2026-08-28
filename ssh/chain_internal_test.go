package ssh

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingCloser notes when it is closed, so the order a chain tears
// itself down in can be watched without a live connection behind it.
type recordingCloser struct {
	name string
	log  *[]string
	mu   *sync.Mutex

	// block, when set, never returns — an inner connection whose close is
	// a write into a link nobody is draining.
	block chan struct{}
}

func (r recordingCloser) Close() error {
	if r.block != nil {
		<-r.block
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	*r.log = append(*r.log, r.name)

	return nil
}

// TestChainClosesOutermostFirst pins the teardown order.
//
// It is the reverse of the intuitive one, and the reason is invisible from
// the outside: only the outermost connection owns a socket, and every
// inner one is a channel whose close is a write over the hop beneath it.
// Reversing this passes every end-to-end test in the package, because a
// healthy chain does not care — which is exactly why the order needs a
// test that looks at it directly.
func TestChainClosesOutermostFirst(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		closed []string
	)

	closer := func(name string) recordingCloser {
		return recordingCloser{name: name, log: &closed, mu: &mu}
	}

	c := &chain{hops: []hop{
		{closer: closer("outermost"), agent: closer("outermost agent")},
		{closer: closer("middle"), agent: closer("middle agent")},
		{closer: closer("target"), agent: closer("target agent")},
	}}

	require.NoError(t, c.Close())

	assert.Equal(t, []string{
		"outermost", "middle", "target",
		"outermost agent", "middle agent", "target agent",
	}, closed, "the socket at the head of the chain must be released first")
}

// TestChainCloseDoesNotWaitOnAnInnerConnection pins what the order is for.
//
// An inner close is a write over the hop beneath it, and a link that has
// died absorbs writes without ever answering. Closing from the target
// inwards would block there — holding Close open on a connection whose
// whole problem is that it is already gone.
func TestChainCloseDoesNotWaitOnAnInnerConnection(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		closed []string
	)

	wedged := make(chan struct{})

	c := &chain{hops: []hop{
		{closer: recordingCloser{name: "outermost", log: &closed, mu: &mu}},
		{closer: recordingCloser{name: "inner", log: &closed, mu: &mu, block: wedged}},
	}}

	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = c.Close()
	}()

	// Close as a whole cannot finish while an inner connection is wedged.
	// What must not wait on it is the socket, which is the close that
	// unblocks everything else.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(closed) > 0 && closed[0] == "outermost"
	}, 5*time.Second, 10*time.Millisecond,
		"the socket was not released before an inner connection blocked the teardown")

	close(wedged)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return once the inner connection let go")
	}
}

// TestHopsAreDialedInTheOrderTheyAreNamed pins what a chain of options
// means. Repeated jump hosts read as OpenSSH's -J list reads: the first
// named is the first dialed, and the target is reached last, through the
// one named nearest it.
//
// The stored form runs the other way — each hop names what it is reached
// through — so the reading and the storage disagree by construction, and
// only a test says which one the caller gets.
func TestHopsAreDialedInTheOrderTheyAreNamed(t *testing.T) {
	t.Parallel()

	// ssh -J a,b target
	repeated := &Config{Host: "target"}
	WithJumpHost("a")(repeated)
	WithJumpHost("b")(repeated)

	// The same chain, said by nesting instead.
	nested := &Config{Host: "target"}
	WithJumpHost("b", WithJumpHost("a"))(nested)

	for name, cfg := range map[string]*Config{"repeated": repeated, "nested": nested} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			specs := hops(cfg)
			require.Len(t, specs, 3, "a target behind two jump hosts is three connections")

			assert.Equal(t, "a:22", specs[0].addr, "the first jump host named is dialed from here")
			assert.Empty(t, specs[0].through, "the first hop is not reached through anything")
			assert.True(t, specs[0].jump, "it is a hop, not the target")

			assert.Equal(t, "b:22", specs[1].addr, "the second jump host named comes next")
			assert.Equal(t, "a:22", specs[1].through, "and is reached through the first")
			assert.True(t, specs[1].jump)

			assert.Equal(t, "target:22", specs[2].addr, "the target is last")
			assert.Equal(t, "b:22", specs[2].through, "reached through the jump host named nearest it")
			assert.False(t, specs[2].jump, "the target is not a hop")
		})
	}
}

// TestJumpHostCarriesItsOwnSettings checks a hop is configured by its own
// options and inherits nothing from the target, which is what makes
// per-hop credentials and host-key policy possible at all.
func TestJumpHostCarriesItsOwnSettings(t *testing.T) {
	t.Parallel()

	cfg := &Config{Host: "target", User: "root", Port: 2200}
	WithJumpHost("bastion", WithUser("jump"), WithPort(2222))(cfg)

	require.NotNil(t, cfg.Jump)

	assert.Equal(t, "jump", cfg.Jump.User, "the hop keeps the user it was given")
	assert.Equal(t, "root", cfg.User, "and does not reach back into the target's")

	specs := hops(cfg)
	require.Len(t, specs, 2)

	assert.Equal(t, "bastion:2222", specs[0].addr, "the hop is dialed on its own port")
	assert.Equal(t, "target:2200", specs[1].addr, "and the target on the target's")
}

// TestJumpOptionIsSafeToReuse checks an option value applied to more than
// one connection gives each its own hop.
//
// Options are values, and callers keep them in slices and apply them
// repeatedly. A hop built once and shared would let a change to one
// connection's chain reach into another's.
func TestJumpOptionIsSafeToReuse(t *testing.T) {
	t.Parallel()

	shared := WithJumpHost("bastion", WithUser("jump"))

	first := &Config{Host: "one"}
	second := &Config{Host: "two"}

	shared(first)
	shared(second)

	require.NotNil(t, first.Jump)
	require.NotNil(t, second.Jump)

	assert.NotSame(t, first.Jump, second.Jump, "two connections were handed the same hop to share")

	first.Jump.User = "changed"
	assert.Equal(t, "jump", second.Jump.User, "changing one connection's hop changed another's")
}

// TestHopNamingIdentifiesTheFailingConnection pins how a failure says
// which connection it came from.
//
// The case worth naming is a refusal reported against the target's
// address when the jump host is what refused: read without the hop, it
// says the target is down when the target was never contacted.
func TestHopNamingIdentifiesTheFailingConnection(t *testing.T) {
	t.Parallel()

	direct := hops(&Config{Host: "target"})
	require.Len(t, direct, 1)
	assert.Empty(t, direct[0].describe(),
		"a direct connection needs no name: the caller typed the only address involved")

	jumped := &Config{Host: "target"}
	WithJumpHost("a")(jumped)
	WithJumpHost("b")(jumped)

	specs := hops(jumped)
	require.Len(t, specs, 3)

	assert.Equal(t, "jump a:22", specs[0].describe(),
		"a hop dialed from here is named as the hop it is")
	assert.Equal(t, "jump b:22 through jump a:22", specs[1].describe(),
		"a hop reached through another names both")
	assert.Equal(t, "target:22 through jump b:22", specs[2].describe(),
		"and so does the target, so a refusal is not read as the target's own")
}

// TestValidateRejectsChainsThatCannotBeDialed checks a misconfigured hop
// is named before anything is dialed, rather than surfacing later as a
// failure against an address the caller cannot place.
func TestValidateRejectsChainsThatCannotBeDialed(t *testing.T) {
	t.Parallel()

	t.Run("target host is still required", func(t *testing.T) {
		t.Parallel()

		assert.ErrorContains(t, (&Config{}).validate(), "ssh: host is required")
	})

	t.Run("a hop must name a host", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Host: "target", Jump: &Config{Host: "  "}}

		assert.ErrorContains(t, cfg.validate(), "ssh: jump host is required",
			"the empty hop must be named as the hop it is")
	})

	// Both halves of a pasted ssh_config value. The host is joined to its
	// port before it is dialed, so a host that already carries one becomes
	// "[bastion:2222]:22" and fails to resolve for a reason nothing
	// explains — which is the outcome this check exists to prevent.
	t.Run("a pasted user@host:port is refused", func(t *testing.T) {
		t.Parallel()

		for name, tc := range map[string]struct{ host, says string }{
			"user":      {host: "deploy@bastion", says: "WithUser"},
			"port":      {host: "bastion:2222", says: "WithPort"},
			"both":      {host: "deploy@bastion:2222", says: "WithUser"},
			"ipv6 port": {host: "[2001:db8::1]:2222", says: "WithPort"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cfg := &Config{Host: "target"}
				WithJumpHost(tc.host)(cfg)

				err := cfg.validate()
				require.Error(t, err, "a pasted %q was accepted", tc.host)

				assert.ErrorContains(t, err, tc.says, "the error must say where the part belongs")
			})
		}
	})

	// An address that is only colons is still a host, and must survive a
	// check written to catch a port.
	t.Run("a bare IPv6 address is a host", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Host: "target"}
		WithJumpHost("2001:db8::1")(cfg)

		assert.NoError(t, cfg.validate(), "an IPv6 jump host was read as carrying a port")
	})

	t.Run("a chain that loops is refused", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Host: "target"}
		cfg.Jump = cfg

		assert.ErrorContains(t, cfg.validate(), "loops back on itself",
			"a hand-built cycle must be reported, not followed")
	})
}
