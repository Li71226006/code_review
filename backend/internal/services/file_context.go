package services

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huangang/codesentry/backend/pkg/logger"

	"github.com/huangang/codesentry/backend/internal/models"
)

type FileContextService struct {
	httpClient    *http.Client
	configService *SystemConfigService
}

func NewFileContextService(configService *SystemConfigService) *FileContextService {
	return &FileContextService{
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		configService: configService,
	}
}

type FileContext struct {
	FilePath       string
	Content        string
	ModifiedRanges []LineRange
	Language       string
}

type LineRange struct {
	Start int
	End   int
}

func (s *FileContextService) GetReviewMode() string {
	mode := s.configService.GetFileContextConfig().ReviewMode
	if mode == "" {
		return "v0" // default
	}
	return mode
}

func (s *FileContextService) GetMaxFileSize() int {
	return s.configService.GetFileContextConfig().MaxFileSize
}

func (s *FileContextService) GetMaxFiles() int {
	return s.configService.GetFileContextConfig().MaxFiles
}

func (s *FileContextService) BuildFileContext(project *models.Project, diff string, ref string) (string, string, error) {
	mode := s.GetReviewMode()
	logger.Infof("[FileContext] BuildFileContext called with mode: %s for ref: %s", mode, ref)

	if mode == "v0" {
		return "", "", nil // No extra context for v0
	}

	// For v1, use basic Diff +/- 10 lines extraction without AST grouping
	if mode == "v1" {
		return s.buildSimpleContext(project, diff, ref)
	}

	// For v2, use Tree-sitter AST to extract complete function/class definitions
	return s.BuildFunctionContext(project, diff, ref)
}

// buildSimpleContext expands each modified hunk by +/- 10 lines, merges overlapping regions,
// and returns the raw blocks without AST function wrapping.
func (s *FileContextService) buildSimpleContext(project *models.Project, diff string, ref string) (string, string, error) {
	files := ParseDiffToFiles(diff)
	if len(files) == 0 {
		return "", "", nil
	}

	maxFiles := s.GetMaxFiles()
	if len(files) > maxFiles {
		sort.Slice(files, func(i, j int) bool {
			return (files[i].Additions + files[i].Deletions) > (files[j].Additions + files[j].Deletions)
		})
		files = files[:maxFiles]
	}

	var builder strings.Builder
	var rawBuilder strings.Builder

	maxFileSize := s.GetMaxFileSize()
	totalExtracted := 0

	for _, file := range files {
		if file.FilePath == "" || file.FilePath == "unknown" || file.FilePath == "/dev/null" {
			continue
		}

		content, err := s.fetchFileContent(project, file.FilePath, ref)
		if err != nil {
			logger.Infof("[FileContext] Failed to fetch file content for %s: %v", file.FilePath, err)
			continue
		}

		rawBuilder.WriteString(fmt.Sprintf("=== RAW FILE: %s ===\n%s\n\n", file.FilePath, content))

		if len(content) > maxFileSize {
			logger.Infof("[FileContext] File %s exceeds max size (%d > %d), skipping context", file.FilePath, len(content), maxFileSize)
			continue
		}

		hunks := extractDiffHunks(file.Content)
		if len(hunks) == 0 {
			continue
		}

		lines := strings.Split(content, "\n")

		// 1. Calculate expanded ranges (+/- 10 lines)
		var ranges []LineRange
		contextLines := 10
		for _, hunk := range hunks {
			start := hunk.NewStart - 1 - contextLines
			if start < 0 {
				start = 0
			}
			end := hunk.NewStart - 1 + hunk.NewCount + contextLines
			if end > len(lines) {
				end = len(lines)
			}
			ranges = append(ranges, LineRange{Start: start + 1, End: end})
		}

		// 2. Merge overlapping ranges
		sort.Slice(ranges, func(i, j int) bool {
			return ranges[i].Start < ranges[j].Start
		})
		var mergedRanges []LineRange
		if len(ranges) > 0 {
			current := ranges[0]
			for i := 1; i < len(ranges); i++ {
				if ranges[i].Start <= current.End+1 { // Merge if overlapping or exactly adjacent
					if ranges[i].End > current.End {
						current.End = ranges[i].End
					}
				} else {
					mergedRanges = append(mergedRanges, current)
					current = ranges[i]
				}
			}
			mergedRanges = append(mergedRanges, current)
		}

		// 3. Build the text for each merged range, injecting hunk lines where appropriate
		for _, mr := range mergedRanges {
			var blockBuilder strings.Builder
			startIdx := mr.Start - 1
			endIdx := mr.End

			currentLineNum := startIdx + 1
			for lineIdx := startIdx; lineIdx < endIdx; {
				lineNum := lineIdx + 1

				// Check if this line falls into any original hunk
				var activeHunk *DiffHunk
				for i, hunk := range hunks {
					if lineNum >= hunk.NewStart && lineNum < hunk.NewStart+hunk.NewCount {
						activeHunk = &hunks[i]
						break
					}
				}

				if activeHunk != nil {
					hunkNewLinesProcessed := 0
					for _, hunkLine := range activeHunk.Lines {
						if strings.HasPrefix(hunkLine, "-") {
							blockBuilder.WriteString(fmt.Sprintf("    | %s\n", hunkLine))
						} else if strings.HasPrefix(hunkLine, "+") {
							blockBuilder.WriteString(fmt.Sprintf("%3d | %s\n", currentLineNum, hunkLine))
							currentLineNum++
							hunkNewLinesProcessed++
						} else if strings.HasPrefix(hunkLine, " ") || hunkLine == "" {
							blockBuilder.WriteString(fmt.Sprintf("%3d | %s\n", currentLineNum, hunkLine))
							currentLineNum++
							hunkNewLinesProcessed++
						}
					}
					lineIdx += hunkNewLinesProcessed
				} else {
					blockBuilder.WriteString(fmt.Sprintf("%3d |   %s\n", lineNum, lines[lineIdx]))
					lineIdx++
					currentLineNum++
				}
			}

			language := detectLanguage(file.FilePath)
			builder.WriteString(fmt.Sprintf("#### `%s` (Lines %d-%d)\n", file.FilePath, mr.Start, mr.End))
			builder.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", language, strings.TrimRight(blockBuilder.String(), "\n")))
			totalExtracted++
		}
	}

	if totalExtracted == 0 {
		return "", rawBuilder.String(), nil
	}

	return builder.String(), rawBuilder.String(), nil
}

// GetModifiedFunctionNames returns a list of unique function names that were modified
// It returns two slices:
// 1. editedFuncs: names of the functions that contain the modifications
// 2. calledFuncs: names of any functions that are being called within the added/deleted lines
func (s *FileContextService) GetModifiedFunctionNames(project *models.Project, diff string, ref string) (editedFuncs []string, calledFuncs []string) {
	files := ParseDiffToFiles(diff)
	if len(files) == 0 {
		return nil, nil
	}

	editedMap := make(map[string]bool)
	calledMap := make(map[string]bool)
	maxFileSize := s.GetMaxFileSize()
	repoMapSvc := &RepoMapService{}

	// Regex to find function calls in diff lines: e.g. "func_name(" or "obj.method_name("
	callRegex := regexp.MustCompile(`\b([a-zA-Z_]\w*)\s*\(`)

	for _, file := range files {
		if file.FilePath == "" || file.FilePath == "unknown" || file.FilePath == "/dev/null" {
			continue
		}

		// 1. Extract functions being called within the diff lines
		hunks := extractDiffHunks(file.Content)
		for _, hunk := range hunks {
			for _, line := range hunk.Lines {
				if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
					matches := callRegex.FindAllStringSubmatch(line, -1)
					for _, match := range matches {
						if len(match) > 1 {
							funcName := match[1]
							// Filter out common language keywords
							if !isCommonKeyword(funcName) {
								calledMap[funcName] = true
							}
						}
					}
				}
			}
		}

		// 2. Extract the functions that contain the modifications
		content, err := s.fetchFileContent(project, file.FilePath, ref)
		if err != nil {
			logger.Infof("[FileContext] GetModifiedFunctionNames: Failed to fetch file content for %s: %v", file.FilePath, err)
			continue
		}

		if len(content) > maxFileSize {
			logger.Infof("[FileContext] GetModifiedFunctionNames: File %s exceeds max size (%d > %d)", file.FilePath, len(content), maxFileSize)
			continue
		}

		modifiedRanges := extractModifiedRanges(file.Content)
		defs := repoMapSvc.extractDefinitions(file.FilePath, content)

		for _, def := range defs {
			if def.Type != "func" && def.Type != "method" {
				continue
			}

			for _, r := range modifiedRanges {
				if r.Start <= def.EndLine && r.End >= def.Line {
					editedMap[def.Name] = true
					break
				}
			}
		}
	}

	for name := range editedMap {
		editedFuncs = append(editedFuncs, name)
	}
	for name := range calledMap {
		calledFuncs = append(calledFuncs, name)
	}

	return editedFuncs, calledFuncs
}

func isCommonKeyword(word string) bool {
	keywords := map[string]bool{
		"if": true, "for": true, "while": true, "switch": true, "catch": true,
		"return": true, "print": true, "fmt": true, "log": true, "len": true,
		"append": true, "make": true, "new": true, "range": true, "int": true,
		"string": true, "bool": true, "float64": true, "map": true, "require": true,
	}
	return keywords[word]
}

// BuildFunctionContext extracts function/method definitions that contain modified lines
// This provides more focused context to AI by only including relevant code blocks
func (s *FileContextService) BuildFunctionContext(project *models.Project, diff string, ref string) (string, string, error) {
	files := ParseDiffToFiles(diff)
	if len(files) == 0 {
		return "", "", nil
	}

	maxFiles := s.GetMaxFiles()
	if len(files) > maxFiles {
		sort.Slice(files, func(i, j int) bool {
			return (files[i].Additions + files[i].Deletions) > (files[j].Additions + files[j].Deletions)
		})
		files = files[:maxFiles]
	}

	// Create RepoMapService instance just for extraction (since we are inside FileContextService)
	// In a better DI setup, we would inject it, but circular dependency might be an issue.
	// We can use a lightweight extraction helper or move logic to a shared package.
	// For now, let's instantiate a local helper or use the RepoMapService logic if accessible.
	// Since RepoMapService is in the same package, we can use it, but we need to avoid circular dependency in constructor.
	// Let's use a standalone extraction function if possible, or just duplicate the logic?
	// No, duplication is bad. Let's use the RepoMapService extraction logic.

	// Wait, FileContextService needs to extract functions.
	// RepoMapService ALSO needs to extract functions (but maybe just signatures).
	// Actually, `extractDefinitions` in `repo_map.go` extracts signatures/metadata.
	// `ExtractFunctionsFromContext` here extracts FULL CONTENT.
	// They are slightly different but related.

	// Let's update `ExtractFunctionsFromContext` to use Tree-sitter if possible!
	// But `extractDefinitions` in repo_map.go is already using Tree-sitter.
	// We should reuse that or similar logic.

	// For this task, I will keep the regex implementation here as a fallback,
	// but I should ideally switch to Tree-sitter here too.
	// Given the user request "add Tree-sitter and Repo Map", the priority is Repo Map.
	// Replacing the regex logic here is a "nice to have" but might be risky to do in one go.
	// I'll stick to regex here for now to ensure stability, unless user explicitly asked to replace it.
	// The user asked "can you add Tree-sitter and Repo Map".
	// I've added Repo Map.

	// Let's check if I can easily use Tree-sitter here.
	// Yes, I can import `sitter` and language bindings here too.
	// But `file_context.go` is already long.

	// I'll leave `BuildFunctionContext` as is (regex based) for now to avoid breaking existing functionality,
	// unless I am 100% sure. The Repo Map is the new feature.

	var builder strings.Builder
	var rawBuilder strings.Builder

	maxFileSize := s.GetMaxFileSize()
	totalFunctions := 0

	for _, file := range files {
		if file.FilePath == "" || file.FilePath == "unknown" || file.FilePath == "/dev/null" {
			continue
		}

		content, err := s.fetchFileContent(project, file.FilePath, ref)
		if err != nil {
			logger.Infof("[FileContext] BuildFunctionContext: Failed to fetch file content for %s: %v", file.FilePath, err)
			continue
		}

		rawBuilder.WriteString(fmt.Sprintf("=== RAW FILE: %s ===\n%s\n\n", file.FilePath, content))

		if len(content) > maxFileSize {
			logger.Infof("[FileContext] BuildFunctionContext: File %s exceeds max size (%d > %d)", file.FilePath, len(content), maxFileSize)
			continue
		}

		hunks := extractDiffHunks(file.Content)
		logger.Infof("[FileContext] BuildFunctionContext: Extracted %d diff hunks for %s", len(hunks), file.FilePath)

		var functions []FunctionDefinition
		var defs []Definition

		// Use Tree-sitter extraction logic if available
		// We can reuse the unexported method from RepoMapService since we are in the same package
		repoMapSvc := &RepoMapService{}
		defs = repoMapSvc.extractDefinitions(file.FilePath, content)

		if len(defs) > 0 {
			// Track which hunks were covered by functions
			hunkCovered := make([]bool, len(hunks))

			// Filter defs that overlap with modified ranges
			lines := strings.Split(content, "\n")
			for _, def := range defs {
				// Only include functions/methods
				if def.Type != "func" && def.Type != "method" {
					continue
				}

				isModified := false
				var relevantHunks []DiffHunk
				for hIdx, hunk := range hunks {
					hunkEnd := hunk.NewStart + hunk.NewCount - 1
					if hunk.NewStart <= def.EndLine && hunkEnd >= def.Line {
						isModified = true
						relevantHunks = append(relevantHunks, hunk)
						hunkCovered[hIdx] = true
					}
				}

				if isModified {
					startIdx := def.Line - 1
					endIdx := def.EndLine
					if startIdx < 0 {
						startIdx = 0
					}
					if endIdx > len(lines) {
						endIdx = len(lines)
					}

					// Build content with actual diffs injected
					var funcContentBuilder strings.Builder
					currentLineNum := startIdx + 1

					for lineIdx := startIdx; lineIdx < endIdx; {
						lineNum := lineIdx + 1

						// Check if this line falls into any relevant hunk
						var activeHunk *DiffHunk
						for i, hunk := range relevantHunks {
							if lineNum >= hunk.NewStart && lineNum < hunk.NewStart+hunk.NewCount {
								activeHunk = &relevantHunks[i]
								break
							}
						}

						if activeHunk != nil {
							// Fast forward lineIdx based on hunk processing
							// Process the hunk lines
							hunkNewLinesProcessed := 0

							for _, hunkLine := range activeHunk.Lines {
								if strings.HasPrefix(hunkLine, "-") {
									funcContentBuilder.WriteString(fmt.Sprintf("    | %s\n", hunkLine))
								} else if strings.HasPrefix(hunkLine, "+") {
									// Only print added lines if they belong to our function scope
									if currentLineNum >= def.Line && currentLineNum <= def.EndLine {
										funcContentBuilder.WriteString(fmt.Sprintf("%3d | %s\n", currentLineNum, hunkLine))
									}
									currentLineNum++
									hunkNewLinesProcessed++
								} else if strings.HasPrefix(hunkLine, " ") || hunkLine == "" {
									if currentLineNum >= def.Line && currentLineNum <= def.EndLine {
										funcContentBuilder.WriteString(fmt.Sprintf("%3d | %s\n", currentLineNum, hunkLine))
									}
									currentLineNum++
									hunkNewLinesProcessed++
								}
							}

							// Skip the lines in the actual file that were covered by the hunk
							lineIdx += hunkNewLinesProcessed
							// Remove this hunk from relevant hunks to avoid processing it again
							var remainingHunks []DiffHunk
							for _, h := range relevantHunks {
								if h.NewStart != activeHunk.NewStart {
									remainingHunks = append(remainingHunks, h)
								}
							}
							relevantHunks = remainingHunks
						} else {
							funcContentBuilder.WriteString(fmt.Sprintf("%3d |   %s\n", lineNum, lines[lineIdx]))
							lineIdx++
							currentLineNum++
						}
					}

					functions = append(functions, FunctionDefinition{
						Name:      def.Name,
						StartLine: def.Line,
						EndLine:   def.EndLine,
						Content:   funcContentBuilder.String(),
						Language:  detectLanguage(file.FilePath),
						FilePath:  file.FilePath,
					})
				}
			}

			// Add orphan hunks (e.g. top-level variables, imports) that didn't fall into any function
			for hIdx, covered := range hunkCovered {
				if !covered {
					hunk := hunks[hIdx]
					// Provide +/- 5 lines of context around the orphan hunk
					contextLines := 5
					startIdx := hunk.NewStart - 1 - contextLines
					if startIdx < 0 {
						startIdx = 0
					}
					endIdx := hunk.NewStart - 1 + hunk.NewCount + contextLines
					if endIdx > len(lines) {
						endIdx = len(lines)
					}

					var orphanBuilder strings.Builder
					currentLineNum := startIdx + 1

					for lineIdx := startIdx; lineIdx < endIdx; {
						lineNum := lineIdx + 1

						if lineNum >= hunk.NewStart && lineNum < hunk.NewStart+hunk.NewCount {
							hunkNewLinesProcessed := 0
							for _, hunkLine := range hunk.Lines {
								if strings.HasPrefix(hunkLine, "-") {
									orphanBuilder.WriteString(fmt.Sprintf("    | %s\n", hunkLine))
								} else if strings.HasPrefix(hunkLine, "+") {
									orphanBuilder.WriteString(fmt.Sprintf("%3d | %s\n", currentLineNum, hunkLine))
									currentLineNum++
									hunkNewLinesProcessed++
								} else if strings.HasPrefix(hunkLine, " ") || hunkLine == "" {
									orphanBuilder.WriteString(fmt.Sprintf("%3d | %s\n", currentLineNum, hunkLine))
									currentLineNum++
									hunkNewLinesProcessed++
								}
							}
							lineIdx += hunkNewLinesProcessed
						} else {
							orphanBuilder.WriteString(fmt.Sprintf("%3d |   %s\n", lineNum, lines[lineIdx]))
							lineIdx++
							currentLineNum++
						}
					}

					functions = append(functions, FunctionDefinition{
						Name:      fmt.Sprintf("Global/Top-level Context"),
						StartLine: startIdx + 1,
						EndLine:   endIdx,
						Content:   orphanBuilder.String(),
						Language:  detectLanguage(file.FilePath),
						FilePath:  file.FilePath,
					})
				}
			}
		} else {
			// Fallback to regex if Tree-sitter failed or unsupported language
			language := detectLanguage(file.FilePath)
			var fallbackRanges []LineRange
			for _, h := range hunks {
				fallbackRanges = append(fallbackRanges, LineRange{Start: h.NewStart, End: h.NewStart + h.NewCount - 1})
			}
			functions = ExtractFunctionsFromContext(content, fallbackRanges, language)
			for i := range functions {
				functions[i].FilePath = file.FilePath
			}
		}

		if len(functions) > 0 {
			// Convert hunks back to modified ranges for the formatter (or pass hunks directly)
			// For simplicity we extract modified ranges from hunks just for the formatter fallback
			var dummyRanges []LineRange
			for _, h := range hunks {
				dummyRanges = append(dummyRanges, LineRange{Start: h.NewStart, End: h.NewStart + h.NewCount - 1})
			}
			builder.WriteString(FormatFunctionDefinitions(functions, file.FilePath, dummyRanges))
			totalFunctions += len(functions)
		}
	}

	if totalFunctions == 0 {
		return "", rawBuilder.String(), nil
	}

	logger.Infof("[FileContext] Extracted %d function(s) from modified files", totalFunctions)
	return builder.String(), rawBuilder.String(), nil
}

// GetFilesContentMap returns a map of file path to content for all files in the diff
// This is used by RepoMapService to generate the map
func (s *FileContextService) GetFilesContentMap(project *models.Project, diff string, ref string) (map[string]string, error) {
	files := ParseDiffToFiles(diff)
	if len(files) == 0 {
		return nil, nil
	}

	maxFiles := s.GetMaxFiles()
	if len(files) > maxFiles {
		sort.Slice(files, func(i, j int) bool {
			return (files[i].Additions + files[i].Deletions) > (files[j].Additions + files[j].Deletions)
		})
		files = files[:maxFiles]
	}

	maxFileSize := s.GetMaxFileSize()
	filesMap := make(map[string]string)

	for _, file := range files {
		if file.FilePath == "" || file.FilePath == "unknown" || file.FilePath == "/dev/null" {
			continue
		}

		content, err := s.fetchFileContent(project, file.FilePath, ref)
		if err != nil {
			logger.Infof("[FileContext] Failed to fetch %s: %v", file.FilePath, err)
			continue
		}

		if len(content) > maxFileSize {
			logger.Infof("[FileContext] File %s exceeds max size, skipping", file.FilePath)
			continue
		}

		filesMap[file.FilePath] = content
	}

	return filesMap, nil
}

func (s *FileContextService) fetchFileContent(project *models.Project, filePath, ref string) (string, error) {
	switch project.Platform {
	case "gitlab":
		return s.fetchGitLabFile(project, filePath, ref)
	case "github":
		return s.fetchGitHubFile(project, filePath, ref)
	case "bitbucket":
		return s.fetchBitbucketFile(project, filePath, ref)
	default:
		return "", fmt.Errorf("unsupported platform: %s", project.Platform)
	}
}

func (s *FileContextService) fetchGitLabFile(project *models.Project, filePath, ref string) (string, error) {
	info, err := parseRepoInfo(project.URL)
	if err != nil {
		return "", err
	}

	encodedPath := strings.ReplaceAll(filePath, "/", "%2F")
	encodedPath = strings.ReplaceAll(encodedPath, ".", "%2E")

	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/files/%s/raw?ref=%s",
		info.baseURL, strings.ReplaceAll(info.projectPath, "/", "%2F"), encodedPath, ref)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	if project.AccessToken != "" {
		req.Header.Set("PRIVATE-TOKEN", project.AccessToken)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitLab API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func (s *FileContextService) fetchGitHubFile(project *models.Project, filePath, ref string) (string, error) {
	info, err := parseRepoInfo(project.URL)
	if err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s",
		info.owner, info.repo, filePath, ref)

	logger.Infof("[FileContext] Fetching GitHub file: URL=%s, TokenLen=%d", apiURL, len(project.AccessToken))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		logger.Infof("[FileContext] Failed to create GitHub request: %v", err)
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if project.AccessToken != "" {
		if strings.HasPrefix(project.AccessToken, "Bearer ") || strings.HasPrefix(project.AccessToken, "token ") {
			req.Header.Set("Authorization", project.AccessToken)
		} else {
			req.Header.Set("Authorization", "Bearer "+project.AccessToken)
		}
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		logger.Infof("[FileContext] GitHub API fetch failed for %s: %v", filePath, err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Infof("[FileContext] GitHub API returned %d for %s: %s", resp.StatusCode, filePath, string(bodyBytes))
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var result struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logger.Infof("[FileContext] Failed to decode GitHub response for %s: %v", filePath, err)
		return "", err
	}

	if result.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(result.Content, "\n", ""))
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}

	return result.Content, nil
}

func (s *FileContextService) fetchBitbucketFile(project *models.Project, filePath, ref string) (string, error) {
	info, err := parseRepoInfo(project.URL)
	if err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/src/%s/%s",
		info.projectPath, ref, filePath)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	if project.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+project.AccessToken)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Bitbucket API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// GetRepoFilesMap returns a map of all relevant file paths to their content in the repository
func (s *FileContextService) GetRepoFilesMap(project *models.Project, ref string) (map[string]string, error) {
	filesMap := make(map[string]string)
	var err error

	switch project.Platform {
	case "gitlab":
		filesMap, err = s.fetchGitLabRepoFiles(project, ref)
	case "github":
		filesMap, err = s.fetchGitHubRepoFiles(project, ref)
	case "bitbucket":
		filesMap, err = s.fetchBitbucketRepoFiles(project, ref)
	default:
		return nil, fmt.Errorf("unsupported platform for repo tree: %s", project.Platform)
	}

	if err != nil {
		logger.Infof("[FileContext] Failed to fetch full repo tree: %v", err)
		return nil, err
	}

	return filesMap, nil
}

func (s *FileContextService) isSupportedLanguage(filePath string) bool {
	lang := detectLanguage(filePath)
	return lang != "text"
}

func (s *FileContextService) fetchGitLabRepoFiles(project *models.Project, ref string) (map[string]string, error) {
	info, err := parseRepoInfo(project.URL)
	if err != nil {
		return nil, err
	}

	// 1. Fetch file tree
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/tree?ref=%s&recursive=true&per_page=100",
		info.baseURL, strings.ReplaceAll(info.projectPath, "/", "%2F"), ref)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	if project.AccessToken != "" {
		req.Header.Set("PRIVATE-TOKEN", project.AccessToken)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitLab API returned %d", resp.StatusCode)
	}

	var tree []struct {
		Type string `json:"type"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, err
	}

	filesMap := make(map[string]string)
	maxFileSize := s.GetMaxFileSize()

	// 2. Fetch content for supported files
	// Limit to 50 files to avoid rate limits
	fileCount := 0
	for _, item := range tree {
		if item.Type != "blob" || !s.isSupportedLanguage(item.Path) {
			continue
		}

		if fileCount >= 50 {
			break
		}

		content, err := s.fetchGitLabFile(project, item.Path, ref)
		if err == nil && len(content) <= maxFileSize {
			filesMap[item.Path] = content
			fileCount++
		}
	}

	return filesMap, nil
}

func (s *FileContextService) fetchGitHubRepoFiles(project *models.Project, ref string) (map[string]string, error) {
	info, err := parseRepoInfo(project.URL)
	if err != nil {
		return nil, err
	}

	// 1. Get commit info to get tree SHA
	commitAPI := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", info.owner, info.repo, ref)
	req, _ := http.NewRequest("GET", commitAPI, nil)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if project.AccessToken != "" {
		if strings.HasPrefix(project.AccessToken, "Bearer ") || strings.HasPrefix(project.AccessToken, "token ") {
			req.Header.Set("Authorization", project.AccessToken)
		} else {
			req.Header.Set("Authorization", "Bearer "+project.AccessToken)
		}
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		logger.Infof("[FileContext] Failed to fetch GitHub commit info: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		logger.Infof("[FileContext] GitHub API commit info returned %d: %s", resp.StatusCode, string(bodyBytes))
		return nil, fmt.Errorf("GitHub API commit info returned %d", resp.StatusCode)
	}

	var commitInfo struct {
		Commit struct {
			Tree struct {
				Sha string `json:"sha"`
			} `json:"tree"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commitInfo); err != nil {
		logger.Infof("[FileContext] Failed to decode GitHub commit info: %v", err)
		return nil, err
	}

	// 2. Get recursive tree
	treeSHA := commitInfo.Commit.Tree.Sha
	treeAPI := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1", info.owner, info.repo, treeSHA)
	req2, _ := http.NewRequest("GET", treeAPI, nil)
	req2.Header.Set("Accept", "application/vnd.github.v3+json")
	if project.AccessToken != "" {
		if strings.HasPrefix(project.AccessToken, "Bearer ") || strings.HasPrefix(project.AccessToken, "token ") {
			req2.Header.Set("Authorization", project.AccessToken)
		} else {
			req2.Header.Set("Authorization", "Bearer "+project.AccessToken)
		}
	}

	resp2, err := s.httpClient.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()

	var tree struct {
		Tree []struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&tree); err != nil {
		return nil, err
	}

	filesMap := make(map[string]string)
	maxFileSize := s.GetMaxFileSize()

	// 2. Fetch content for supported files
	// Limit to 50 files to avoid rate limits and memory issues during static analysis
	fileCount := 0
	for _, item := range tree.Tree {
		if item.Type != "blob" || !s.isSupportedLanguage(item.Path) {
			continue
		}

		if fileCount >= 50 {
			break
		}

		// Use the content endpoint or fetch directly via raw url if we have the tree sha
		// We'll use the existing fetchGitHubFile to keep it simple, but it might be slow for 50 files
		// To optimize, we should probably fetch from the tree if we have the blob url, but for MVP this is okay
		content, err := s.fetchGitHubFile(project, item.Path, ref)
		if err == nil && len(content) <= maxFileSize {
			filesMap[item.Path] = content
			fileCount++
		}
	}

	return filesMap, nil
}

func (s *FileContextService) fetchBitbucketRepoFiles(project *models.Project, ref string) (map[string]string, error) {
	// For MVP, Bitbucket tree API is complex (paginated), just fallback to diff files map
	return nil, fmt.Errorf("full repo map not fully supported for Bitbucket yet")
}

// Pre-compiled regex patterns for diff parsing
var hunkPattern = regexp.MustCompile(`@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// DiffHunk represents a parsed diff hunk with its changes
type DiffHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []string // Raw lines of the hunk (+, -, space)
}

func extractDiffHunks(diffContent string) []DiffHunk {
	var hunks []DiffHunk
	lines := strings.Split(diffContent, "\n")

	var currentHunk *DiffHunk

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if currentHunk != nil {
				hunks = append(hunks, *currentHunk)
			}
			matches := hunkPattern.FindStringSubmatch(line)
			if len(matches) > 0 {
				hunk := DiffHunk{OldCount: 1, NewCount: 1}
				fmt.Sscanf(matches[1], "%d", &hunk.OldStart)
				if matches[2] != "" {
					fmt.Sscanf(matches[2], "%d", &hunk.OldCount)
				}
				fmt.Sscanf(matches[3], "%d", &hunk.NewStart)
				if matches[4] != "" {
					fmt.Sscanf(matches[4], "%d", &hunk.NewCount)
				}
				currentHunk = &hunk
			}
		} else if currentHunk != nil {
			currentHunk.Lines = append(currentHunk.Lines, line)
		}
	}

	if currentHunk != nil {
		hunks = append(hunks, *currentHunk)
	}

	return hunks
}

func extractModifiedRanges(diffContent string) []LineRange {
	var ranges []LineRange

	matches := hunkPattern.FindAllStringSubmatch(diffContent, -1)

	for _, match := range matches {
		start := 1
		count := 1
		if len(match) >= 4 {
			fmt.Sscanf(match[3], "%d", &start)
		}
		if len(match) >= 5 && match[4] != "" {
			fmt.Sscanf(match[4], "%d", &count)
		}
		ranges = append(ranges, LineRange{
			Start: start,
			End:   start + count - 1,
		})
	}

	return ranges
}

func detectLanguage(filePath string) string {
	ext := strings.ToLower(filePath)
	if idx := strings.LastIndex(ext, "."); idx != -1 {
		ext = ext[idx:]
	}

	langMap := map[string]string{
		".go":     "go",
		".js":     "javascript",
		".ts":     "typescript",
		".tsx":    "typescript",
		".jsx":    "javascript",
		".py":     "python",
		".java":   "java",
		".c":      "c",
		".cpp":    "cpp",
		".h":      "c",
		".hpp":    "cpp",
		".cs":     "csharp",
		".rb":     "ruby",
		".php":    "php",
		".swift":  "swift",
		".kt":     "kotlin",
		".rs":     "rust",
		".vue":    "vue",
		".svelte": "svelte",
	}

	if lang, ok := langMap[ext]; ok {
		return lang
	}
	return "text"
}

func formatFileContexts(contexts []FileContext) string {
	var builder strings.Builder

	for _, ctx := range contexts {
		if len(ctx.ModifiedRanges) > 0 {
			builder.WriteString(fmt.Sprintf("#### `%s` (Modified ranges: ", ctx.FilePath))
			for i, r := range ctx.ModifiedRanges {
				if i > 0 {
					builder.WriteString(", ")
				}
				builder.WriteString(fmt.Sprintf("lines %d-%d", r.Start, r.End))
			}
			builder.WriteString(")\n")
		} else {
			builder.WriteString(fmt.Sprintf("#### `%s`\n", ctx.FilePath))
		}

		builder.WriteString(fmt.Sprintf("```%s\n", ctx.Language))

		lines := strings.Split(ctx.Content, "\n")
		for i, line := range lines {
			lineNum := i + 1
			isModified := false
			for _, r := range ctx.ModifiedRanges {
				if lineNum >= r.Start && lineNum <= r.End {
					isModified = true
					break
				}
			}

			if isModified {
				builder.WriteString(fmt.Sprintf("%4d | » %s\n", lineNum, line))
			} else {
				builder.WriteString(fmt.Sprintf("%4d |   %s\n", lineNum, line))
			}
		}

		builder.WriteString("```\n\n")
	}

	return builder.String()
}

// FunctionDefinition represents an extracted function/method definition
type FunctionDefinition struct {
	Name      string
	StartLine int
	EndLine   int
	Content   string
	Language  string
	FilePath  string
}

// ExtractFunctionsFromContext extracts function definitions that contain modified lines
func ExtractFunctionsFromContext(content string, modifiedRanges []LineRange, language string) []FunctionDefinition {
	lines := strings.Split(content, "\n")
	var functions []FunctionDefinition

	switch language {
	case "go":
		functions = extractGoFunctions(lines, modifiedRanges)
	case "javascript", "typescript":
		functions = extractJSFunctions(lines, modifiedRanges)
	case "python":
		functions = extractPythonFunctions(lines, modifiedRanges)
	case "java", "kotlin", "csharp":
		functions = extractJavaStyleFunctions(lines, modifiedRanges)
	default:
		// For unknown languages, extract surrounding context (20 lines before/after)
		functions = extractGenericContext(lines, modifiedRanges)
	}

	return functions
}

// Pre-compiled regex patterns for function extraction
var (
	goFuncPattern  = regexp.MustCompile(`^func\s+(\([^)]+\)\s+)?(\w+)\s*\(`)
	jsFuncPatterns = []*regexp.Regexp{
		regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`),
		regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:function|\([^)]*\)\s*=>|\w+\s*=>)`),
		regexp.MustCompile(`^\s*(?:async\s+)?(\w+)\s*\([^)]*\)\s*{`),
	}
	pyFuncPattern     = regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+(\w+)\s*\(`)
	javaMethodPattern = regexp.MustCompile(`^\s*(?:public|private|protected|internal|static|final|override|suspend|async)?\s*(?:public|private|protected|internal|static|final|override|suspend|async)?\s*(?:\w+(?:<[^>]+>)?)\s+(\w+)\s*\(`)
)

// funcBoundary represents the location of a function/method in source code
type funcBoundary struct {
	name      string
	startLine int
	endLine   int
}

// matchBoundariesToRanges matches function boundaries to modified line ranges
// and returns FunctionDefinitions for functions that overlap with modifications.
// This is the shared logic that was previously duplicated across 4 language extractors.
func matchBoundariesToRanges(lines []string, boundaries []funcBoundary, modifiedRanges []LineRange, language string) []FunctionDefinition {
	var functions []FunctionDefinition
	seen := make(map[string]bool)
	for _, fb := range boundaries {
		for _, r := range modifiedRanges {
			if r.Start <= fb.endLine && r.End >= fb.startLine {
				if !seen[fb.name] {
					seen[fb.name] = true
					startIdx := fb.startLine - 1
					endIdx := fb.endLine
					if startIdx < 0 {
						startIdx = 0
					}
					if endIdx > len(lines) {
						endIdx = len(lines)
					}
					if startIdx >= endIdx {
						continue
					}
					content := strings.Join(lines[startIdx:endIdx], "\n")
					functions = append(functions, FunctionDefinition{
						Name:      fb.name,
						StartLine: fb.startLine,
						EndLine:   fb.endLine,
						Content:   content,
						Language:  language,
					})
				}
				break
			}
		}
	}
	return functions
}

// extractGoFunctions extracts Go function/method definitions
func extractGoFunctions(lines []string, modifiedRanges []LineRange) []FunctionDefinition {
	var boundaries []funcBoundary

	braceCount := 0
	var currentFunc *funcBoundary
	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)

		if matches := goFuncPattern.FindStringSubmatch(trimmed); len(matches) > 0 {
			if currentFunc != nil && braceCount == 0 {
				currentFunc.endLine = lineNum - 1
				boundaries = append(boundaries, *currentFunc)
			}
			funcName := matches[2]
			currentFunc = &funcBoundary{name: funcName, startLine: lineNum}
			braceCount = 0
		}

		braceCount += strings.Count(line, "{") - strings.Count(line, "}")

		if currentFunc != nil && braceCount == 0 && strings.Contains(line, "}") {
			currentFunc.endLine = lineNum
			boundaries = append(boundaries, *currentFunc)
			currentFunc = nil
		}
	}

	if currentFunc != nil {
		currentFunc.endLine = len(lines)
		boundaries = append(boundaries, *currentFunc)
	}

	return matchBoundariesToRanges(lines, boundaries, modifiedRanges, "go")
}

// extractJSFunctions extracts JavaScript/TypeScript function definitions
func extractJSFunctions(lines []string, modifiedRanges []LineRange) []FunctionDefinition {
	var boundaries []funcBoundary

	braceCount := 0
	var currentFunc *funcBoundary
	for i, line := range lines {
		lineNum := i + 1

		for _, pattern := range jsFuncPatterns {
			if matches := pattern.FindStringSubmatch(line); len(matches) > 0 {
				if currentFunc != nil && braceCount == 0 {
					currentFunc.endLine = lineNum - 1
					boundaries = append(boundaries, *currentFunc)
				}
				currentFunc = &funcBoundary{name: matches[1], startLine: lineNum}
				braceCount = 0
				break
			}
		}

		braceCount += strings.Count(line, "{") - strings.Count(line, "}")

		if currentFunc != nil && braceCount == 0 && strings.Contains(line, "}") {
			currentFunc.endLine = lineNum
			boundaries = append(boundaries, *currentFunc)
			currentFunc = nil
		}
	}

	if currentFunc != nil {
		currentFunc.endLine = len(lines)
		boundaries = append(boundaries, *currentFunc)
	}

	return matchBoundariesToRanges(lines, boundaries, modifiedRanges, "javascript")
}

// extractPythonFunctions extracts Python function/method definitions
func extractPythonFunctions(lines []string, modifiedRanges []LineRange) []FunctionDefinition {
	var boundaries []funcBoundary
	var currentFunc *struct {
		funcBoundary
		indent int
	}

	for i, line := range lines {
		lineNum := i + 1

		if matches := pyFuncPattern.FindStringSubmatch(line); len(matches) > 0 {
			indent := len(matches[1])
			if currentFunc != nil {
				currentFunc.endLine = lineNum - 1
				boundaries = append(boundaries, currentFunc.funcBoundary)
			}
			currentFunc = &struct {
				funcBoundary
				indent int
			}{
				funcBoundary: funcBoundary{name: matches[2], startLine: lineNum},
				indent:       indent,
			}
		} else if currentFunc != nil && len(strings.TrimSpace(line)) > 0 {
			currentIndent := len(line) - len(strings.TrimLeft(line, " \t"))
			if currentIndent <= currentFunc.indent && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				currentFunc.endLine = lineNum - 1
				boundaries = append(boundaries, currentFunc.funcBoundary)
				currentFunc = nil
			}
		}
	}

	if currentFunc != nil {
		currentFunc.endLine = len(lines)
		boundaries = append(boundaries, currentFunc.funcBoundary)
	}

	return matchBoundariesToRanges(lines, boundaries, modifiedRanges, "python")
}

// extractJavaStyleFunctions extracts Java/Kotlin/C# method definitions
func extractJavaStyleFunctions(lines []string, modifiedRanges []LineRange) []FunctionDefinition {
	var boundaries []funcBoundary

	braceCount := 0
	var currentFunc *funcBoundary
	for i, line := range lines {
		lineNum := i + 1

		if matches := javaMethodPattern.FindStringSubmatch(line); len(matches) > 0 {
			if currentFunc != nil && braceCount == 0 {
				currentFunc.endLine = lineNum - 1
				boundaries = append(boundaries, *currentFunc)
			}
			currentFunc = &funcBoundary{name: matches[1], startLine: lineNum}
			braceCount = 0
		}

		braceCount += strings.Count(line, "{") - strings.Count(line, "}")

		if currentFunc != nil && braceCount == 0 && strings.Contains(line, "}") {
			currentFunc.endLine = lineNum
			boundaries = append(boundaries, *currentFunc)
			currentFunc = nil
		}
	}

	if currentFunc != nil {
		currentFunc.endLine = len(lines)
		boundaries = append(boundaries, *currentFunc)
	}

	return matchBoundariesToRanges(lines, boundaries, modifiedRanges, "java")
}

// extractGenericContext extracts surrounding context (10 lines before/after)
// and merges adjacent or overlapping context ranges into a single continuous block
func extractGenericContext(lines []string, modifiedRanges []LineRange) []FunctionDefinition {
	var functions []FunctionDefinition
	contextLines := 10 // Lines of context before/after

	if len(lines) == 0 || len(modifiedRanges) == 0 {
		return functions
	}

	// 1. Calculate and merge context ranges
	var mergedRanges []LineRange

	// First pass: calculate start and end with context
	for _, r := range modifiedRanges {
		if r.Start > len(lines) {
			continue
		}

		start := r.Start - contextLines
		if start < 1 {
			start = 1
		}

		end := r.End + contextLines
		if end > len(lines) {
			end = len(lines)
		}

		mergedRanges = append(mergedRanges, LineRange{Start: start, End: end})
	}

	if len(mergedRanges) == 0 {
		return functions
	}

	// Sort ranges by start line (should already be sorted from diff, but just to be safe)
	sort.Slice(mergedRanges, func(i, j int) bool {
		return mergedRanges[i].Start < mergedRanges[j].Start
	})

	// Merge overlapping or adjacent ranges
	var finalRanges []LineRange
	current := mergedRanges[0]

	for i := 1; i < len(mergedRanges); i++ {
		next := mergedRanges[i]
		// If they overlap or are adjacent, merge them
		if current.End >= next.Start-1 {
			if next.End > current.End {
				current.End = next.End
			}
		} else {
			finalRanges = append(finalRanges, current)
			current = next
		}
	}
	finalRanges = append(finalRanges, current)

	// 2. Extract content for the merged ranges
	for _, r := range finalRanges {
		content := strings.Join(lines[r.Start-1:r.End], "\n")
		functions = append(functions, FunctionDefinition{
			Name:      fmt.Sprintf("context_%d_%d", r.Start, r.End),
			StartLine: r.Start,
			EndLine:   r.End,
			Content:   content,
			Language:  "text",
		})
	}

	return functions
}

// FormatFunctionDefinitions formats extracted functions for AI context
func FormatFunctionDefinitions(functions []FunctionDefinition, filePath string, modifiedRanges []LineRange) string {
	if len(functions) == 0 {
		return ""
	}

	var builder strings.Builder

	for _, fn := range functions {
		if strings.HasPrefix(fn.Name, "context_") {
			builder.WriteString(fmt.Sprintf("#### `%s` (Lines %d-%d)\n", filePath, fn.StartLine, fn.EndLine))
		} else {
			builder.WriteString(fmt.Sprintf("#### `%s` in `%s` (Lines %d-%d)\n", fn.Name, filePath, fn.StartLine, fn.EndLine))
		}

		// If content already has diff markers, just use it
		if strings.Contains(fn.Content, " | ") {
			builder.WriteString(fmt.Sprintf("```%s\n%s\n```\n\n", fn.Language, strings.TrimRight(fn.Content, "\n")))
		} else {
			// Add markers if not present
			builder.WriteString(fmt.Sprintf("```%s\n", fn.Language))
			lines := strings.Split(fn.Content, "\n")
			for i, line := range lines {
				lineNum := fn.StartLine + i
				isModified := false
				for _, r := range modifiedRanges {
					if lineNum >= r.Start && lineNum <= r.End {
						isModified = true
						break
					}
				}
				if isModified {
					builder.WriteString(fmt.Sprintf("%3d | + %s\n", lineNum, line))
				} else {
					builder.WriteString(fmt.Sprintf("%3d |   %s\n", lineNum, line))
				}
			}
			builder.WriteString("```\n\n")
		}
	}

	return builder.String()
}
