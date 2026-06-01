package ops

import "os"

// BinaryPath returns the absolute path to the running qvr binary, for
// embedding into agent hook configs. Hooks fire in the agent's own
// process with the agent's PATH, which may not include qvr — so we wire
// in the resolved executable path rather than the bare name "qvr".
// Falls back to "qvr" if the executable path can't be determined.
func BinaryPath() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return "qvr"
}
