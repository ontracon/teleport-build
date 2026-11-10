package scripts

import (
	"context"
	"testing"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/require"
)

func TestMirrorInstallScripts(t *testing.T) {
	opts := InstallScriptOptions{
		MirrorMode:      true,
		CDNBaseURL:      "https://mirror.example.com/releases",
		TeleportVersion: semver.New("18.10.0-10.1"),
		TeleportFlavor:  "teleport",
		ProxyAddr:       "proxy.example.com:443",
	}
	legacy, err := GetInstallScript(context.Background(), opts)
	require.NoError(t, err)
	require.Contains(t, legacy, "mirror.example.com/releases")
	require.Contains(t, legacy, `if [[ -n "$TELEPORT_MIRROR_URL" ]]`)
	require.Contains(t, legacy, "sha256sum")
	join, err := GetNodeInstallScript(context.Background(), InstallNodeScriptOptions{InstallOptions: opts})
	require.NoError(t, err)
	require.Contains(t, join, `if [[ 'true' == "true" ]]; then`)
	require.Contains(t, join, "install_from_updater")

	opts.AutoupdateStyle = UpdaterBinaryAutoupdate
	updater, err := GetInstallScript(context.Background(), opts)
	require.NoError(t, err)
	require.Contains(t, updater, "https://mirror.example.com/releases")
	require.Contains(t, updater, "Archive checksum mismatch")
	require.Contains(t, updater, "--base-url")
}

func TestMirrorURLValidation(t *testing.T) {
	for _, value := range []string{"https://mirror.example.com", "https://mirror:8443/path"} {
		require.NoError(t, ValidateMirrorURL(value))
	}
	for _, value := range []string{"", "http://mirror", "https://u:p@mirror", "https://mirror/?secret=x", "https://mirror/#x", "https://mirror/'$(id)", "https://mirror/hello world"} {
		require.Error(t, ValidateMirrorURL(value), value)
	}
}

func TestMirrorBuildVersionOrdering(t *testing.T) {
	first, err := semver.NewVersion("18.10.0-10.1")
	require.NoError(t, err)
	next, err := semver.NewVersion("18.10.0-11.1")
	require.NoError(t, err)
	stable, err := semver.NewVersion("18.10.0")
	require.NoError(t, err)
	require.Less(t, first.Compare(*next), 0)
	require.Less(t, next.Compare(*stable), 0)
}
