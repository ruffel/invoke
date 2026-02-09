package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSH_NewFromSSHConfig(t *testing.T) {
	// Create a temporary ssh config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "ssh_config")

	configContent := `
Host myalias
    HostName 1.2.3.4
    User testuser
    Port 2222
    IdentityFile ~/.ssh/id_ed25519
    StrictHostKeyChecking no
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	t.Run("custom path", func(t *testing.T) {
		cfg, err := NewFromSSHConfig("myalias", configPath)
		require.NoError(t, err)

		assert.Equal(t, "1.2.3.4", cfg.Host)
		assert.Equal(t, "testuser", cfg.User)
		assert.Equal(t, 2222, cfg.Port)
		assert.True(t, cfg.InsecureSkipVerify)
		// IdentityFile resolution check (it uses os.UserHomeDir())
		assert.True(t, filepath.IsAbs(cfg.PrivateKeyPath))
		assert.Contains(t, cfg.PrivateKeyPath, "id_ed25519")
	})

	t.Run("non-existent path", func(t *testing.T) {
		_, err := NewFromSSHConfig("myalias", filepath.Join(tmpDir, "non_existent"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open ssh config")
	})
}
