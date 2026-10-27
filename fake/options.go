package fake

// Option configures the fake at construction.
type Option func(*Environment)

// WithEnv adds KEY=VALUE pairs to the fake target's base environment.
func WithEnv(pairs ...string) Option {
	return func(e *Environment) {
		for _, pair := range pairs {
			if key, value, ok := cutPair(pair); ok {
				e.baseEnv[key] = value
			}
		}
	}
}
