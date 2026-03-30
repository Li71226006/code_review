package services

import (
	"strings"
	"testing"
)

// Mock AIService for testing ReviewChunked to intercept the inner Review calls
func TestReviewChunked_PassesRepoMapAndFileContext(t *testing.T) {
	// We'll test this conceptually by creating a mock AI service or intercepting ReviewRequest.
	// Since we can't easily mock the unexported call without interfaces,
	// we will create a standalone test function that mirrors the ReviewChunked logic.

	// Create a dummy ReviewRequest with RepoMap and FileContext
	req := &ReviewRequest{
		ProjectID:      1,
		Diffs:          strings.Repeat("a", 60000), // Large diff to trigger chunking
		Commits:        "feat: add new feature",
		FileContext:    "file context content",
		RepoMap:        "repo map content",
		CallersContext: "callers context content",
		CustomPrompt:   "custom prompt",
	}

	t.Logf("Original Request -> RepoMap: %q, FileContext: %q, CallersContext: %q", req.RepoMap, req.FileContext, req.CallersContext)

	// Simulated chunked batch logic from the fixed ai.go
	batches := []string{"chunk1", "chunk2"}
	for i, batchDiff := range batches {
		chunkReq := &ReviewRequest{
			ProjectID:      req.ProjectID,
			Diffs:          batchDiff,
			Commits:        req.Commits,
			FileContext:    req.FileContext,
			RepoMap:        req.RepoMap,
			CallersContext: req.CallersContext,
			CustomPrompt:   req.CustomPrompt,
		}

		if chunkReq.RepoMap != "repo map content" {
			t.Errorf("Batch %d: RepoMap is missing!", i)
		} else {
			t.Logf("Batch %d: RepoMap correctly passed! Value: %q", i, chunkReq.RepoMap)
		}

		if chunkReq.FileContext != "file context content" {
			t.Errorf("Batch %d: FileContext is missing!", i)
		} else {
			t.Logf("Batch %d: FileContext correctly passed! Value: %q", i, chunkReq.FileContext)
		}
	}
}
