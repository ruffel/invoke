package fake

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
)

// The mini-shell interprets the POSIX subset the contract suite and
// typical incidental scripts use: command sequencing with ; and &&,
// single and double quotes, $VAR expansion, $(command) substitution,
// numeric redirects to /dev/null and between the two output streams, cd,
// and exit. Nothing more — a script needing more belongs in a consumer
// handler.

// step is one simple command in a script, with its connector to the
// previous step.
type step struct {
	text     string
	andsWith bool // joined to the previous step by && rather than ;
}

// execScript runs a shell script within the session.
func execScript(ctx context.Context, s *session, script string) (int, bool) {
	steps, ok := splitScript(script)
	if !ok {
		_, _ = io.WriteString(s.stderr, "sh: syntax error\n")

		return 2, false
	}

	shellSession := s.clone()
	status := 0

	for _, st := range steps {
		if st.andsWith && status != 0 {
			continue
		}

		trimmed := strings.TrimSpace(st.text)
		if trimmed == "" {
			continue
		}

		var (
			exited      bool
			interrupted bool
		)

		status, exited, interrupted = execSimple(ctx, shellSession, trimmed)
		if exited || interrupted {
			return status, interrupted
		}
	}

	return status, false
}

// splitScript divides a script into steps at top-level ; and &&
// boundaries, respecting quotes and command substitution.
//
//nolint:cyclop // Single-pass quote/paren scanner; the state machine reads better unsplit.
func splitScript(script string) ([]step, bool) {
	var (
		steps   []step
		current strings.Builder
	)

	andNext := false
	inSingle, inDouble := false, false
	depth := 0

	flush := func(ands bool) {
		steps = append(steps, step{text: current.String(), andsWith: andNext})
		current.Reset()

		andNext = ands
	}

	runes := []rune(script)

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			}

			if r == '$' && i+1 < len(runes) && runes[i+1] == '(' {
				depth++
			}

			if r == ')' && depth > 0 {
				depth--
			}
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		case r == '$' && i+1 < len(runes) && runes[i+1] == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0 && r == ';':
			flush(false)

			continue
		case depth == 0 && r == '&' && i+1 < len(runes) && runes[i+1] == '&':
			flush(true)

			i++

			continue
		}

		current.WriteRune(r)
	}

	if inSingle || inDouble || depth != 0 {
		return nil, false
	}

	flush(false)

	return steps, true
}

// execSimple tokenizes and runs one simple command, applying redirects
// and the shell-level builtins cd and exit. It returns, in order: the
// exit status, whether the script must stop (exit), and whether the
// command was interrupted by cancellation.
func execSimple(ctx context.Context, s *session, text string) (int, bool, bool) {
	argv, redirects, tokOK, tokInterrupted := tokenize(ctx, s, text)
	if tokInterrupted {
		return -1, false, true
	}

	if !tokOK {
		_, _ = io.WriteString(s.stderr, "sh: syntax error\n")

		return 2, false, false
	}

	if len(argv) == 0 {
		return 0, false, false
	}

	switch argv[0] {
	case "exit":
		code := 0
		if len(argv) > 1 {
			code, _ = strconv.Atoi(argv[1])
		}

		return code, true, false

	case "cd":
		if len(argv) != 2 || !s.fs.isDir(vfsClean(s.dir, argv[1])) {
			_, _ = io.WriteString(s.stderr, "cd: no such directory\n")

			return 1, false, false
		}

		s.dir = vfsClean(s.dir, argv[1])

		return 0, false, false
	}

	sub := s.clone()
	applyRedirects(sub, redirects)

	code, wasInterrupted := dispatch(ctx, sub, argv[0], argv[1:])

	return code, false, wasInterrupted
}

// redirect is one parsed redirection word.
type redirect struct {
	fd     int    // 1 or 2
	toFD   int    // duplication target, or 0 when toPath is set
	toPath string // only /dev/null is simulated
}

// applyRedirects rewires the session's output streams in order.
func applyRedirects(s *session, redirects []redirect) {
	for _, r := range redirects {
		var target io.Writer

		switch {
		case r.toPath != "":
			target = io.Discard
		case r.toFD == 1:
			target = s.stdout
		case r.toFD == 2:
			target = s.stderr
		default:
			continue
		}

		if r.fd == 1 {
			s.stdout = target
		} else {
			s.stderr = target
		}
	}
}

// parseRedirect recognizes redirection words: >/dev/null, N>/dev/null,
// and N>&M.
func parseRedirect(word string) (redirect, bool) {
	fd := 1
	rest := word

	if len(rest) > 1 && (rest[0] == '1' || rest[0] == '2') && rest[1] == '>' {
		fd = int(rest[0] - '0')
		rest = rest[1:]
	}

	if !strings.HasPrefix(rest, ">") {
		return redirect{}, false
	}

	rest = rest[1:]

	if strings.HasPrefix(rest, "&") {
		toFD, err := strconv.Atoi(rest[1:])
		if err != nil || (toFD != 1 && toFD != 2) {
			return redirect{}, false
		}

		return redirect{fd: fd, toFD: toFD}, true
	}

	if rest == "/dev/null" {
		return redirect{fd: fd, toPath: rest}, true
	}

	return redirect{}, false
}

// tokenize splits a simple command into argv words and redirects,
// performing quote handling, $VAR expansion, and $() substitution. It
// returns, in order: the argv words, the redirects, whether the command
// parsed, and whether a substitution was interrupted by cancellation.
func tokenize(ctx context.Context, s *session, text string) ([]string, []redirect, bool, bool) {
	var (
		words   []string
		current strings.Builder
	)

	haveWord := false

	flush := func() {
		if haveWord {
			words = append(words, current.String())
			current.Reset()

			haveWord = false
		}
	}

	runes := []rune(text)

	for i := 0; i < len(runes); {
		switch r := runes[i]; r {
		case ' ', '\t':
			flush()

			i++

		case '\'':
			end := indexRune(runes, i+1, '\'')
			if end < 0 {
				return nil, nil, false, false
			}

			current.WriteString(string(runes[i+1 : end]))

			haveWord = true
			i = end + 1

		case '"':
			segment, next, segOK, segInterrupted := expandDoubleQuoted(ctx, s, runes, i+1)
			if segInterrupted {
				return nil, nil, false, true
			}

			if !segOK {
				return nil, nil, false, false
			}

			current.WriteString(segment)

			haveWord = true
			i = next

		case '$':
			segment, next, segOK, segInterrupted := expandDollar(ctx, s, runes, i)
			if segInterrupted {
				return nil, nil, false, true
			}

			if !segOK {
				return nil, nil, false, false
			}

			current.WriteString(segment)

			haveWord = true
			i = next

		default:
			current.WriteRune(r)

			haveWord = true
			i++
		}
	}

	flush()

	argv, redirects := classifyWords(words)

	return argv, redirects, true, false
}

// classifyWords separates tokenized words into argv and redirects.
func classifyWords(words []string) ([]string, []redirect) {
	var (
		argv      []string
		redirects []redirect
	)

	for _, word := range words {
		if r, isRedirect := parseRedirect(word); isRedirect {
			redirects = append(redirects, r)

			continue
		}

		argv = append(argv, word)
	}

	return argv, redirects
}

// expandDoubleQuoted consumes a double-quoted segment starting after the
// opening quote, expanding $VAR and $() inside it. It returns, in order:
// the expanded segment, the index after the closing quote, whether the
// segment parsed, and whether a substitution was interrupted.
func expandDoubleQuoted(ctx context.Context, s *session, runes []rune, start int) (string, int, bool, bool) {
	var out strings.Builder

	for i := start; i < len(runes); {
		switch r := runes[i]; r {
		case '"':
			return out.String(), i + 1, true, false

		case '$':
			expanded, after, expOK, expInterrupted := expandDollar(ctx, s, runes, i)
			if expInterrupted {
				return "", 0, false, true
			}

			if !expOK {
				return "", 0, false, false
			}

			out.WriteString(expanded)

			i = after

		default:
			out.WriteRune(r)

			i++
		}
	}

	return "", 0, false, false
}

// expandDollar consumes a $NAME or $(command) form starting at the $. It
// returns, in order: the expanded value, the index after the form,
// whether it parsed, and whether a substitution was interrupted.
func expandDollar(ctx context.Context, s *session, runes []rune, start int) (string, int, bool, bool) {
	i := start + 1

	if i < len(runes) && runes[i] == '(' {
		depth := 1
		j := i + 1

		for ; j < len(runes) && depth > 0; j++ {
			switch runes[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}

		if depth != 0 {
			return "", 0, false, false
		}

		inner := string(runes[i+1 : j-1])

		var captured bytes.Buffer

		sub := s.clone()
		sub.stdout = &captured

		_, subInterrupted := execScript(ctx, sub, inner)
		if subInterrupted {
			return "", 0, false, true
		}

		return strings.TrimRight(captured.String(), "\n"), j, true, false
	}

	nameEnd := i
	for nameEnd < len(runes) && isVarRune(runes[nameEnd], nameEnd == i) {
		nameEnd++
	}

	if nameEnd == i {
		return "$", i, true, false
	}

	return s.lookupEnv(string(runes[i:nameEnd])), nameEnd, true, false
}

func isVarRune(r rune, first bool) bool {
	if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
		return true
	}

	return !first && r >= '0' && r <= '9'
}

func indexRune(runes []rune, from int, want rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == want {
			return i
		}
	}

	return -1
}
