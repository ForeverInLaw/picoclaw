package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInsertedLegacyDuplicateKeepsPersistedModelIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.ModelList = []ModelConfig{{ModelName: "same", Model: "openai/shared"}}
	require.NoError(t, SaveConfig(path, cfg))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	old := cfg.ModelList[0]
	inserted := old
	inserted.ID = ""
	cfg.ModelList = []ModelConfig{inserted, old}
	require.NoError(t, SaveConfig(path, cfg))
	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, old.ID, cfg.ModelList[1].ID)
	require.NotEqual(t, old.ID, cfg.ModelList[0].ID)
	ids := []string{cfg.ModelList[0].ID, cfg.ModelList[1].ID}
	require.NoError(t, SaveConfig(path, cfg))
	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, ids, []string{cfg.ModelList[0].ID, cfg.ModelList[1].ID})
}

func TestMultiKeyRuntimeIDsAvoidConfiguredAndGeneratedCollisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.ModelList = []ModelConfig{
		{ID: "foo", ModelName: "multi", Model: "openai/shared", APIKeys: []string{"a", "b", "c"}},
		{ID: "foo__key_1", ModelName: "reserved", Model: "openai/other"},
		{ID: "foo__key_1__slot_1", ModelName: "reserved-slot", Model: "openai/other"},
	}
	require.NoError(t, SaveConfig(path, cfg))
	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.ModelList, 5)
	first := cfg.ModelList[0].ID
	require.Equal(t, "foo__key_1__slot_2", first)
	require.Equal(t, "foo__key_2", cfg.ModelList[1].ID)
	require.NoError(t, SaveConfig(path, cfg))
	cfg, err = LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, first, cfg.ModelList[0].ID)
	saved := cfg.ConfiguredModels()
	require.Len(t, saved, 3)
	require.Equal(t, "foo", saved[0].ID)
	require.Equal(t, []string{"a", "b", "c"}, saved[0].APIKeys)
}
