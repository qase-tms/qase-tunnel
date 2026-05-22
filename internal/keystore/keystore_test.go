package keystore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceNameMatchesQaseTunnelConvention(t *testing.T) {
	assert.Equal(t, "qase.io/qase-tunnel", ServiceName)
}

func TestMemoryKeystore_SetGetRoundTrip(t *testing.T) {
	k := NewMemoryKeystore()
	require.NoError(t, k.Set("token", "abc"))

	got, err := k.Get("token")
	require.NoError(t, err)
	assert.Equal(t, "abc", got)
}

func TestMemoryKeystore_GetUnknownReturnsErrNotFound(t *testing.T) {
	k := NewMemoryKeystore()
	_, err := k.Get("missing")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryKeystore_DeleteThenGetReturnsErrNotFound(t *testing.T) {
	k := NewMemoryKeystore()
	require.NoError(t, k.Set("a", "1"))
	require.NoError(t, k.Delete("a"))

	_, err := k.Get("a")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryKeystore_MultipleKeysDoNotCollide(t *testing.T) {
	k := NewMemoryKeystore()
	require.NoError(t, k.Set("a", "x"))
	require.NoError(t, k.Set("b", "y"))

	a, _ := k.Get("a")
	b, _ := k.Get("b")
	assert.Equal(t, "x", a)
	assert.Equal(t, "y", b)
}

func TestFileKeystore_SetGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	require.NoError(t, k.Set("api_token", "real-token-1234567890"))

	got, err := k.Get("api_token")
	require.NoError(t, err)
	assert.Equal(t, "real-token-1234567890", got)
}

func TestFileKeystore_OnDiskSecretsAreEncrypted(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	const plaintext = "DO-NOT-LEAK-PLAINTEXT-VALUE"
	require.NoError(t, k.Set("api_token", plaintext))

	body, err := os.ReadFile(filepath.Join(dir, "secrets.enc"))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(body), plaintext),
		"secrets file must not contain the plaintext value")
}

func TestFileKeystore_GetUnknownReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	_, err = k.Get("nope")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFileKeystore_DeleteRemovesEntry(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	require.NoError(t, k.Set("a", "v1"))
	require.NoError(t, k.Delete("a"))

	_, err = k.Get("a")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFileKeystore_DeleteMissingIsNoop(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	assert.NoError(t, k.Delete("does-not-exist"))
}

func TestFileKeystore_PermissionsAre0600OnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics not applicable on Windows")
	}
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)
	require.NoError(t, k.Set("api_token", "tok-1234"))

	for _, name := range []string{"key.bin", "secrets.enc"} {
		stat, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err, name)
		assert.Equal(t, os.FileMode(0o600), stat.Mode().Perm(), "%s must be 0600", name)
	}
}

func TestFileKeystore_MultipleKeysDoNotCollide(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	require.NoError(t, k.Set("api_token", "t1"))
	require.NoError(t, k.Set("stcp_secret_uuid-abc", "s1"))
	require.NoError(t, k.Set("stcp_secret_uuid-def", "s2"))

	a, _ := k.Get("api_token")
	s1, _ := k.Get("stcp_secret_uuid-abc")
	s2, _ := k.Get("stcp_secret_uuid-def")
	assert.Equal(t, "t1", a)
	assert.Equal(t, "s1", s1)
	assert.Equal(t, "s2", s2)
}

func TestFileKeystore_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	k1, err := NewFileKeystore(dir)
	require.NoError(t, err)
	require.NoError(t, k1.Set("api_token", "persist-me"))

	k2, err := NewFileKeystore(dir)
	require.NoError(t, err)
	got, err := k2.Get("api_token")
	require.NoError(t, err)
	assert.Equal(t, "persist-me", got)
}

func TestFileKeystore_DefaultDirIsHomeQaseTunnel(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", tmpHome)
	}

	k, err := NewFileKeystore("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmpHome, ".qase-tunnel"), k.Path())
}

func TestFileKeystore_ConcurrentSetGet(t *testing.T) {
	dir := t.TempDir()
	k, err := NewFileKeystore(dir)
	require.NoError(t, err)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			key := "k"
			val := "v"
			_ = k.Set(key, val)
			_, _ = k.Get(key)
			_ = i
		}(i)
	}
	wg.Wait()
}
