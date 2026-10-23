package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeChannelMessages(t *testing.T) {
	content := loadFixture(t, "claude/channel_messages.jsonl")
	for _, incremental := range []bool{false, true} {
		t.Run(fmt.Sprint(incremental), func(t *testing.T) {
			var messages []ParsedMessage
			if incremental {
				path := createTestFile(t, "channel.jsonl", content)
				offset := strings.Index(content, `{"parentUuid":"aaaaaaaa-0000-4000-8000-000000000011"`)
				require.Positive(t, offset)
				var err error
				messages, _, _, _, err = claudeParseSessionFrom(path, int64(offset), claudeIncrementalScan{startOrdinal: 2, lastEntryUUID: "aaaaaaaa-0000-4000-8000-000000000011", stored: claudeStoredIdentity{entrypoint: "cli"}})
				require.NoError(t, err)
				require.Len(t, messages, 2)
			} else {
				session, full := runClaudeParserTest(t, "channel.jsonl", content)
				assert.Equal(t, 2, session.UserMessageCount)
				require.Len(t, full, 4)
				messages = full[2:]
			}
			assert.Equal(t, RoleUser, messages[0].Role)
			assert.False(t, messages[0].IsSystem)
			assert.Contains(t, messages[0].Content, "what changed in the last commit?")
			assert.Contains(t, messages[0].Content, `<channel source="telegram"`)
			assert.Equal(t, 2, messages[0].Ordinal)
			assert.Equal(t, "The last commit renamed the config loader.", messages[1].Content)
		})
	}
}

func TestClaudeChannelMetaAndPreview(t *testing.T) {
	for _, origin := range []string{"channel", "peer", ""} {
		t.Run(origin, func(t *testing.T) {
			body := strings.Repeat("question ", 50)
			content := fmt.Sprintf(`{"type":"user","uuid":"c1","isMeta":true,"origin":{"kind":%q},"message":{"content":%q}}`+"\n", origin, `<channel source="`+strings.Repeat("x", 400)+`">`+"\n"+body+"\n</channel>")
			path := createTestFile(t, "meta.jsonl", content)
			results, err := parseClaudeSession(path, "project", "local")
			require.NoError(t, err)
			appended, _, _, err := callParseClaudeSessionFrom(path, 0, 0, "")
			require.NoError(t, err)
			if origin != "channel" {
				require.Len(t, results, 1)
				assert.Empty(t, results[0].Messages)
				assert.Empty(t, appended)
				return
			}
			require.Len(t, results, 1)
			assert.Equal(t, 1, results[0].Session.UserMessageCount)
			assert.Equal(t, strings.Repeat("question ", 33)+"que...", results[0].Session.FirstMessage)
			assert.Equal(t, results[0].Messages, appended)
		})
	}
}

func TestClaudeTypedChannelEnvelopePreview(t *testing.T) {
	content := `{"type":"user","uuid":"u1","message":{"content":"<channel source=\"example\">literal text</channel>"}}` + "\n"
	session, _ := runClaudeParserTest(t, "typed.jsonl", content)
	assert.Equal(t, `<channel source="example">literal text</channel>`, session.FirstMessage)
}

func TestClaudeChannelRewindKeepsRetryIdentity(t *testing.T) {
	// One typed turn and four channel turns used to be discarded as a retry.
	lines := []string{`{"type":"user","uuid":"root","message":{"content":"start"}}`}
	parent := "root"
	for i := range 4 {
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"c%d","parentUuid":%q,"isMeta":true,"origin":{"kind":"channel"},"message":{"content":"<channel source=\"telegram\">question %d</channel>"}}`, i, parent, i), fmt.Sprintf(`{"type":"assistant","uuid":"a%d","parentUuid":"c%d","message":{"content":"answer %d"}}`, i, i, i))
		parent = fmt.Sprintf("a%d", i)
	}
	lines = append(lines, `{"type":"user","uuid":"retry","parentUuid":"root","message":{"content":"try again"}}`)
	path := createTestFile(t, "rewind.jsonl", strings.Join(lines, "\n")+"\n")
	results, err := parseClaudeSession(path, "project", "local")
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "rewind", results[0].Session.ID)
	require.Len(t, results[0].Messages, 2)
	assert.Equal(t, "try again", results[0].Messages[1].Content)
	assert.Equal(t, 2, results[0].Session.UserMessageCount)
	assert.Equal(t, "rewind-c0", results[1].Session.ID)
	assert.Equal(t, "rewind", results[1].Session.ParentSessionID)
	assert.Equal(t, RelFork, results[1].Session.RelationshipType)
	assert.Equal(t, 4, results[1].Session.UserMessageCount)
	assert.Equal(t, "question 0", results[1].Session.FirstMessage)
	require.Len(t, results[1].Messages, 8)
	assert.Equal(t, "answer 3", results[1].Messages[7].Content)
}
