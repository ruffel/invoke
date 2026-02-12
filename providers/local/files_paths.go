package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkPathTraversal(root, target string) error {
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(target)

	if cleanRoot == cleanTarget {
		return nil
	}

	if !strings.HasPrefix(cleanTarget, cleanRoot+string(os.PathSeparator)) {
		return fmt.Errorf("illegal file path: %s is not within %s", target, root)
	}

	return nil
}
