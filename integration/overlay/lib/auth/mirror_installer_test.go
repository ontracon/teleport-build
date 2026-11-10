package auth

import (
	"context"
	"testing"

	"github.com/gravitational/teleport/lib/srv/server/installer"
	"github.com/stretchr/testify/require"
)

func TestMirrorDefaultInstaller(t *testing.T) {
	t.Setenv("TELEPORT_CDN_BASE_URL", "https://mirror.example.com")
	selected, err := (&GRPCServer{}).defaultInstaller(context.Background())
	require.NoError(t, err)
	require.Equal(t, installer.NewDefaultInstaller.GetScript(), selected.GetScript())
}
