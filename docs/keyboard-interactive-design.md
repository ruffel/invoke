# Keyboard-interactive authentication

Status: proposed. Supersedes the feature request it was written against.
That request is wrong in four places: two would ship outright bugs (its
guard breaks a legal login, and its fallback disables the option it ships
alongside), and two are safety claims that do not hold (the guard does not
stop the leak it is for, and the fallback silently reorders authentication
for existing callers).

## In brief

Some SSH servers will not take a password sent straight at them. They ask
for it as a question instead — the same password, delivered by a
different mechanism, which the protocol calls keyboard-interactive. This
library only knows how to send passwords straight at a server, so against
those hosts it fails, with an error that does not say why. `ssh` at a
terminal works against the same host, because it asks the person the
question.

Three changes:

1. **Say what the server accepts when authentication fails.** Today the
   message is `attempted methods [none]`, which names neither what we
   offered nor what the server wanted. It should name both.

2. **Teach `WithPassword` to answer the question form.** If the server
   asks questions instead of taking a password, answer the first question
   with the password — and refuse to answer anything after it. That
   refusal is what keeps the password out of a second-factor prompt.

3. **Add `WithKeyboardInteractive(fn)` for everything else.** When the
   server asks for something that is not your password, you supply the
   function that answers it.

Most callers only ever touch `WithPassword`, and it keeps meaning what it
means today: here is my password, log me in. The rest of this document is
why each of those is shaped the way it is, and what was measured to rule
out the obvious alternatives.

## The problem

A host configured with `PasswordAuthentication no` and
`KbdInteractiveAuthentication yes` never advertises the `password`
method. A caller configuring `WithPassword` against it is not refused —
nothing is ever sent — and the connection fails with:

```
transport failure during handshake: ssh: handshake failed: ssh: unable to
authenticate, attempted methods [none], no supported methods remain
```

The same host, from the same machine, takes `ssh user@host` and a typed
password. That gap is the whole complaint, and it has two independent
halves that are worth separating because they have different fixes and
different risks.

The first half is diagnosis. The message names neither the method this
connection offered — `password` never enters x/crypto's `tried` list,
because it was never attempted — nor the method the server wanted.
OpenSSH's own client, against the same host, says
`Permission denied (publickey,keyboard-interactive)`. It names the answer.

The second half is capability. Even a caller who diagnoses it correctly
has nothing to reach for: `authMethods` composes password, key, and agent
only ([ssh/auth.go:22](../ssh/auth.go)).

## What is actually true

Everything below was established by running the pinned x/crypto
(v0.55.0) and a real PAM-backed sshd, not by reading documentation.

**A challenge may carry no questions, and refusing one breaks a login
that had already succeeded.** RFC 4256 provides for a round that only
says something. It can arrive at either end of a conversation — a PAM
host commonly sends one once it has accepted the password, and a module
that has something to say up front sends one before asking anything.
x/crypto invokes the callback for it with `questions == nil,
echos == nil` and requires exactly zero answers back
(`client_auth.go:641-662`). Its own doc comment says so in as many words.
Answering it with an error fails the handshake; answering it with an
answer fails on the count.

**A conversation may hold several rounds, and shape does not distinguish
them.** OpenSSH's sshd emits at most one prompt per round —
`auth-pam.c`'s `sshpam_query()` hardcodes `*num = 1` — so a second factor
does not arrive as a second *question*, it arrives as a second *round*,
with the same one-question echo-off shape as the password prompt. A
`"Verification code: "` prompt and a `"Password: "` prompt are
indistinguishable by anything but their text, which is server-authored,
`gettext`-wrapped and freely reconfigured.

**Returning an error mid-conversation desynchronises the stream.** The
client returns without sending `SSH_MSG_USERAUTH_INFO_RESPONSE`, so its
next write is read by the server as the missing response. A correct
password configured alongside did not log in.

**Method selection is client-side and deduplicated by RFC name.**
x/crypto walks `config.Auth` in order and takes the first entry whose
method name the server advertises, skipping any name already tried
(`client_auth.go:135-146`). Two `keyboard-interactive` methods cannot
coexist: only the first runs.

**The server's accepted methods are reachable.** v0.55.0 added
`ClientConfig.AuthCallback`, whose `ClientAuthContext.AllowedMethods`
carries the server's own list, on the connection that is already failing.
Returning `(nil, nil)` falls through to the ordinary selection, so
installing it changes nothing.

## Design

Three changes, described separately because they are three distinct
decisions. They ship together as one PR — see **Work**.

### 1. Name the methods the server accepts

`authMethods` returns a `credentials` value carrying the methods, the RFC
names they are offered under, and the agent connection they hold. The
names are kept because `ssh.AuthMethod`'s own `method()` is unexported,
so nothing else can say what this side offered.

`dial` installs an observer on the client config that records
`AllowedMethods` before the selection it does not influence:

```go
// Purely an observer: returning no method falls through to the ordinary
// selection, and this is the only place the server's own terms are
// visible before they are thrown away.
AuthCallback: func(actx *ssh.ClientAuthContext) (ssh.AuthMethod, error) {
    accepted = actx.AllowedMethods

    return nil, nil
},
```

A handshake failure with a non-empty `accepted` is decorated:

```
ssh: the server accepts [publickey keyboard-interactive]; this connection
offered [password]
```

Gated on `accepted` being non-empty, so a host-key failure is never
decorated with an auth-shaped note: host-key verification completes during
key exchange, strictly before user authentication, so the observer has not
run and `accepted` is nil. The gate is not perfect in the other direction
— a connection dying *during* authentication also leaves `accepted` set,
and would be decorated. The note stays true in that case, just less
pointed, which is the right way for it to be imprecise.

Two implementation notes that are easy to lose. `golangci-lint`'s `nilnil`
fires on the observer and needs a justified `//nolint`, because
`(nil, nil)` is x/crypto's documented fall-through contract. And the
observer is race-free only because `handshake` calls `ssh.NewClientConn`
inline on its own goroutine — the watcher goroutine beside it touches only
`abort`. Moving the handshake into a goroutine, as an earlier shape of that
function might tempt someone to, would put a write to `accepted` in one
goroutine and the read in `setupFailure` in another.

### 2. `WithPassword` also answers a keyboard-interactive login

**Why implicitly, rather than behind an option the caller must name.**
This is a library for automated orchestration: there is no human at a
terminal, ever, and nothing that reads an error message and adjusts. An
explicit-only design prices the failure as a developer inconvenience —
connect, read a good error, add a line — but the failure that matters is
a fleet turning on `PasswordAuthentication no` in a hardening rollout,
after which every job against it fails at once and recovery needs a code
change and a redeploy. A credential the caller configured for a host
should reach that host by whichever method the host is willing to take.

That the two methods usually carry the same credential is OpenSSH's own
position, not an assumption made here. `sshd_config(5)` recommends the
very pairing that produces the trap, on the grounds that the methods are
interchangeable: *"Because PAM keyboard-interactive authentication usually
serves an equivalent role to password authentication, you should disable
either PasswordAuthentication or KbdInteractiveAuthentication."* A host
reached this way is, in the common case, asking for the same password
through a different door.

The consent an explicit option would buy is also thinner than it looks.
Against a stack that asks for something other than a password first, the
explicit and implicit designs transmit exactly the same bytes to exactly
the same host; the only difference is whether the caller wrote a line
accepting it. That is consent, not protection, and it is bought at the
cost above.

The library registers its own responder, appended **last** in
`authMethods` — after the key and after the agent. That ordering does two
jobs, and both are load-bearing. It keeps today's precedence, so a caller
who configured a key still offers the key first rather than sending a
password to a hardened host ahead of it — which means the password
crosses over keyboard-interactive only when nothing else configured could
have logged in at all. Whatever exposure this design carries exists only
where the alternative was failing anyway. And because nothing follows it,
an error arising from this method is the one x/crypto returns
(`client_auth.go:148-152` returns the retained error only when no method
remains) rather than being discarded for the generic message.

The responder is not a shape guard. It has three rules, each of which
exists because the alternative was measured to be wrong:

- A round with no questions is answered with no answers, never an error.
- Only the first round that asks anything is answered with the password.
- Every later round is declined **in the protocol** — the right number of
  empty strings — never by returning an error.

Against the deployment this feature exists for, the conversation is one
password round and then an informational one, and the login succeeds.
Against a host that stacks a second factor after a password, the password
reaches the password prompt and the code prompt gets an empty string:
authentication fails honestly, and nothing the caller did not intend is
transmitted.

**The residual exposure, stated plainly:** the rule is *first round that
asks anything*, not *first round that asks for a password*, because
nothing in the protocol distinguishes those. A server whose first
question is something other than a password — a passcode-first PAM stack,
or a host whose PAM configuration changed after its key was trusted —
receives the password in reply to that question. Shape cannot detect it
and prompt text cannot be trusted to. What bounds it is that the host
already passed the caller's host-key policy, already holds that password
by configuration, and was reached only because no key or agent could log
in; what it costs is that the password may land in a different sink on
that host. This is the trade the design accepts, and it is the reason the
responder answers one round rather than all of them.

How often that trade is called in is not established. An earlier reading
of the evidence held that the trigger configuration selects for
second-factor deployments, which would have made this the common case
rather than the corner. That was an inference rather than a measurement,
and `sshd_config(5)`'s own recommendation above cuts the other way.
Treat the frequency as unknown and the bound as the thing doing the work.

The responder is suppressed entirely when a caller's answerer is
configured, because two methods of the same RFC name cannot both run and
the caller's is the one they asked for. It keys off the configuration
rather than off which option was called, so a hand-assembled `Config`
behaves the same way:

```go
switch {
case cfg.KeyboardInteractive != nil:
    creds.add("keyboard-interactive",
        ssh.KeyboardInteractive(declining(cfg.KeyboardInteractive)))
case cfg.Password != "":
    creds.add("keyboard-interactive",
        ssh.KeyboardInteractive(answeringWith(cfg.Password)))
}
```

`WithPassword`'s signature and doc are unchanged. Its behaviour changes
only in a case that previously failed outright, so no `!` marker is owed.

### 3. `WithKeyboardInteractive` for everything else

```go
// KeyboardInteractive answers the questions a server puts to a
// keyboard-interactive login. See [WithKeyboardInteractive].
KeyboardInteractive ssh.KeyboardInteractiveChallenge
```

Exported and typed as x/crypto's own type, following `HostKeyCallback`
([ssh/options.go:55](../ssh/options.go)) — the settled precedent for a
func-typed field on `Config`, which is already non-comparable because of
it.

The caller's answerer is handed **every** round the server sends,
including one that asks nothing. An earlier draft had the library absorb
those rounds so a caller could not mishandle them, which was wrong: RFC
4256 §3.3 says a round carrying only an instruction should be shown, and
absorbing it means a closure driving a real terminal can never show the
server's message. Someone reaching for this option is doing something
specific; hiding a protocol round from them is the wrong default.

Passing it through is safe because the count contract already carries the
answer. One reply per question, so zero questions means zero replies —
and the idiomatic loop gets that right without trying:

```go
answers := make([]string, len(questions)) // len 0
for i := range answers { /* not entered */ }

return answers, nil // len(answers) == len(questions) == 0
```

The request's version failed not because it saw the round but because it
returned an *error* for it, which fails a login that had otherwise
succeeded.

So the doc carries five things, each a measured hazard rather than a
caution:

- **A round may ask nothing**, and the only correct reply is no answers —
  `return nil, nil`. Never an error.
- **Never index `questions` or `echos` without checking `len`.** They are
  both nil on such a round, and `echos[0]` panics on the caller's
  goroutine, inside `New`. This is not hypothetical: writing the request's
  own guard with its operands in the other order produces exactly that
  crash.
- **A conversation may hold several rounds**, and an answerer that
  replies with the same secret every time has sent it to every module
  that asked. The doc's example must therefore *not* be a loop filling
  every question with the same password — run against a
  password-then-passcode stack, that example transmits the login password
  to the passcode prompt.
- **`answer` runs outside every bound this package sets.** It is invoked
  inline between a read and a write with no I/O pending, so neither the
  socket deadline nor the context watcher's abort can interrupt it.
  Measured: `WithTimeout(500ms)` with a blocking answerer returned at
  4.005s, when the answerer was released. An answerer that waits on a
  person hangs `New` for as long as the person takes; if it prompts, it
  owns the deadline.
- **`answer` may be invoked concurrently.** An `Option` is documented as
  a value a caller may keep and use for several connections
  ([ssh/options.go:290](../ssh/options.go)), so round state must be
  per-connection or synchronised. A shared stateful answerer across two
  concurrent `New` calls trips the race detector.

The doc's example is the terminal case, because that is what the option
is for and what the type was designed around:

```go
ssh.WithKeyboardInteractive(
    func(_, instruction string, questions []string, echos []bool) ([]string, error) {
        if instruction != "" {
            fmt.Fprintln(os.Stderr, instruction)
        }

        answers := make([]string, len(questions))
        for i, question := range questions {
            fmt.Fprint(os.Stderr, question)

            if echos[i] {
                answers[i] = readLine()
            } else {
                answers[i] = readSecret()
            }
        }

        return answers, nil
    })
```

It is correct on a zero-question round without a special case, it never
reuses a secret across rounds, and it shows the server's own words to
whoever is deciding — which is the property automatic answering gives up.

### Error classification

An answerer that returns an error is making a deterministic policy
refusal, and it currently lands in `*invoke.TransportError` — the
retryable family — against the norm
[ssh/classify_test.go:36](../ssh/classify_test.go) already pins:
*"a deterministic policy refusal must not classify as retryable
transport"*. Retrying calls the same pure function for the same answer
and spends the server's `MaxAuthTries` doing it.

This is fixable precisely, and the mechanism matters because the doc
elsewhere rejects matching on x/crypto's internal message strings. The
caller's answerer is wrapped before registration — `declining` above —
which is the only remaining job that wrapper has now that it no longer
absorbs the empty round. It owns the boundary where a caller's error
appears, and joins it to an exported sentinel:

```go
// ErrAnswererDeclined reports that a keyboard-interactive answerer
// refused to answer. It is the caller's own decision surfacing, not a
// failure of the connection, and it is not retried.
var ErrAnswererDeclined = errors.New("ssh: keyboard-interactive answerer declined")
```

x/crypto preserves it: `client_auth.go:657` returns the callback's error
unwrapped, and `client.go:85` wraps the result with `%w`, so `errors.Is`
recovers the sentinel at the handshake error site. No string matching, and
it only works because keyboard-interactive is registered last — an error
from a method that something follows is discarded.

**A correction to an earlier draft of this document.** It proposed
classifying *authentication failure in general* out of `TransportError`
as part of change 1, on the grounds that change 1 already stands at that
error site. That was wrong: separating "the server refused authentication"
from "the connection died during authentication" has no typed signal in
x/crypto, and the only discriminator available is the very message-string
match this design rejects. `AllowedMethods` being non-empty does not
separate them either, since a wire failure mid-authentication also has it.
So the general defect stays its own issue, listed under **Out of scope**,
and only the path this change creates is classified here.

## What is rejected

**The shape guard**, in both directions. "Exactly one question, echo off,
else error" refuses the zero-question round that a PAM host really sends,
breaking the login it exists to enable; and it admits every dangerous
prompt, because a TOTP challenge, a forced-password-change prompt and a
key-passphrase prompt are all one question with echo off. Of four
realistic counter-scenarios it blocks one (Duo, which prompts with echo
*on*), fails to block two, and wrongly blocks a legitimate case. Written
with its operands in the other order it panics the calling program on the
nil `echos` slice.

**Any prompt-text guard.** A blocklist against strings the other party
chooses, in a locale it chooses.

**`WithKeyboardInteractivePassword`.** It takes the same input as
`WithPassword`, expresses the same intent, and differs only in which RFC
method carries it. Choosing between them requires knowing the server's
PAM configuration, which is the knowledge the trap already proves the
caller does not have.

**A general callback with no library-owned responder.** The realistic
consumer is non-interactive and has a password, so the answerer they
write is a password answerer — and both plausible hand-rolls are broken.
This is CONTRIBUTING's third clause exactly: the hand-rolled version
hides a trap, so the helper earns its place.

**An exported `PasswordAnswerer` the caller passes to
`WithKeyboardInteractive`.** Considered seriously as the explicit
alternative to change 2, and rejected on the orchestration argument above:
it still requires a code change and a redeploy to recover from a
server-side configuration change, which is the failure that matters here.
It transmits the same bytes to the same host as the implicit design in the
one case that carries exposure, so what it buys is the caller's signature
on that transmission rather than protection from it.

**`ssh.RetryableAuthMethod`.** The answerer is a pure function of
configured state; retrying re-sends the same answer.

**A bespoke `Answerer`/`Prompt` type.** x/crypto's own type is the
precedent `HostKeyCallback` already set.

**Absorbing the zero-question round on a caller's behalf.** Proposed in an
earlier draft of this document and rejected on review: it would make a
terminal-driving closure unable to show the server's message, which RFC
4256 §3.3 says it should, and the count contract already makes the round
safe to hand over. The library still absorbs it for its own responder,
which has nowhere to show anything.

## Implementation

Three files carry it, and none of the changes is large.

**`ssh/auth.go`** builds the list of ways to log in. Two changes: it
returns a small `credentials` value instead of a bare slice, so the RFC
method names travel with the methods and change 1 has something to report
— `ssh.AuthMethod` will not tell you its own name. And it gains one branch
at the **end** of the list, after the key and the agent:

```go
switch {
case cfg.KeyboardInteractive != nil:
    creds.add("keyboard-interactive",
        ssh.KeyboardInteractive(declining(cfg.KeyboardInteractive)))
case cfg.Password != "":
    creds.add("keyboard-interactive",
        ssh.KeyboardInteractive(answeringWith(cfg.Password)))
}
```

Two small helpers beside it. `answeringWith` is the library's own
responder: no answers to a round that asks nothing, the password to the
first round that asks something, empty strings to every round after.
`declining` wraps a caller's answerer to tag a refusal with
`ErrAnswererDeclined`, so it classifies as policy rather than transport.

**`ssh/chain.go`** makes the connection. It gains a read-only hook that
notes what the server said it accepts, and uses it to decorate a
handshake failure. The hook changes nothing — returning no method falls
straight through to x/crypto's ordinary selection.

**`ssh/options.go`** gains one `Config` field and one option, following
`HostKeyCallback` and `WithHostKeyCallback` exactly. `WithPassword` is not
touched.

Then tests, the package doc, and a README line.

## Test plan

Package-local, in `ssh/`. `invoketest` needs no change and owes no
misbehave entry — authentication is invisible to the contract suite,
confirmed by running it over a keyboard-interactive-authenticated
environment (56 pass / 2 skip, identical to the password lane).

The in-process server gains roughly 120 lines: a `passwordAuth` field
defaulting true and subtracted after the option loop, so no existing
`startTestServer` call site changes; a `challenge` hook wired to
`KeyboardInteractiveCallback`; and a recorder of every answer the client
sent.

Each test below must be capable of failing for the reason it is credited
with — the repo's own standard, and the standard three of the request's
proposed tests do not meet.

1. **The trap.** Hardened server, `WithPassword` only. Before change 2:
   fails, and the message names both method lists. After: succeeds.
2. **The zero-question round, built-in responder.** Server answers
   password, then sends an informational round. Login succeeds; the
   responder is entered once.
3. **The zero-question round, caller's closure.** Same server, a closure
   recording every round it is handed. It must see the informational
   round — the property the earlier draft would have removed — and the
   login must still succeed when it replies `nil, nil`.
4. **The harvest.** Server sends `"Password: "` then
   `"Verification code: "`, both echo off. The recorder must hold the
   password once and an empty string once. This is the load-bearing
   security test; it fails against an unbounded responder.
5. **Ordering.** Authorized key plus password, against a server offering
   `publickey` and `keyboard-interactive`. Moving the keyboard-interactive
   branch ahead of the key must fail this test — the request's equivalent
   test passes with the invariant broken, because it configures no method
   that could follow.
6. **Suppression.** `WithPassword` and `WithKeyboardInteractive` together:
   the caller's answerer runs, the library's does not.
7. **Concurrency.** One `Option` value shared by two concurrent `New`
   calls, under `-race`.
8. **Refusal classifies as policy, not transport.** A closure returning an
   error against a hardened server: the failure must satisfy
   `errors.Is(err, ErrAnswererDeclined)` and must *not* satisfy
   `errors.As(err, &transportErr)` — mirroring the assertion
   [ssh/classify_test.go:36](../ssh/classify_test.go) already makes. It
   fails if the sentinel is dropped, and it fails if the method is moved
   off the end of the list, since the error is then discarded.
9. **Diagnosis.** The decorated error names the accepted and offered
   lists, and an undecorated host-key failure stays undecorated.
10. **The real server**, behind the `openssh` tag. `alpine:3` with
   `openssh-server-pam` and `linux-pam`, served by `/usr/sbin/sshd.pam` —
   the stock `openssh` package has no PAM and advertises a method it
   cannot serve. The lane must carry a **second PAM module** so a genuine
   two-round conversation is exercised: a stack with no second factor
   tests only the safe case. `waitForSSHD`
   ([ssh/openssh_test.go:121](../ssh/openssh_test.go)) takes no options at
   all and `dialOpenSSH` always appends `ssh.WithPassword`. With change 2
   those happen to work against a PAM host — which is precisely why the
   lane must configure the answerer explicitly rather than passing by
   accident, and why `waitForSSHD` needs an options parameter.

## Work

One branch, one PR, one conventional commit: `feat(ssh): Answer
keyboard-interactive challenges`. Roughly 180 production lines and 800
test lines across `auth.go`, `chain.go`, `options.go`, the testserver
plumbing, a new `kbdint_test.go`, the `openssh` lane, the package doc and
the README.

That is smaller than 47b4876, which carried the jump host in one PR at
2371 insertions across 9 files, and it is one concern: make the library
work against a server that asks questions, and say so clearly when it
cannot. Splitting the diagnosis out ahead of the option would ship a
message naming a fix that did not exist yet, and the repository
squash-merges, so intermediate commits would not survive into history in
any case.

The one piece carrying unknown risk is the PAM-backed `openssh` lane: it
needs `openssh-server-pam`, `linux-pam` and a hand-written PAM stack in
the image, which has been proven on a laptop but not in CI. If it fights,
peel it off into `test(ssh): Exercise keyboard-interactive against a
PAM-backed sshd` and let the feature land without it. Do not plan for
that split up front.

`feat(ssh):` raises the minor: v1.1.0 → v1.2.0, with the docker submodule
repointed and tagged per RELEASING.md even though nothing in `docker/`
changes.

## Out of scope

Two defects confirmed while investigating, neither caused by this work:

- **Authentication failure in general classifies as retryable
  `*invoke.TransportError`** — a wrong password is retried today. Only the
  path this change creates is fixed here, via a sentinel the package owns.
  The general case needs a way to tell "the server refused" from "the wire
  died mid-authentication", and x/crypto offers no typed signal for it, so
  the only discriminator on hand is a match against its message string —
  which this design rejects. That question deserves its own issue rather
  than a guess folded into this one.
- `WithPrivateKey` and `WithAgent` both produce the RFC name `publickey`
  ([ssh/auth.go:33](../ssh/auth.go)), and the client keeps only the first
  method per name — so a configured agent is likely never offered when a
  key file is also set. The `credentials.add` change makes this newly
  visible, which is a reason to file it, not to fold it in.
