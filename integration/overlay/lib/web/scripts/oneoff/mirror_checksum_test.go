package oneoff

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMirrorBootstrapChecksum(t *testing.T) {
	root := t.TempDir()
	uname := filepath.Join(root, "uname")
	require.NoError(t, os.WriteFile(uname, []byte("#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo x86_64;; esac\n"), 0755))
	marker := filepath.Join(root, "executed")
	payload := []byte("#!/bin/sh\nprintf executed > \"$1\"\n")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "teleport/teleport-update", Mode: 0755, Size: int64(len(payload))}))
	_, err := tw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	sum := sha256.Sum256(buf.Bytes())
	var corrupt, wrongName atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			if wrongName.Load() {
				fmt.Fprintf(w, "%x  unrelated\n", sum)
				return
			}
			if corrupt.Load() {
				fmt.Fprintln(w, strings.Repeat("0", 64)+"  "+filepath.Base(strings.TrimSuffix(r.URL.Path, ".sha256")))
				return
			}
			fmt.Fprintf(w, "%x  %s\n", sum, filepath.Base(strings.TrimSuffix(r.URL.Path, ".sha256")))
			return
		}
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	script, err := BuildScript(OneOffScriptParams{BinUname: uname, CDNBaseURL: srv.URL,
		TeleportVersion: "v18.10.0-10.1", Entrypoint: "teleport-update",
		EntrypointArgs: marker, VerifyChecksum: true})
	require.NoError(t, err)
	path := filepath.Join(root, "install.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0755))
	out, err := exec.Command("sh", path).CombinedOutput()
	require.NoError(t, err, string(out))
	require.FileExists(t, marker)
	require.NoError(t, os.Remove(marker))
	corrupt.Store(true)
	out, err = exec.Command("sh", path).CombinedOutput()
	require.Error(t, err, string(out))
	require.NoFileExists(t, marker)
	corrupt.Store(false)
	wrongName.Store(true)
	out, err = exec.Command("sh", path).CombinedOutput()
	require.Error(t, err, string(out))
	require.NoFileExists(t, marker)
}
