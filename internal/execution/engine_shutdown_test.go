package execution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// SpyEngine wraps an AgentEngine and tracks Shutdown calls.
// Exported so cmd/waza tests can use it if needed.
type SpyEngine struct {
	Inner         AgentEngine
	ShutdownCount atomic.Int32
	ShutdownErr   error // error to return from Shutdown
}

func NewSpyEngine(inner AgentEngine) *SpyEngine {
	return &SpyEngine{Inner: inner}
}

func (s *SpyEngine) Initialize(ctx context.Context) error {
	return s.Inner.Initialize(ctx)
}

func (s *SpyEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	return s.Inner.Execute(ctx, req)
}

func (s *SpyEngine) Shutdown(ctx context.Context) error {
	s.ShutdownCount.Add(1)
	if s.ShutdownErr != nil {
		return s.ShutdownErr
	}
	return s.Inner.Shutdown(ctx)
}

func (s *SpyEngine) SessionUsage(sessionID string) *models.UsageStats {
	return s.Inner.SessionUsage(sessionID)
}

func (s *SpyEngine) PreservedWorkspaces() []string {
	return s.Inner.PreservedWorkspaces()
}

func (s *SpyEngine) WasCalled() bool {
	return s.ShutdownCount.Load() > 0
}

func (s *SpyEngine) CallCount() int {
	return int(s.ShutdownCount.Load())
}

// ---------------------------------------------------------------------------
// MockEngine.Shutdown contract
// ---------------------------------------------------------------------------

func TestMockEngine_Shutdown_ReturnsNilError(t *testing.T) {
	engine := NewMockEngine("test-model")
	err := engine.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestMockEngine_Shutdown_Idempotent(t *testing.T) {
	engine := NewMockEngine("test-model")

	for i := 0; i < 5; i++ {
		err := engine.Shutdown(context.Background())
		assert.NoError(t, err, "Shutdown call %d should not error", i+1)
	}
}

func TestMockEngine_Shutdown_AfterExecute(t *testing.T) {
	engine := NewMockEngine("test-model")
	ctx := context.Background()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	_, err = engine.Execute(ctx, &ExecutionRequest{
		Message: "hello",
	})
	require.NoError(t, err)

	err = engine.Shutdown(ctx)
	assert.NoError(t, err, "Shutdown after Execute should succeed")
}

func TestMockEngine_Shutdown_WithCancelledContext(t *testing.T) {
	engine := NewMockEngine("test-model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := engine.Shutdown(ctx)
	assert.NoError(t, err, "MockEngine.Shutdown should succeed even with canceled context")
}

// ---------------------------------------------------------------------------
// SpyEngine — verifies tracking works correctly
// ---------------------------------------------------------------------------

func TestSpyEngine_TracksShutdownCalls(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)

	assert.False(t, spy.WasCalled(), "Shutdown should not be called yet")
	assert.Equal(t, 0, spy.CallCount())

	err := spy.Shutdown(context.Background())
	require.NoError(t, err)

	assert.True(t, spy.WasCalled(), "Shutdown should have been called")
	assert.Equal(t, 1, spy.CallCount())
}

func TestSpyEngine_TracksMultipleShutdownCalls(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)

	for i := 0; i < 3; i++ {
		err := spy.Shutdown(context.Background())
		require.NoError(t, err)
	}

	assert.Equal(t, 3, spy.CallCount())
}

func TestSpyEngine_PropagatesShutdownError(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)
	spy.ShutdownErr = errors.New("shutdown failed: connection refused")

	err := spy.Shutdown(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown failed")
	assert.True(t, spy.WasCalled(), "Shutdown should still be tracked even on error")
}

func TestSpyEngine_DelegatesExecute(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)

	err := spy.Initialize(context.Background())
	require.NoError(t, err)

	resp, err := spy.Execute(context.Background(), &ExecutionRequest{
		Message: "hello",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Contains(t, resp.FinalOutput, "Mock response")
}

// ---------------------------------------------------------------------------
// CopilotEngine.Shutdown contract (no SDK required)
// ---------------------------------------------------------------------------

func TestCopilotEngine_Shutdown_NoInit(t *testing.T) {
	// Shutdown on an engine that was never initialized should be safe.
	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	err := engine.Shutdown(context.Background())
	assert.NoError(t, err, "Shutdown on uninitialized CopilotEngine should not error")
}

func TestCopilotEngine_Shutdown_Idempotent(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).Build()

	for i := 0; i < 3; i++ {
		err := engine.Shutdown(context.Background())
		assert.NoError(t, err, "Shutdown call %d should not error", i+1)
	}
}

func TestCopilotEngine_Shutdown_CleansWorkspace(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).Build()

	// Simulate a workspace existing (without running the full SDK)
	tmpDir := t.TempDir()
	engine.workspacesMu.Lock()
	engine.workspaces = append(engine.workspaces, tmpDir)
	engine.workspacesMu.Unlock()

	err := engine.Shutdown(context.Background())
	require.NoError(t, err)

	// After shutdown, workspace should be cleared
	engine.workspacesMu.Lock()
	defer engine.workspacesMu.Unlock()
	require.Empty(t, engine.workspaces)
}

func TestCopilotEngine_Shutdown_WithCancelledContext(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := engine.Shutdown(ctx)
	assert.NoError(t, err, "CopilotEngine.Shutdown should handle canceled context gracefully")
}

// ---------------------------------------------------------------------------
// NoCleanup behavior
// ---------------------------------------------------------------------------

func TestMockEngine_NoCleanup_PreservesWorkspace(t *testing.T) {
	tmp := t.TempDir()
	// MockEngine uses os.MkdirTemp which relies on these env vars to determine where to create temp dirs
	t.Setenv("TMPDIR", tmp) // Unix/macOS
	t.Setenv("TMP", tmp)    // Windows

	engine := NewMockEngineBuilder("test-model").WithNoCleanup(true).Build()
	ctx := context.Background()

	require.NoError(t, engine.Initialize(ctx))
	resp, err := engine.Execute(ctx, &ExecutionRequest{Message: "hello"})
	require.NoError(t, err)

	wsDir := resp.WorkspaceDir
	require.DirExists(t, wsDir, "workspace should exist before shutdown")

	require.NoError(t, engine.Shutdown(ctx))

	assert.DirExists(t, wsDir, "workspace should still exist after shutdown with no-cleanup")
	assert.Equal(t, []string{wsDir}, engine.PreservedWorkspaces())
}

func TestMockEngine_NoCleanup_PreservesAcrossExecutions(t *testing.T) {
	// MockEngine uses os.MkdirTemp which relies on these env vars to determine where to create temp dirs
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // Unix/macOS
	t.Setenv("TMP", tmp)    // Windows

	engine := NewMockEngineBuilder("test-model").WithNoCleanup(true).Build()
	ctx := context.Background()

	require.NoError(t, engine.Initialize(ctx))

	resp1, err := engine.Execute(ctx, &ExecutionRequest{Message: "first"})
	require.NoError(t, err)
	ws1 := resp1.WorkspaceDir

	resp2, err := engine.Execute(ctx, &ExecutionRequest{Message: "second"})
	require.NoError(t, err)
	ws2 := resp2.WorkspaceDir

	// Both workspaces should exist (no-cleanup prevents per-execute removal)
	assert.DirExists(t, ws1, "first workspace should still exist")
	assert.DirExists(t, ws2, "second workspace should still exist")

	require.NoError(t, engine.Shutdown(ctx))

	assert.Equal(t, []string{ws1, ws2}, engine.PreservedWorkspaces())
}

func TestMockEngine_Cleanup_RemovesWorkspace(t *testing.T) {
	engine := NewMockEngine("test-model")
	ctx := context.Background()

	require.NoError(t, engine.Initialize(ctx))
	resp, err := engine.Execute(ctx, &ExecutionRequest{Message: "hello"})
	require.NoError(t, err)

	wsDir := resp.WorkspaceDir
	require.DirExists(t, wsDir)

	require.NoError(t, engine.Shutdown(ctx))

	assert.NoDirExists(t, wsDir, "workspace should be removed after shutdown without no-cleanup")
	assert.Nil(t, engine.PreservedWorkspaces())
}

func TestCopilotEngine_NoCleanup_PreservesWorkspace(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).
		WithNoCleanup(true).
		Build()

	// Simulate a workspace existing
	tmpDir := t.TempDir()
	engine.workspacesMu.Lock()
	engine.workspaces = append(engine.workspaces, tmpDir)
	engine.workspacesMu.Unlock()

	require.NoError(t, engine.Shutdown(context.Background()))

	assert.DirExists(t, tmpDir, "workspace should still exist with no-cleanup")
	assert.Equal(t, []string{tmpDir}, engine.PreservedWorkspaces())
}

func TestCopilotEngine_NoCleanup_SkipsSessionDeletion(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	engine := &CopilotEngine{
		defaultModelID: "test-model",
		client:         clientMock,
		noCleanup:      true,
		sessions: map[string]CopilotSession{
			"session-1": nil,
		},
	}

	// Only expect Stop(), NOT DeleteSession()
	clientMock.EXPECT().Stop().Return(nil)

	require.NoError(t, engine.Shutdown(context.Background()))
}

// ---------------------------------------------------------------------------
// AgentEngine interface compliance — static check
// ---------------------------------------------------------------------------

var (
	_ AgentEngine = (*MockEngine)(nil)
	_ AgentEngine = (*CopilotEngine)(nil)
	_ AgentEngine = (*SpyEngine)(nil)
)
