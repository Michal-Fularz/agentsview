package sync_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync"
)

func TestClaudeChannelUpgradeKeepsRetryCuration(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project-a")
	require.NoError(t, os.MkdirAll(projectDir, 0o700))
	path := filepath.Join(projectDir, "rewind.jsonl")
	lines := []string{`{"type":"user","uuid":"root","timestamp":"2026-10-22T19:00:00Z","message":{"content":"start"}}`}
	parent := "root"
	for i := range 4 {
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"c%d","parentUuid":%q,"timestamp":"2026-10-22T19:01:00Z","isMeta":true,"origin":{"kind":"channel"},"message":{"content":"<channel source=\"telegram\">question %d</channel>"}}`, i, parent, i))
		parent = fmt.Sprintf("c%d", i)
	}
	lines = append(lines, `{"type":"user","uuid":"retry","parentUuid":"root","timestamp":"2026-10-22T19:02:00Z","message":{"content":"try again"}}`)
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	archivePath := filepath.Join(t.TempDir(), "archive.db")
	database, err := db.OpenIsolated(ctx, archivePath)
	require.NoError(t, err)
	cfg := sync.EngineConfig{AgentDirs: map[parser.AgentType][]string{parser.AgentClaude: {root}}, Machine: "local"}
	engine := sync.NewEngine(ctx, database, cfg)
	require.Zero(t, engine.SyncAll(ctx, nil).Failed)
	// Version 144 discarded this branch. Remove it before activating the
	// export index so the seeded archive contains only the original retry.
	require.NoError(t, database.Update(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE session_id = 'rewind-c0'")
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE id = 'rewind-c0'")
		return err
	}))
	messages := fetchMessages(t, database, "rewind")
	require.Len(t, messages, 2)
	_, err = database.PinMessage(ctx, "rewind", messages[1].ID, new("saved retry"))
	require.NoError(t, err)
	window, err := database.BuildRecallEvidenceWindow(ctx, "rewind", 1, 1)
	require.NoError(t, err)
	selection, err := window.BindSelection(db.RecallEvidenceSelection{MessageStartOrdinal: 1, MessageEndOrdinal: 1})
	require.NoError(t, err)
	_, err = database.InsertRecallEntry(ctx, db.RecallEntry{
		ID: "saved-fact", Type: "fact", Scope: "project", Status: "accepted",
		Title: "retry", Body: "saved retry", SourceSessionID: "rewind", Agent: "claude", ProvenanceOK: true,
		Evidence: []db.RecallEvidence{{SessionID: "rewind", MessageStartOrdinal: 1, MessageEndOrdinal: 1, ContentDigest: selection.ContentDigest}},
	})
	require.NoError(t, err)
	before, err := database.ExportConversationChanges(ctx, db.ConversationExportOptions{})
	require.NoError(t, err)
	var exportedBefore db.ConversationChange
	for _, change := range before.Changes {
		if change.SessionID == "rewind" && change.Ordinal == 1 && change.MessageID != "" {
			exportedBefore = change
		}
	}
	require.NotEmpty(t, exportedBefore.MessageID)
	engine.Close()
	require.NoError(t, database.Close())

	// Model version 144: only the retry was archived, with the same file
	// fingerprint. The channel branch had never been stored.
	raw, err := sql.Open("sqlite3", archivePath)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, "UPDATE sessions SET data_version = 144")
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, "PRAGMA user_version = 144")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	assert.ElementsMatch(t, []parser.AgentType{parser.AgentClaude, parser.AgentCowork, parser.AgentQoder, parser.AgentIcodemate}, db.ScopedDataVersionProviders(144, 145))
	reopened, err := db.OpenIsolated(ctx, archivePath)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	assert.False(t, reopened.NeedsResync(), "the archive upgrades in place")
	upgraded := sync.NewEngine(ctx, reopened, cfg)
	defer upgraded.Close()
	stats := upgraded.SyncAll(ctx, nil)
	require.Zero(t, stats.Failed)
	assert.Positive(t, stats.Synced)
	retry := fetchMessages(t, reopened, "rewind")
	require.Len(t, retry, 2)
	assert.Equal(t, "start", retry[0].Content)
	assert.Equal(t, "try again", retry[1].Content)
	fork, err := reopened.GetSessionFull(ctx, "rewind-c0")
	require.NoError(t, err)
	require.NotNil(t, fork)
	assert.Equal(t, 4, fork.UserMessageCount)
	assert.Equal(t, "question 0", *fork.FirstMessage)
	pins, err := reopened.ListPinnedMessages(ctx, "rewind", "")
	require.NoError(t, err)
	require.Len(t, pins, 1)
	assert.Equal(t, new("saved retry"), pins[0].Note)
	assert.Equal(t, retry[1].ID, pins[0].MessageID)
	recall, err := reopened.GetRecallEntry(ctx, "saved-fact")
	require.NoError(t, err)
	require.NotNil(t, recall)
	assert.True(t, recall.ProvenanceOK)
	assert.Equal(t, "rewind", recall.SourceSessionID)
	require.Len(t, recall.Evidence, 1)
	assert.Equal(t, selection.ContentDigest, recall.Evidence[0].ContentDigest)
	exported, err := reopened.GetConversationMessage(ctx, db.ConversationMessageOptions{DatabaseID: before.DatabaseID, SessionID: "rewind", MessageID: exportedBefore.MessageID, Revision: exportedBefore.Revision})
	require.NoError(t, err)
	assert.Equal(t, new("try again"), exported.Text)
	found, err := reopened.Search(ctx, db.SearchFilter{Query: `"question 3"`})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	assert.Equal(t, "rewind-c0", found.Results[0].SessionID)
}
