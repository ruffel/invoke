package docker

import "strconv"

// The provider's own errands in the container — resolving a name, checking
// a command before it runs, recording and signalling its process, moving
// an upload into place — are sh scripts, gathered here. Each reads its
// inputs from positional parameters, never by interpolation, so nothing a
// caller named is reinterpreted by the shell, and runRaw runs them.

// Exit codes the pre-flight check uses to tell its two failures apart.
// They sit above the range a normal command would plausibly use.
const (
	preCheckBadDir   = 91
	preCheckNotFound = 92
)

// pathWalkFor walks PATH for the name held in the positional parameter
// param, files only, running hit on the first executable match and miss
// when nothing matched. $d holds the matching directory in hit.
//
// The walk is what keeps resolution honest: "command -v" answers for
// shell builtins too — on a busybox image even "true" is one — and a bare
// name is not a path anything can exec. Every other provider resolves
// through the filesystem; this is the same search spelled in sh.
func pathWalkFor(param, hit, miss string) string {
	return `IFS=:; for d in $PATH; do [ -f "$d/` + param + `" ] && [ -x "$d/` + param + `" ] && { ` + hit +
		`; }; done; ` + miss
}

// lookPathScript resolves $1 as LookPath does: a name containing a slash
// is checked directly; a bare name walks PATH and prints the first
// executable file it finds.
func lookPathScript() string {
	return `case "$1" in
*/*) [ -f "$1" ] && [ -x "$1" ] && printf '%s\n' "$1" && exit 0; exit 1 ;;
*) ` + pathWalkFor(`$1`, `printf '%s\n' "$d/$1"; exit 0`, `exit 1`) + ` ;;
esac`
}

// shellProbeScript prints where $1 is on PATH, and exits zero whether or
// not it was found: the shell running the script exists wherever PATH
// says, and detectShell names it as it was found.
func shellProbeScript() string {
	return pathWalkFor(`$1`, `printf '%s\n' "$d/$1"; exit 0`, `exit 0`)
}

// preCheckScript validates a command's working directory, $1, and its
// executable, $2, before the real command runs, exiting preCheckBadDir or
// preCheckNotFound. $3 is non-empty when the caller set a PATH, which $4
// then carries.
//
// The check resolves the executable where the command will be: the
// daemon finds it through the exec's own environment, so a PATH the
// caller supplies is the one the check walks too. Otherwise the two
// disagree, refusing a runnable command or waving through one that
// resolves elsewhere at run time.
//
// A name is resolved by walking PATH for executable files, as every
// provider resolves one. "command -v" answers for shell builtins too, so
// it blesses a bare "cd" that no exec could run. A path is checked
// directly, because "command -v" given a path answers whether the file
// exists on some shells and whether it can be executed on others.
func preCheckScript() string {
	badDir, notFound := strconv.Itoa(preCheckBadDir), strconv.Itoa(preCheckNotFound)

	return `if [ -n "$3" ]; then PATH=$4; export PATH; fi
if [ -n "$1" ] && ! cd "$1"; then exit ` + badDir + `; fi
case "$2" in
*/*) [ -f "$2" ] && [ -x "$2" ] || exit ` + notFound + ` ;;
*) ` + pathWalkFor(`$2`, `exit 0`, `exit `+notFound) + ` ;;
esac`
}

// wrapperScript records the shell's own process id in the file $1, then
// replaces the shell with the rest of its arguments. $$ is the shell's id
// and exec hands it to the command, so the recorded id is the command's
// own; see wrapCommand for why the daemon's view of it will not do.
const wrapperScript = `echo $$ > "$1"; shift; exec "$@"`

// killScript delivers the signal named by $1 to the process whose id the
// wrapper recorded in $2.
//
// The wrapper writes the file before exec'ing, so it may not exist for
// the first instants of the command's life; the script waits briefly for
// it rather than reporting a kill that signalled nothing, and exits 1 if
// it never appears.
const killScript = `for _ in 1 2 3 4 5 6 7 8 9 10; do
	if [ -s "$2" ]; then exec kill -s "$1" "$(cat "$2")"; fi
	sleep 0.05
done
exit 1`

// mergeScript moves the staged arrival $1 into place at $2, merging a
// tree into an existing directory as the transfer engine does, with $3
// as the suffix of the temporary names it uses on the way. A refusal is
// reported on stdout, which is what runRaw hands back.
//
// A tree lands in an existing directory by merging into it, as cp, scp
// and rsync do: each file replaces its namesake, and what only the
// destination holds is left alone. A directory already there is merged
// into and given the source's mode; a symbolic link where the tree has a
// directory is refused rather than written through, and a file where it
// has one, or a directory where it has a file, is an error rather than
// something to delete. The root may be a link to a directory, which is
// the ordinary way to point a stable name at a release.
//
// Every entry lands by way of a temporary name beside it, then one
// rename onto the name it was asked for. Staging shares the root's
// parent, but the tree can cross onto another filesystem below it, a
// mounted child directory or a root linked elsewhere, and there mv falls
// back to copying: the copy goes to the temporary name, which nothing
// reads, and the real name still appears in one step. A symbolic link
// standing where a file arrives is moved aside first and put back if the
// file cannot follow it in: mv given a link to a directory moves into it,
// -T or not, so the link has to be out of the way.
const mergeScript = `fail() { printf '%s\n' "$*"; exit 1; }
suffix=$3

land() {
	tmp="$2.invoke-new-$suffix"
	if ! mv -- "$1" "$tmp"; then rm -rf -- "$tmp"; fail "moving $1 beside $2 failed"; fi
	if [ -L "$2" ]; then
		aside="$2.invoke-old-$suffix"
		if ! mv -- "$2" "$aside"; then rm -rf -- "$tmp"; fail "setting $2 aside failed"; fi
		if ! mv -T -- "$tmp" "$2"; then
			mv -- "$aside" "$2"
			rm -rf -- "$tmp"
			fail "moving into $2 failed"
		fi
		rm -f -- "$aside"
		return
	fi
	if ! mv -fT -- "$tmp" "$2"; then rm -rf -- "$tmp"; fail "moving into $2 failed"; fi
}

place() {
	if [ -d "$2" ] && [ ! -L "$2" ]; then fail "destination $2 is a directory"; fi
	land "$1" "$2"
}

merge() {
	mode=$(stat -c %a "$1") || exit 1
	chmod u+rwx "$1" || exit 1
	for e in "$1"/* "$1"/.[!.]* "$1"/..?*; do
		[ -e "$e" ] || [ -L "$e" ] || continue
		t="$2/${e##*/}"
		if [ -d "$e" ] && [ ! -L "$e" ]; then
			if [ -L "$t" ]; then fail "destination $t is a symbolic link; refusing to write the transferred tree through it"; fi
			if [ -d "$t" ]; then
				( merge "$e" "$t" ) || exit 1
			elif [ -e "$t" ]; then
				fail "destination $t is not a directory"
			else
				( land "$e" "$t" ) || exit 1
			fi
		else
			( place "$e" "$t" ) || exit 1
		fi
	done
	chmod "$mode" "$2" || exit 1
}

if [ -d "$1" ] && [ ! -L "$1" ]; then
	if [ -d "$2" ]; then merge "$1" "$2"; exit; fi
	if [ -e "$2" ] || [ -L "$2" ]; then fail "destination $2 exists and is not a directory"; fi
	land "$1" "$2"
else
	place "$1" "$2"
fi`
