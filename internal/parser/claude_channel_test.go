package parser

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeChannelMessages(t *testing.T) {
	content := loadFixture(t, "claude/channel_messages.jsonl")
	for _, incremental := range []bool{false, true} {
		t.Run(strconv.FormatBool(incremental), func(t *testing.T) {
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
	for _, tt := range []struct {
		name, origin, body, preview string
		isMeta                      bool
	}{
		{name: "channel", origin: "channel", isMeta: true, body: strings.Repeat("question ", 50), preview: strings.Repeat("question ", 33) + "que..."},
		{name: "peer", origin: "peer", isMeta: true, body: "question"},
		{name: "typed", body: "literal text", preview: `<channel source="telegram">literal text</channel>`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			content := fmt.Sprintf(`{"type":"user","uuid":"c1","isMeta":%t,"origin":{"kind":%q},"message":{"content":%q}}`+"\n", tt.isMeta, tt.origin, `<channel source="telegram">`+tt.body+`</channel>`)
			path := createTestFile(t, "meta.jsonl", content)
			results, err := parseClaudeSession(path, "project", "local")
			require.NoError(t, err)
			require.Len(t, results, 1)
			if tt.name == "peer" {
				assert.Empty(t, results[0].Messages)
				appended, _, _, _, err := claudeParseSessionFrom(path, 0, claudeIncrementalScan{})
				require.NoError(t, err)
				assert.Empty(t, appended)
				return
			}
			assert.Equal(t, tt.preview, results[0].Session.FirstMessage)
		})
	}
}

func TestClaudeChannelPreviewEnvelopes(t *testing.T) {
	for _, tt := range []struct{ name, content, preview string }{
		{"quoted angle", `<channel source="telegram" user="a > b">question</channel>`, "question"},
		{"no attributes", `<channel>question</channel>`, "question"},
		{"whitespace", " \n<channel source=\"telegram\">\n question\n</channel>\n ", "question"},
		{"adjacent", `<channel>first</channel>` + "\n" + `<channel source="telegram">second</channel>`, "first second"},
		{"malformed", `<channel source="telegram>question</channel>`, `<channel source="telegram>question</channel>`},
		{"trailing text", `<channel>question</channel> trailing`, `<channel>question</channel> trailing`},
		{"literal body", `<channel>a < b & c</channel>`, "a < b & c"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			content := fmt.Sprintf(`{"type":"user","uuid":"c1","isMeta":true,"origin":{"kind":"channel"},"message":{"content":%q}}`+"\n", tt.content)
			session, _ := runClaudeParserTest(t, "preview.jsonl", content)
			assert.Equal(t, tt.preview, session.FirstMessage)
		})
	}
}

func TestClaudeChannelRewindKeepsRetryIdentity(t *testing.T) {
	content, err := os.ReadFile("../testdata/claude_channel_rewind.jsonl")
	require.NoError(t, err)
	path := createTestFile(t, "rewind.jsonl", string(content))
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
