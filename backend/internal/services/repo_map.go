package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/huangang/codesentry/backend/internal/models"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// RepoMapService handles generation of repository structure maps using Tree-sitter
type RepoMapService struct {
	fileContextService *FileContextService
	cache              sync.Map // Simple in-memory cache for repo maps
}

func NewRepoMapService(fileContextService *FileContextService) *RepoMapService {
	return &RepoMapService{
		fileContextService: fileContextService,
	}
}

// Definition represents a code definition (function, class, variable, etc.)
type Definition struct {
	Type      string // function, class, variable, etc.
	Name      string
	Signature string // Full signature if available
	Line      int
	EndLine   int
	FilePath  string
	Parent    string // Name of the parent class/struct if applicable
}

// RepoMap represents the structure of a repository
type RepoMap struct {
	Definitions []Definition
	GeneratedAt time.Time
}

type CachedRepoData struct {
	RepoMap  string
	FilesMap map[string]string
}

// GetRepoMap generates or retrieves a cached repo map for the project
func (s *RepoMapService) GetRepoMap(ctx context.Context, project *models.Project, ref string) (string, map[string]string, error) {
	// Check cache first (key: projectID + ref)
	cacheKey := fmt.Sprintf("%d:%s", project.ID, ref)
	if cached, ok := s.cache.Load(cacheKey); ok {
		data := cached.(CachedRepoData)
		return data.RepoMap, data.FilesMap, nil
	}

	// Fetch full repo tree files
	filesMap, err := s.fileContextService.GetRepoFilesMap(project, ref)
	if err != nil {
		return "", nil, err
	}

	repoMapStr := s.GenerateMapForFiles(filesMap)

	// Cache it
	s.cache.Store(cacheKey, CachedRepoData{
		RepoMap:  repoMapStr,
		FilesMap: filesMap,
	})

	return repoMapStr, filesMap, nil
}

// FindCallers scans the global files map and extracts AST functions/methods that call the target functions.
// It returns two strings: callersContext and calleeContext
func (s *RepoMapService) FindCallers(filesMap map[string]string, editedFuncs []string, calledFuncs []string) (string, string) {
	if (len(editedFuncs) == 0 && len(calledFuncs) == 0) || len(filesMap) == 0 {
		return "", ""
	}

	var callersBuilder strings.Builder
	var calleeBuilder strings.Builder

	callersFoundAny := false
	calleeFoundAny := false
	maxCallersToExtract := 10
	callersFound := 0

	// Create lookup maps for faster checking
	editedMap := make(map[string]bool)
	for _, f := range editedFuncs {
		editedMap[f] = true
	}
	calledMap := make(map[string]bool)
	for _, f := range calledFuncs {
		calledMap[f] = true
	}

	// Pre-process all definitions to find the definitions of the target functions themselves (Callee Context)
	for path, content := range filesMap {
		defs := s.extractDefinitions(path, content)
		lines := strings.Split(content, "\n")

		for _, def := range defs {
			// Find definitions of the CALLED functions (Callee Context)
			// Exclude functions that are already in the editedMap (because they are in File Context)
			isTargetDef := false
			if (def.Type == "func" || def.Type == "method") && calledMap[def.Name] && !editedMap[def.Name] {
				isTargetDef = true
			}

			if isTargetDef {
				startIdx := def.Line - 1
				endIdx := def.EndLine
				if startIdx >= 0 && endIdx <= len(lines) && startIdx < endIdx {
					body := strings.Join(lines[startIdx:endIdx], "\n")
					calleeBuilder.WriteString(fmt.Sprintf("#### Definition: `%s` in `%s`\n", def.Signature, path))
					calleeBuilder.WriteString("```" + detectLanguage(path) + "\n")
					calleeBuilder.WriteString(body + "\n")
					calleeBuilder.WriteString("```\n\n")
					calleeFoundAny = true
				}
			}

			// Find CALLERS of the EDITED functions
			if callersFound >= maxCallersToExtract {
				continue
			}

			if def.Type != "func" && def.Type != "method" {
				continue
			}

			// Skip self-referential
			if editedMap[def.Name] {
				continue
			}

			startIdx := def.Line - 1
			endIdx := def.EndLine
			if startIdx < 0 || endIdx > len(lines) || startIdx >= endIdx {
				continue
			}

			body := strings.Join(lines[startIdx:endIdx], "\n")

			callsTarget := false
			for tf := range editedMap {
				if len(tf) < 2 {
					continue
				}
				if strings.Contains(body, tf+"(") || strings.Contains(body, tf+" (") || strings.Contains(body, "."+tf+"(") || strings.Contains(body, "."+tf+" (") {
					callsTarget = true
					break
				}
			}

			if callsTarget {
				callersBuilder.WriteString(fmt.Sprintf("#### Caller: `%s` in `%s`\n", def.Signature, path))
				callersBuilder.WriteString("```" + detectLanguage(path) + "\n")
				callersBuilder.WriteString(body + "\n")
				callersBuilder.WriteString("```\n\n")
				callersFoundAny = true
				callersFound++
			}
		}
	}

	callersCtx := ""
	if callersFoundAny {
		callersCtx = callersBuilder.String()
	}

	calleeCtx := ""
	if calleeFoundAny {
		calleeCtx = calleeBuilder.String()
	}

	return callersCtx, calleeCtx
}

// GenerateMapForFiles generates a map for a specific list of files content
func (s *RepoMapService) GenerateMapForFiles(files map[string]string) string {
	var allDefs []Definition

	for path, content := range files {
		defs := s.extractDefinitions(path, content)
		allDefs = append(allDefs, defs...)
	}

	// Sort definitions by file path and line number
	sort.Slice(allDefs, func(i, j int) bool {
		if allDefs[i].FilePath != allDefs[j].FilePath {
			return allDefs[i].FilePath < allDefs[j].FilePath
		}
		return allDefs[i].Line < allDefs[j].Line
	})

	// Build parent-child relationships
	var hierarchicalDefs []Definition
	var currentParents []*Definition

	for i := range allDefs {
		def := &allDefs[i]

		// Remove parents that ended before this definition starts
		var activeParents []*Definition
		for _, p := range currentParents {
			if def.Line <= p.EndLine && p.FilePath == def.FilePath {
				activeParents = append(activeParents, p)
			}
		}
		currentParents = activeParents

		// Assign parent if exists
		if len(currentParents) > 0 {
			def.Parent = currentParents[len(currentParents)-1].Name
		}

		// If this is a container type (class, interface, struct), add it to parents stack
		if def.Type == "class" || def.Type == "interface" || def.Type == "struct" || def.Type == "type" {
			currentParents = append(currentParents, def)
		}

		hierarchicalDefs = append(hierarchicalDefs, *def)
	}

	return s.formatRepoMap(hierarchicalDefs)
}

// extractDefinitions uses Tree-sitter to parse content and extract definitions
func (s *RepoMapService) extractDefinitions(path string, content string) []Definition {
	lang := detectLanguage(path)
	var parser *sitter.Parser
	var query *sitter.Query

	switch lang {
	case "go":
		parser = sitter.NewParser()
		parser.SetLanguage(golang.GetLanguage())
		// Query for Go: functions, methods, types, variables, parameters, return types
		q, _ := sitter.NewQuery([]byte(`
			(function_declaration 
				name: (identifier) @name 
				parameters: (parameter_list) @params 
				result: (_)? @return) @func
			(method_declaration 
				name: (field_identifier) @name 
				parameters: (parameter_list) @params 
				result: (_)? @return) @method
			(type_declaration (type_spec name: (type_identifier) @name)) @type
			(interface_type) @interface
		`), golang.GetLanguage())
		query = q
	case "python":
		parser = sitter.NewParser()
		parser.SetLanguage(python.GetLanguage())
		q, _ := sitter.NewQuery([]byte(`
			(function_definition 
				name: (identifier) @name 
				parameters: (parameters) @params 
				return_type: (_)? @return) @func
			(class_definition name: (identifier) @name) @class
		`), python.GetLanguage())
		query = q
	case "javascript", "typescript":
		parser = sitter.NewParser()
		if lang == "typescript" {
			parser.SetLanguage(typescript.GetLanguage())
			q, _ := sitter.NewQuery([]byte(`
				(function_declaration 
					name: (identifier) @name 
					parameters: (formal_parameters) @params 
					return_type: (type_annotation)? @return) @func
				(class_declaration name: (type_identifier) @name) @class
				(interface_declaration name: (type_identifier) @name) @interface
				(method_definition 
					name: (property_identifier) @name 
					parameters: (formal_parameters) @params 
					return_type: (type_annotation)? @return) @method
			`), typescript.GetLanguage())
			query = q
		} else {
			parser.SetLanguage(javascript.GetLanguage())
			q, _ := sitter.NewQuery([]byte(`
				(function_declaration 
					name: (identifier) @name 
					parameters: (formal_parameters) @params) @func
				(class_declaration name: (identifier) @name) @class
				(method_definition 
					name: (property_identifier) @name 
					parameters: (formal_parameters) @params) @method
			`), javascript.GetLanguage())
			query = q
		}
	case "java":
		parser = sitter.NewParser()
		parser.SetLanguage(java.GetLanguage())
		q, _ := sitter.NewQuery([]byte(`
			(class_declaration name: (identifier) @name) @class
			(method_declaration 
				name: (identifier) @name 
				parameters: (formal_parameters) @params 
				type: (_)? @return) @method
			(interface_declaration name: (identifier) @name) @interface
		`), java.GetLanguage())
		query = q
	case "cpp", "c":
		parser = sitter.NewParser()
		parser.SetLanguage(cpp.GetLanguage())
		q, _ := sitter.NewQuery([]byte(`
			(function_definition declarator: (function_declarator declarator: (identifier) @name)) @func
			(class_specifier name: (type_identifier) @name) @class
			(struct_specifier name: (type_identifier) @name) @struct
		`), cpp.GetLanguage())
		query = q
	case "csharp":
		parser = sitter.NewParser()
		parser.SetLanguage(csharp.GetLanguage())
		q, _ := sitter.NewQuery([]byte(`
			(class_declaration name: (identifier) @name) @class
			(interface_declaration name: (identifier) @name) @interface
			(struct_declaration name: (identifier) @name) @struct
			(method_declaration name: (identifier) @name) @method
			(constructor_declaration name: (identifier) @name) @constructor
		`), csharp.GetLanguage())
		query = q
	default:
		return nil // Unsupported language
	}

	if parser == nil || query == nil {
		return nil
	}

	tree, _ := parser.ParseCtx(context.Background(), nil, []byte(content))
	root := tree.RootNode()

	qc := sitter.NewQueryCursor()
	qc.Exec(query, root)

	var defs []Definition

	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}

		// We expect the query to capture @name and the whole node (@func, @class, etc)
		var def Definition
		def.FilePath = path

		var params, returnType string

		for _, c := range m.Captures {
			node := c.Node
			name := query.CaptureNameForId(c.Index)

			if name == "name" {
				def.Name = node.Content([]byte(content))
			} else if name == "params" {
				params = node.Content([]byte(content))
			} else if name == "return" {
				returnType = node.Content([]byte(content))
			} else {
				def.Type = name // func, class, method, etc.
				def.Line = int(node.StartPoint().Row) + 1
				def.EndLine = int(node.EndPoint().Row) + 1

				// Extract signature (first line usually)
				lines := strings.Split(content, "\n")
				if def.Line > 0 && def.Line <= len(lines) {
					def.Signature = strings.TrimSpace(lines[def.Line-1])
				}
			}
		}

		// Ensure we don't have empty name or type
		if def.Name == "" || def.Type == "" {
			continue
		}

		// Enhance signature with parsed parameters and return types
		if params != "" {
			def.Signature = fmt.Sprintf("%s %s%s", def.Type, def.Name, params)
			if returnType != "" {
				def.Signature += fmt.Sprintf(" -> %s", returnType)
			}
		} else if def.Type == "class" || def.Type == "interface" || def.Type == "type" {
			def.Signature = fmt.Sprintf("%s %s", def.Type, def.Name)
		}

		defs = append(defs, def)
	}

	return defs
}

func (s *RepoMapService) formatRepoMap(defs []Definition) string {
	if len(defs) == 0 {
		return ""
	}

	var builder strings.Builder

	currentFile := ""
	for _, def := range defs {
		if def.FilePath != currentFile {
			if currentFile != "" {
				builder.WriteString("\n")
			}
			builder.WriteString(fmt.Sprintf("#### `%s`\n", def.FilePath))
			currentFile = def.FilePath
		}

		// Use the enhanced signature
		sig := def.Signature
		if sig == "" {
			sig = fmt.Sprintf("%s %s", def.Type, def.Name)
		}

		// Add indentation for nested items (methods inside classes)
		indent := ""
		if def.Parent != "" && def.Type != "class" && def.Type != "struct" && def.Type != "interface" {
			indent = "  "
		}

		builder.WriteString(fmt.Sprintf("%s- `%s` (L%d)\n", indent, sig, def.Line))
	}

	return builder.String()
}
