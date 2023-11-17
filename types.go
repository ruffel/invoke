package invoke

type RunResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Args     []string
}
