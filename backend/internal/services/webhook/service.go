package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huangang/codesentry/backend/pkg/logger"

	"github.com/huangang/codesentry/backend/internal/config"
	"github.com/huangang/codesentry/backend/internal/models"
	"github.com/huangang/codesentry/backend/internal/services"
	"gorm.io/gorm"
)

// Service handles webhook events from Git platforms
type Service struct {
	db                  *gorm.DB
	projectService      *services.ProjectService
	reviewService       *services.ReviewLogService
	aiService           *services.AIService
	notificationService *services.NotificationService
	configService       *services.SystemConfigService
	fileContextService  *services.FileContextService
	repoMapService      *services.RepoMapService
	reviewCacheService  *services.ReviewCacheService
	issueTrackerService *services.IssueTrackerService
	httpClient          *http.Client
}

// NewService creates a new webhook Service instance
func NewService(db *gorm.DB, aiCfg *config.OpenAIConfig) *Service {
	configService := services.NewSystemConfigService(db)
	fileContextService := services.NewFileContextService(configService)
	return &Service{
		db:                  db,
		projectService:      services.NewProjectService(db),
		reviewService:       services.NewReviewLogService(db),
		aiService:           services.NewAIService(db, aiCfg),
		notificationService: services.NewNotificationService(db),
		configService:       configService,
		fileContextService:  fileContextService,
		repoMapService:      services.NewRepoMapService(fileContextService),
		reviewCacheService:  services.NewReviewCacheService(db),
		issueTrackerService: services.NewIssueTrackerService(db),
		httpClient:          &http.Client{Timeout: 30 * time.Second},
	}
}

// GetReviewScore returns the review score for a given commit SHA
func (s *Service) GetReviewScore(commitSHA string) (*ReviewScoreResponse, error) {
	var reviewLog models.ReviewLog
	if err := s.db.Where("commit_hash = ?", commitSHA).Order("created_at DESC").First(&reviewLog).Error; err != nil {
		return nil, fmt.Errorf("review not found for commit: %s", commitSHA)
	}

	resp := &ReviewScoreResponse{
		CommitSHA: commitSHA,
		Status:    reviewLog.ReviewStatus,
		ReviewID:  reviewLog.ID,
	}

	switch reviewLog.ReviewStatus {
	case "pending", "processing", "analyzing":
		resp.Message = "Review in progress"
	case "completed":
		var project models.Project
		s.db.First(&project, reviewLog.ProjectID)
		minScore := s.getEffectiveMinScore(&project)
		passed := reviewLog.Score != nil && *reviewLog.Score >= minScore
		resp.Score = reviewLog.Score
		resp.MinScore = minScore
		resp.Passed = &passed
		resp.Message = "Review completed"
	case "skipped":
		passed := true
		resp.Passed = &passed
		resp.Message = "Skipped: " + reviewLog.ReviewResult
	case "failed":
		resp.Message = "Review failed: " + reviewLog.ErrorMessage
	}

	return resp, nil
}

// SyncReview performs a synchronous review for the given project and request
func (s *Service) SyncReview(ctx context.Context, project *models.Project, req *SyncReviewRequest) (*SyncReviewResponse, error) {
	minScore := s.getEffectiveMinScore(project)

	branch := strings.TrimPrefix(req.Ref, "refs/heads/")
	if s.isBranchIgnored(branch, project.BranchFilter) {
		return &SyncReviewResponse{
			Passed:   true,
			Score:    100,
			MinScore: minScore,
			Message:  "Branch is in ignore list, skipping review",
		}, nil
	}

	if s.isCommitAlreadyReviewed(project.ID, req.CommitSHA) {
		return &SyncReviewResponse{
			Passed:   true,
			Score:    100,
			MinScore: minScore,
			Message:  "Commit already reviewed and passed",
		}, nil
	}

	additions, deletions, filesChanged := ParseDiffStats(req.Diffs)

	reviewLog := &models.ReviewLog{
		ProjectID:     project.ID,
		EventType:     "push",
		Branch:        branch,
		CommitHash:    req.CommitSHA,
		Author:        req.Author,
		CommitMessage: req.Message,
		ReviewStatus:  "pending",
		Additions:     additions,
		Deletions:     deletions,
		FilesChanged:  filesChanged,
	}
	s.reviewService.Create(reviewLog)

	if IsEmptyDiff(req.Diffs) {
		logger.Warnf("[Webhook] WARNING: Empty commit detected for sync review commit=%s - skipping AI review", req.CommitSHA)
		reviewLog.ReviewStatus = "skipped"
		reviewLog.ReviewResult = "Empty commit - no code changes to review"
		s.reviewService.Update(reviewLog)

		return &SyncReviewResponse{
			Passed:      true,
			Score:       100,
			MinScore:    minScore,
			Message:     "Skipped: Empty commit - no code changes",
			ReviewID:    reviewLog.ID,
			FullContent: "Empty commit - no code changes to review",
		}, nil
	}

	reviewLog.ReviewStatus = "processing"
	s.reviewService.Update(reviewLog)

	// Compute diff hash and check cache
	diffHash := services.ComputeDiffHash(req.Diffs)
	reviewLog.DiffHash = diffHash
	s.reviewService.Update(reviewLog)

	if cached := s.reviewCacheService.FindCachedReview(project.ID, diffHash); cached != nil {
		reviewLog.ReviewStatus = "completed"
		reviewLog.ReviewResult = cached.ReviewResult
		reviewLog.Score = &cached.Score
		s.reviewService.Update(reviewLog)

		passed := cached.Score >= minScore
		message := fmt.Sprintf("Score: %.0f/100 (min: %.0f) [cached]", cached.Score, minScore)
		if !passed {
			message = fmt.Sprintf("Review failed: %.0f/100 (min: %.0f required) [cached]", cached.Score, minScore)
		}
		return &SyncReviewResponse{
			Passed:      passed,
			Score:       cached.Score,
			MinScore:    minScore,
			Message:     message,
			ReviewID:    reviewLog.ID,
			FullContent: cached.ReviewResult,
		}, nil
	}

	var fileContext string
	var rawFilesContext string
	var callersContext string
	var calleeContext string
	var editedFuncs []string
	var calledFuncs []string

	reviewMode := s.fileContextService.GetReviewMode()
	logger.Infof("[Webhook] Sync review mode: %s", reviewMode)

	// v1 and v2 both need local context (Diff + 10 lines)
	if reviewMode == "v1" || reviewMode == "v2" {
		fileContext, rawFilesContext, _ = s.fileContextService.BuildFileContext(project, req.Diffs, req.CommitSHA)
		editedFuncs, calledFuncs = s.fileContextService.GetModifiedFunctionNames(project, req.Diffs, req.CommitSHA)
		if fileContext != "" {
			logger.Infof("[Webhook] Built file context for sync review: %d chars", len(fileContext))
		} else {
			logger.Infof("[Webhook] File context is empty for sync review")
		}
	}

	var repoMap string
	// Only v2 needs RepoMap and Cross-file callers
	if reviewMode == "v2" {
		// Attempt to fetch global RepoMap and files map
		var globalFilesMap map[string]string
		repoMap, globalFilesMap, _ = s.repoMapService.GetRepoMap(ctx, project, req.CommitSHA)

		if repoMap == "" {
			filesMap, _ := s.fileContextService.GetFilesContentMap(project, req.Diffs, req.CommitSHA)
			if len(filesMap) > 0 {
				repoMap = s.repoMapService.GenerateMapForFiles(filesMap)
				if len(editedFuncs) > 0 || len(calledFuncs) > 0 {
					callersContext, calleeContext = s.repoMapService.FindCallers(filesMap, editedFuncs, calledFuncs)
				}
			}
		} else {
			if len(globalFilesMap) > 0 && (len(editedFuncs) > 0 || len(calledFuncs) > 0) {
				callersContext, calleeContext = s.repoMapService.FindCallers(globalFilesMap, editedFuncs, calledFuncs)
			}
		}

		if repoMap != "" {
			logger.Infof("[Webhook] Generated Repo Map for sync review: %d chars", len(repoMap))
		}
		if callersContext != "" {
			logger.Infof("[Webhook] Generated Callers Context for sync review: %d chars", len(callersContext))
		}
		if calleeContext != "" {
			logger.Infof("[Webhook] Generated Callee Context for sync review: %d chars", len(calleeContext))
		}
	}

	result, err := s.aiService.ReviewChunked(ctx, &services.ReviewRequest{
		ProjectID:      project.ID,
		Diffs:          req.Diffs,
		Commits:        req.Message,
		FileContext:    fileContext,
		RepoMap:        repoMap,
		CallersContext: callersContext,
		CalleeContext:  calleeContext,
	})

	if err != nil {
		reviewLog.ReviewStatus = "failed"
		reviewLog.ErrorMessage = err.Error()
		s.reviewService.Update(reviewLog)
		return nil, fmt.Errorf("AI review failed: %w", err)
	}

	reviewLog.ReviewStatus = "completed"
	reviewLog.ReviewResult = result.Content
	reviewLog.Score = &result.Score
	reviewLog.FinalPrompt = result.FinalPrompt
	reviewLog.RawFilesContext = rawFilesContext
	s.reviewService.Update(reviewLog)

	passed := result.Score >= minScore
	message := fmt.Sprintf("Score: %.0f/100 (min: %.0f)", result.Score, minScore)
	if !passed {
		message = fmt.Sprintf("Review failed: %.0f/100 (min: %.0f required)", result.Score, minScore)
	}

	return &SyncReviewResponse{
		Passed:      passed,
		Score:       result.Score,
		MinScore:    minScore,
		Message:     message,
		ReviewID:    reviewLog.ID,
		FullContent: result.Content,
	}, nil
}

// ProcessReviewTask processes a review task from the async queue
func (s *Service) ProcessReviewTask(ctx context.Context, task *services.ReviewTask) (retErr error) {
	logger.Infof("[TaskQueue] Processing review task: review_log_id=%d, project=%d, commit=%s",
		task.ReviewLogID, task.ProjectID, task.CommitSHA)

	// Recover from panic to ensure review status is updated to "failed"
	defer func() {
		if r := recover(); r != nil {
			panicMsg := fmt.Sprintf("panic: %v", r)
			logger.Infof("[TaskQueue] Recovered from panic in review task %d: %s", task.ReviewLogID, panicMsg)
			// Update review status to failed
			if reviewLog, err := s.reviewService.GetByID(task.ReviewLogID); err == nil {
				reviewLog.ReviewStatus = "failed"
				reviewLog.ErrorMessage = panicMsg
				s.reviewService.Update(reviewLog)
				services.PublishReviewEvent(reviewLog.ID, reviewLog.ProjectID, reviewLog.CommitHash, "failed", nil, panicMsg)
			}
			retErr = fmt.Errorf("panic recovered: %s", panicMsg)
		}
	}()

	reviewLog, err := s.reviewService.GetByID(task.ReviewLogID)
	if err != nil {
		return fmt.Errorf("review log not found: %w", err)
	}

	project, err := s.projectService.GetByID(task.ProjectID)
	if err != nil {
		return fmt.Errorf("project not found: %w", err)
	}

	reviewLog.ReviewStatus = "analyzing"
	s.reviewService.Update(reviewLog)
	services.PublishReviewEvent(reviewLog.ID, reviewLog.ProjectID, reviewLog.CommitHash, "analyzing", nil, "")

	filteredDiff := s.filterDiff(task.Diff, project.FileExtensions, project.IgnorePatterns)

	if IsEmptyDiff(filteredDiff) {
		logger.Warnf("[TaskQueue] WARNING: Empty commit detected for review_log_id=%d - skipping AI review", task.ReviewLogID)
		services.LogWarning("TaskQueue", "EmptyCommit", fmt.Sprintf("Empty commit %.8s detected, skipping AI review", task.CommitSHA), nil, "", "", map[string]interface{}{
			"project_id":    task.ProjectID,
			"review_log_id": task.ReviewLogID,
			"commit":        task.CommitSHA,
		})
		reviewLog.ReviewStatus = "skipped"
		reviewLog.ReviewResult = "Empty commit - no code changes to review"
		s.reviewService.Update(reviewLog)
		services.PublishReviewEvent(reviewLog.ID, reviewLog.ProjectID, reviewLog.CommitHash, "skipped", nil, "Empty commit - no code changes")
		return nil
	}

	// Compute diff hash and check cache
	diffHash := services.ComputeDiffHash(filteredDiff)
	reviewLog.DiffHash = diffHash
	s.reviewService.Update(reviewLog)

	if cached := s.reviewCacheService.FindCachedReview(project.ID, diffHash); cached != nil {
		reviewLog.ReviewStatus = "completed"
		reviewLog.ReviewResult = cached.ReviewResult
		reviewLog.Score = &cached.Score
		s.reviewService.Update(reviewLog)
		services.PublishReviewEvent(reviewLog.ID, reviewLog.ProjectID, reviewLog.CommitHash, "completed", &cached.Score, "")

		// Still send notification and set commit status for cached results
		s.notificationService.SendReviewNotification(project, &services.ReviewNotification{
			ProjectName:   project.Name,
			Branch:        task.Branch,
			Author:        task.Author,
			CommitMessage: task.CommitMessage,
			Score:         cached.Score,
			ReviewResult:  cached.ReviewResult,
			EventType:     task.EventType,
			MRURL:         task.MRURL,
		})

		// Auto-create issues for low-score reviews
		go s.issueTrackerService.CheckAndCreateIssue(reviewLog, project.Name)

		minScore := s.getEffectiveMinScore(project)
		statusState := "success"
		statusDesc := fmt.Sprintf("AI Review Passed: %.0f/%.0f [cached]", cached.Score, minScore)
		if cached.Score < minScore {
			statusState = "failed"
			statusDesc = fmt.Sprintf("AI Review Failed: %.0f (Min: %.0f) [cached]", cached.Score, minScore)
		}
		s.setCommitStatus(project, task.CommitSHA, statusState, statusDesc, task.GitLabProjectID)
		return nil
	}

	var fileContext string
	var rawFilesContext string
	var callersContext string
	var calleeContext string
	var editedFuncs []string
	var calledFuncs []string

	reviewMode := s.fileContextService.GetReviewMode()
	logger.Infof("[TaskQueue] Async review mode: %s", reviewMode)

	// v1 and v2 both need local context (Diff + 10 lines)
	if reviewMode == "v1" || reviewMode == "v2" {
		fileContext, rawFilesContext, _ = s.fileContextService.BuildFileContext(project, filteredDiff, task.CommitSHA)
		editedFuncs, calledFuncs = s.fileContextService.GetModifiedFunctionNames(project, filteredDiff, task.CommitSHA)
		if fileContext != "" {
			logger.Infof("[TaskQueue] Built file context for async review: %d chars", len(fileContext))
		} else {
			logger.Infof("[TaskQueue] File context is empty for async review")
		}
	}

	// Build Repo Map (if enabled)
	var repoMap string
	// Only v2 needs RepoMap and Cross-file callers
	if reviewMode == "v2" {
		// Attempt to fetch global RepoMap
		var globalFilesMap map[string]string
		repoMap, globalFilesMap, _ = s.repoMapService.GetRepoMap(ctx, project, task.CommitSHA)

		// Fallback to diff files only if global map fails or is empty
		if repoMap == "" {
			filesMap, _ := s.fileContextService.GetFilesContentMap(project, filteredDiff, task.CommitSHA)
			if len(filesMap) > 0 {
				repoMap = s.repoMapService.GenerateMapForFiles(filesMap)
				if len(editedFuncs) > 0 || len(calledFuncs) > 0 {
					callersContext, calleeContext = s.repoMapService.FindCallers(filesMap, editedFuncs, calledFuncs)
				}
			}
		} else {
			if len(globalFilesMap) > 0 && (len(editedFuncs) > 0 || len(calledFuncs) > 0) {
				callersContext, calleeContext = s.repoMapService.FindCallers(globalFilesMap, editedFuncs, calledFuncs)
			}
		}

		if repoMap != "" {
			logger.Infof("[TaskQueue] Generated Repo Map: %d chars", len(repoMap))
		}
		if callersContext != "" {
			logger.Infof("[TaskQueue] Generated Callers Context: %d chars", len(callersContext))
		}
		if calleeContext != "" {
			logger.Infof("[TaskQueue] Generated Callee Context: %d chars", len(calleeContext))
		}
	}

	result, err := s.aiService.ReviewChunked(ctx, &services.ReviewRequest{
		ProjectID:      project.ID,
		Diffs:          filteredDiff,
		Commits:        task.CommitMessage,
		FileContext:    fileContext,
		RepoMap:        repoMap,
		CallersContext: callersContext,
		CalleeContext:  calleeContext,
	})

	if err != nil {
		logger.Infof("[TaskQueue] AI review failed: %v", err)
		reviewLog.ReviewStatus = "failed"
		reviewLog.ErrorMessage = err.Error()
		s.reviewService.Update(reviewLog)
		services.PublishReviewEvent(reviewLog.ID, reviewLog.ProjectID, reviewLog.CommitHash, "failed", nil, err.Error())
		s.setCommitStatus(project, task.CommitSHA, "failed", "AI Review Failed", task.GitLabProjectID)
		return err
	}

	logger.Infof("[TaskQueue] AI review completed, score: %.1f", result.Score)
	reviewLog.ReviewStatus = "completed"
	reviewLog.ReviewResult = result.Content
	reviewLog.Score = &result.Score
	reviewLog.FinalPrompt = result.FinalPrompt
	reviewLog.RawFilesContext = rawFilesContext
	s.reviewService.Update(reviewLog)
	services.PublishReviewEvent(reviewLog.ID, reviewLog.ProjectID, reviewLog.CommitHash, "completed", &result.Score, "")

	s.notificationService.SendReviewNotification(project, &services.ReviewNotification{
		ProjectName:   project.Name,
		Branch:        task.Branch,
		Author:        task.Author,
		CommitMessage: task.CommitMessage,
		Score:         result.Score,
		ReviewResult:  result.Content,
		EventType:     task.EventType,
		MRURL:         task.MRURL,
	})

	// Auto-create issues for low-score reviews
	go s.issueTrackerService.CheckAndCreateIssue(reviewLog, project.Name)

	if project.CommentEnabled {
		comment := s.formatReviewComment(result.Score, result.Content)
		var commentErr error

		if task.MRNumber != nil {
			// Post MR/PR comment for merge request events
			switch project.Platform {
			case "gitlab":
				commentErr = s.postGitLabMRComment(project, *task.MRNumber, comment)
			case "github":
				commentErr = s.postGitHubPRComment(project, *task.MRNumber, comment)
			case "bitbucket":
				commentErr = s.postBitbucketPRComment(project, *task.MRNumber, comment)
			}
		} else if task.CommitSHA != "" {
			// Post commit comment for push events
			switch project.Platform {
			case "gitlab":
				commentErr = s.postGitLabCommitComment(project, task.CommitSHA, comment)
			case "github":
				commentErr = s.postGitHubCommitComment(project, task.CommitSHA, comment)
			case "bitbucket":
				commentErr = s.postBitbucketCommitComment(project, task.CommitSHA, comment)
			}
		}

		if commentErr != nil {
			logger.Infof("[TaskQueue] Failed to post comment: %v", commentErr)
		} else {
			reviewLog.CommentPosted = true
			s.reviewService.Update(reviewLog)
		}
	}

	minScore := s.getEffectiveMinScore(project)
	statusState := "success"
	statusDesc := fmt.Sprintf("AI Review Passed: %.0f/%.0f", result.Score, minScore)
	if result.Score < minScore {
		statusState = "failed"
		statusDesc = fmt.Sprintf("AI Review Failed: %.0f (Min: %.0f)", result.Score, minScore)
	}
	s.setCommitStatus(project, task.CommitSHA, statusState, statusDesc, task.GitLabProjectID)

	return nil
}
