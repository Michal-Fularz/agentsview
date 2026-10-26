package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandCodeProviderDiscoversSessions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectDir := filepath.Join(root, "users-alice-code-sample-project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sess_a.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sess_a.meta.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sess_a.checkpoints.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "sess_a.prompts.jsonl"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "notes.txt"), []byte("ignore"), 0o644))

	provider, ok := NewProvider(AgentCommandCode, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)

	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, AgentCommandCode, sources[0].Provider)
	assert.Equal(t, filepath.Join(projectDir, "sess_a.jsonl"), sources[0].DisplayPath)
}

func TestCommandCodeProviderFindsSourceFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectDir := filepath.Join(root, "users-alice-code-sample-project")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	path := filepath.Join(projectDir, "sess_123.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o644))

	provider, ok := NewProvider(AgentCommandCode, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)

	found, ok, err := provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: "sess_123",
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, path, found.DisplayPath)

	_, ok, err = provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: "sess_missing",
	})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCommandCodeProviderParsesSession(t *testing.T) {
	t.Parallel()

	content := `{"id":"m1","timestamp":"2026-06-01T10:00:00Z","sessionId":"sess_123","role":"user","content":[{"type":"text","text":"Inspect server logs"}],"gitBranch":"feature/command-code","metadata":{"version":2,"cwd":"/Users/alice/code/sample-project"}}
{"id":"m2","timestamp":"2026-06-01T10:00:01Z","sessionId":"sess_123","role":"assistant","content":[{"type":"reasoning","text":"I should read the logs first."},{"type":"tool-call","toolCallId":"tc1","toolName":"Read","input":{"file_path":"server.log"}}],"gitBranch":"feature/command-code","metadata":{"version":2}}
{"id":"m3","timestamp":"2026-06-01T10:00:02Z","sessionId":"sess_123","role":"tool","content":[{"type":"tool-result","toolCallId":"tc1","toolName":"Read","output":{"type":"text","value":"error: boom"}}],"gitBranch":"feature/command-code","metadata":{"version":2}}
{"id":"m4","timestamp":"2026-06-01T10:00:03Z","sessionId":"sess_123","role":"assistant","content":[{"type":"text","text":"The error is in the startup path."}],"gitBranch":"feature/command-code","metadata":{"version":2}}`

	root := t.TempDir()
	path := filepath.Join(root, "project", "sess_123.jsonl")
	writeSourceFile(t, path, content)
	metaPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
	require.NoError(t, os.WriteFile(metaPath, []byte(`{"title":"Startup investigation"}`), 0o644))

	provider, ok := NewProvider(AgentCommandCode, ProviderConfig{
		Roots:   []string{root},
		Machine: "local",
	})
	require.True(t, ok)
	source, found, err := provider.FindSource(t.Context(), FindSourceRequest{
		RawSessionID: "sess_123",
	})
	require.NoError(t, err)
	require.True(t, found)
	outcome, err := provider.Parse(t.Context(), ParseRequest{
		Source:  source,
		Machine: "local",
	})
	require.NoError(t, err)
	require.Len(t, outcome.Results, 1)

	sess := outcome.Results[0].Result.Session
	msgs := outcome.Results[0].Result.Messages
	require.Len(t, msgs, 4)

	assert.Equal(t, "commandcode:sess_123", sess.ID)
	assert.Equal(t, AgentCommandCode, sess.Agent)
	assert.Equal(t, "sample_project", sess.Project)
	assert.Equal(t, "/Users/alice/code/sample-project", sess.Cwd)
	assert.Equal(t, "feature/command-code", sess.GitBranch)
	assert.Equal(t, "Inspect server logs", sess.FirstMessage)
	assert.Equal(t, "Startup investigation", sess.SessionName)
	assert.Equal(t, 4, sess.MessageCount)
	assert.Equal(t, 1, sess.UserMessageCount)

	assert.Equal(t, RoleUser, msgs[0].Role)
	assert.Equal(t, "Inspect server logs", msgs[0].Content)

	assert.Equal(t, RoleAssistant, msgs[1].Role)
	assert.True(t, msgs[1].HasThinking)
	assert.Equal(t, "I should read the logs first.", msgs[1].ThinkingText)
	require.Len(t, msgs[1].ToolCalls, 1)
	assert.Equal(t, "tc1", msgs[1].ToolCalls[0].ToolUseID)
	assert.Equal(t, "Read", msgs[1].ToolCalls[0].ToolName)

	assert.Equal(t, RoleUser, msgs[2].Role)
	require.Len(t, msgs[2].ToolResults, 1)
	assert.Equal(t, "tc1", msgs[2].ToolResults[0].ToolUseID)
	assert.Equal(t, "error: boom", DecodeContent(msgs[2].ToolResults[0].ContentRaw))

	assert.Equal(t, RoleAssistant, msgs[3].Role)
	assert.Equal(t, "The error is in the startup path.", msgs[3].Content)
}

func parseCommandCodeFixture(t *testing.T, content, meta string) (ParseResult, error) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "project", "00000000-0000-0000-0000-000000000001.jsonl")
	writeSourceFile(t, path, content)
	if meta != "" {
		writeSourceFile(t, strings.TrimSuffix(path, ".jsonl")+".meta.json", meta)
	}
	provider, ok := NewProvider(AgentCommandCode, ProviderConfig{Roots: []string{root}})
	require.True(t, ok)
	sources, err := provider.Discover(t.Context())
	require.NoError(t, err)
	require.Len(t, sources, 1)
	outcome, err := provider.Parse(t.Context(), ParseRequest{Source: sources[0]})
	if err != nil {
		return ParseResult{}, err
	}
	require.Len(t, outcome.Results, 1)
	return outcome.Results[0].Result, nil
}

func TestCommandCodeProviderV3Transcript(t *testing.T) {
	t.Parallel()
	result, err := parseCommandCodeFixture(t, `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:00Z","cwd":"/example/project"}
{"type":"message","timestamp":"2026-10-14T21:00:02Z","message":{"role":"user","content":[{"type":"text","text":"Inspect server logs"}]}}
{"type":"message","timestamp":"2026-10-14T21:00:03Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Read the logs first."},{"type":"text","text":"Reading."},{"type":"tool_use","id":"call_1","name":"Read","input":{"file_path":"server.log"}}]}}
{"type":"message","timestamp":"2026-10-14T21:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"error: boom"}]}]}}
{"type":"message","timestamp":"2026-10-14T21:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"Startup failed."}]}}
`, "")
	require.NoError(t, err)
	sess, msgs := result.Session, result.Messages
	require.Len(t, msgs, 4)
	assert.Equal(t, "commandcode:00000000-0000-0000-0000-000000000001", sess.ID)
	assert.Equal(t, "3", sess.SourceVersion)
	assert.Equal(t, "/example/project", sess.Cwd)
	assert.Equal(t, "Inspect server logs", sess.FirstMessage)
	assert.Equal(t, 1, sess.UserMessageCount)
	assert.Equal(t, "Reading.", msgs[1].Content)
	assert.True(t, msgs[1].HasThinking)
	assert.Equal(t, "Read the logs first.", msgs[1].ThinkingText)
	assert.True(t, msgs[1].HasToolUse)
	require.Len(t, msgs[1].ToolCalls, 1)
	assert.Equal(t, "call_1", msgs[1].ToolCalls[0].ToolUseID)
	assert.Equal(t, "Read", msgs[1].ToolCalls[0].ToolName)
	assert.JSONEq(t, `{"file_path":"server.log"}`, msgs[1].ToolCalls[0].InputJSON)
	require.Len(t, msgs[2].ToolResults, 1)
	assert.Equal(t, "call_1", msgs[2].ToolResults[0].ToolUseID)
	assert.Equal(t, "error: boom", DecodeContent(msgs[2].ToolResults[0].ContentRaw))
}

func TestCommandCodeProviderModelAndInlineEffort(t *testing.T) {
	t.Parallel()
	result, err := parseCommandCodeFixture(t, `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:00Z","cwd":"/example/project"}
{"type":"message","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Unattributed"}]}}
{"type":"model_change","timestamp":"2026-10-14T21:00:02Z","model":"model-a"}
{"type":"message","timestamp":"2026-10-14T21:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"Inline wins"}]},"model":"model-b","effort":"low"}
{"type":"message","timestamp":"2026-10-14T21:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"Carried model"}]}}
`, "")
	require.NoError(t, err)
	require.Len(t, result.Messages, 3)
	for i, want := range []string{"", "model-b", "model-a"} {
		assert.Equal(t, want, result.Messages[i].Model)
	}
	assert.Empty(t, result.Messages[0].ReasoningEffort)
	assert.Equal(t, "low", result.Messages[1].ReasoningEffort)
}

func TestCommandCodeProviderLatestSessionInfoAndMetaTitlePrecedence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, entries, want string }{
		{"latest name", `{"type":"session_info","timestamp":"2026-10-14T21:00:01Z","name":"Initial"}
{"type":"session_info","timestamp":"2026-10-14T21:00:02Z","name":"Renamed"}
`, "Renamed"},
		{"cleared name", `{"type":"session_info","timestamp":"2026-10-14T21:00:01Z","name":"Initial"}
{"type":"session_info","timestamp":"2026-10-14T21:00:02Z"}
`, "Sidecar"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			content := `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:00Z","cwd":"/example/project"}` + "\n" + `{"type":"message","timestamp":"2026-10-14T21:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}` + "\n" + tt.entries
			result, err := parseCommandCodeFixture(t, content, `{"title":"Sidecar"}`)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Session.SessionName)
		})
	}
}

func TestCommandCodeProviderForkUsage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, content, meta, wantParent string
	}{
		{
			name: "header parent on Windows",
			content: `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:02Z","parentSession":"C:\\sessions\\parent.jsonl"}
{"type":"message","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Copied"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
{"type":"message","timestamp":"2026-10-14T21:00:03Z","message":{"role":"user","content":[{"type":"text","text":"Continue"}]}}
{"type":"message","timestamp":"2026-10-14T21:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"New"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
`,
			wantParent: "commandcode:parent",
		},
		{
			name: "CLI fork",
			content: `{"type":"session","version":3,"id":"parent","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:00Z"}
{"type":"message","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Copied"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
{"type":"message","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:03Z","message":{"role":"user","content":[{"type":"text","text":"Continue"}]}}
{"type":"message","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"New"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
`,
			meta:       `{"parentSessionId":"parent","forkedAt":"2026-10-14T21:00:02Z"}`,
			wantParent: "commandcode:parent",
		},
		{
			name: "fork of a fork",
			content: `{"type":"session","version":3,"id":"parent","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:00Z","parentSession":"/example/grandparent.jsonl"}
{"type":"message","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Copied"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
{"type":"message","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:03Z","message":{"role":"user","content":[{"type":"text","text":"Continue"}]}}
{"type":"message","sessionId":"00000000-0000-0000-0000-000000000001","timestamp":"2026-10-14T21:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"New"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
`,
			meta:       `{"parentSessionId":"parent","forkedAt":"2026-10-14T21:00:02Z"}`,
			wantParent: "commandcode:parent",
		},
		{
			name: "non-fork rewritten header",
			content: `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:02Z"}
{"type":"message","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Copied"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
{"type":"message","timestamp":"2026-10-14T21:00:03Z","message":{"role":"user","content":[{"type":"text","text":"Continue"}]}}
{"type":"message","timestamp":"2026-10-14T21:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"New"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := parseCommandCodeFixture(t, tt.content, tt.meta)
			require.NoError(t, err)
			assert.Equal(t, tt.wantParent, result.Session.ParentSessionID)
			require.Len(t, result.Messages, 3)
			if tt.wantParent != "" {
				assert.Equal(t, RelFork, result.Session.RelationshipType)
				assert.Empty(t, result.Messages[0].TokenUsage)
				assert.False(t, result.Messages[0].HasContextTokens)
				assert.False(t, result.Messages[0].HasOutputTokens)
				assert.Equal(t, 5, result.Session.TotalOutputTokens)
			} else {
				assert.NotEmpty(t, result.Messages[0].TokenUsage)
				assert.Equal(t, 10, result.Session.TotalOutputTokens)
			}
			assert.Equal(t, 10, result.Session.PeakContextTokens)
			assert.NotEmpty(t, result.Messages[2].TokenUsage)
		})
	}
}

func TestCommandCodeProviderForkSameMillisecondUsage(t *testing.T) {
	t.Parallel()
	result, err := parseCommandCodeFixture(t, `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:02.123Z","parentSession":"/example/parent.jsonl"}
{"type":"message","timestamp":"2026-10-14T21:00:02.123Z","message":{"role":"assistant","content":[{"type":"text","text":"Copied answer"}]},"usage":{"inputTokens":10,"outputTokens":5,"cacheReadTokens":0,"cacheWriteTokens":0}}
`, `{"parentSessionId":"parent","forkedAt":"2026-10-14T21:00:02.123Z"}`)
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	assert.Equal(t, "Copied answer", result.Messages[0].Content)
	assert.Empty(t, result.Messages[0].TokenUsage)
	assert.False(t, result.Messages[0].HasOutputTokens)
	assert.Zero(t, result.Session.TotalOutputTokens)
}

func TestCommandCodeProviderRejectsFutureVersion(t *testing.T) {
	t.Parallel()
	_, err := parseCommandCodeFixture(t, `{"type":"session","version":4,"timestamp":"2026-10-14T21:00:00Z"}
{"type":"message","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}
`, "")
	require.ErrorContains(t, err, "unsupported Command Code session format version 4")
}

func TestCommandCodeProviderUsageWithoutContent(t *testing.T) {
	t.Parallel()
	result, err := parseCommandCodeFixture(t, `{"type":"session","version":3,"timestamp":"2026-10-14T21:00:00Z"}
{"type":"message","timestamp":"2026-10-14T21:00:01Z","message":{"role":"assistant","content":[]},"usage":{"inputTokens":1000,"outputTokens":150,"cacheReadTokens":300,"cacheWriteTokens":200,"cacheWriteTokens1h":100}}
{"type":"message","timestamp":"2026-10-14T21:00:02Z","message":{"role":"assistant","content":[]}}
`, "")
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)
	msg := result.Messages[0]
	assert.Equal(t, RoleAssistant, msg.Role)
	assert.Empty(t, msg.Content)
	assert.JSONEq(t, `{"cache_creation":{"ephemeral_1h_input_tokens":100},"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"input_tokens":500,"output_tokens":150}`, string(msg.TokenUsage))
	assert.Equal(t, 1000, msg.ContextTokens)
	assert.Equal(t, 150, msg.OutputTokens)
	assert.Equal(t, 150, result.Session.TotalOutputTokens)
	assert.Equal(t, 1000, result.Session.PeakContextTokens)
	assert.True(t, result.Session.HasPeakContextTokens)
}
