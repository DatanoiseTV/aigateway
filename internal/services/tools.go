package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ToolFunc func(args map[string]interface{}) (string, error)

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ToolService struct {
	tools       map[string]ToolFunc
	toolSchemas map[string]ToolDefinition
}

func NewToolService() *ToolService {
	ts := &ToolService{
		tools:       make(map[string]ToolFunc),
		toolSchemas: make(map[string]ToolDefinition),
	}
	ts.registerBuiltInTools()
	return ts
}

func (t *ToolService) registerBuiltInTools() {
	t.RegisterWithSchema(
		"get_time",
		"Returns the current time in HH:MM:SS format",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
			"required":   []string{},
		},
		func(args map[string]interface{}) (string, error) {
			return time.Now().Format("15:04:05"), nil
		},
	)

	t.RegisterWithSchema(
		"get_date",
		"Returns the current date in YYYY-MM-DD format",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
			"required":   []string{},
		},
		func(args map[string]interface{}) (string, error) {
			return time.Now().Format("2006-01-02"), nil
		},
	)

	t.RegisterWithSchema(
		"http_get",
		"Performs an HTTP GET request to the specified URL",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url":    map[string]interface{}{"type": "string", "description": "The URL to fetch"},
				"params": map[string]interface{}{"type": "object", "description": "Optional query parameters"},
			},
			"required": []string{"url"},
		},
		func(args map[string]interface{}) (string, error) {
			url, _ := args["url"].(string)
			if url == "" {
				return `{"error": "url is required"}`, nil
			}

			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return fmt.Sprintf(`{"error": "%v"}`, err), nil
			}

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Sprintf(`{"error": "%v"}`, err), nil
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			return string(body), nil
		},
	)

	t.RegisterWithSchema(
		"send_http_request_json",
		"Sends an HTTP request with JSON body and returns JSON response",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url":     map[string]interface{}{"type": "string", "description": "The URL to request"},
				"method":  map[string]interface{}{"type": "string", "description": "HTTP method (GET, POST, PUT, DELETE)", "enum": []string{"GET", "POST", "PUT", "DELETE"}},
				"body":    map[string]interface{}{"type": "object", "description": "JSON body to send"},
				"headers": map[string]interface{}{"type": "object", "description": "Optional headers"},
			},
			"required": []string{"url", "method"},
		},
		func(args map[string]interface{}) (string, error) {
			url, _ := args["url"].(string)
			method, _ := args["method"].(string)
			if url == "" || method == "" {
				return `{"error": "url and method are required"}`, nil
			}

			var body io.Reader = nil
			if args["body"] != nil {
				bodyBytes, _ := json.Marshal(args["body"])
				body = bytes.NewReader(bodyBytes)
			}

			req, err := http.NewRequest(method, url, body)
			if err != nil {
				return fmt.Sprintf(`{"error": "%v"}`, err), nil
			}
			req.Header.Set("Content-Type", "application/json")
			if headers, ok := args["headers"].(map[string]interface{}); ok {
				for k, v := range headers {
					req.Header.Set(k, fmt.Sprintf("%v", v))
				}
			}

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Sprintf(`{"error": "%v"}`, err), nil
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(resp.Body)
			return string(respBody), nil
		},
	)

	t.RegisterWithSchema(
		"tcp_connect",
		"Tests TCP connectivity to a host:port",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"host": map[string]interface{}{"type": "string", "description": "The hostname or IP"},
				"port": map[string]interface{}{"type": "integer", "description": "The port number"},
			},
			"required": []string{"host", "port"},
		},
		func(args map[string]interface{}) (string, error) {
			host, _ := args["host"].(string)
			port, _ := args["port"].(float64)
			if host == "" || port == 0 {
				return `{"error": "host and port are required"}`, nil
			}

			addr := fmt.Sprintf("%s:%d", host, int(port))
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				return fmt.Sprintf(`{"error": "connection failed: %v"}`, err), nil
			}
			conn.Close()
			return `{"status": "connected"}`, nil
		},
	)

	t.RegisterWithSchema(
		"udp_connect",
		"Tests UDP connectivity to a host:port",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"host": map[string]interface{}{"type": "string", "description": "The hostname or IP"},
				"port": map[string]interface{}{"type": "integer", "description": "The port number"},
			},
			"required": []string{"host", "port"},
		},
		func(args map[string]interface{}) (string, error) {
			host, _ := args["host"].(string)
			port, _ := args["port"].(float64)
			if host == "" || port == 0 {
				return `{"error": "host and port are required"}`, nil
			}

			addr := fmt.Sprintf("%s:%d", host, int(port))
			conn, err := net.DialTimeout("udp", addr, 5*time.Second)
			if err != nil {
				return fmt.Sprintf(`{"error": "connection failed: %v"}`, err), nil
			}
			conn.Close()
			return `{"status": "connected"}`, nil
		},
	)

	t.RegisterWithSchema(
		"ripgrep_style_grep",
		"Searches for a pattern in text (like ripgrep)",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern":     map[string]interface{}{"type": "string", "description": "The regex pattern to search for"},
				"text":        map[string]interface{}{"type": "string", "description": "The text to search in"},
				"ignore_case": map[string]interface{}{"type": "boolean", "description": "Case insensitive search"},
			},
			"required": []string{"pattern", "text"},
		},
		func(args map[string]interface{}) (string, error) {
			pattern, _ := args["pattern"].(string)
			text, _ := args["text"].(string)
			ignoreCase, _ := args["ignore_case"].(bool)

			if pattern == "" || text == "" {
				return `{"error": "pattern and text are required"}`, nil
			}

			flags := "i"
			if !ignoreCase {
				flags = ""
			}
			re, err := regexp.Compile("(? " + flags + ")" + pattern)
			if err != nil {
				return fmt.Sprintf(`{"error": "invalid pattern: %v"}`, err), nil
			}

			matches := re.FindAllString(text, -1)
			result := map[string]interface{}{
				"matches": matches,
				"count":   len(matches),
			}
			jsonBytes, _ := json.Marshal(result)
			return string(jsonBytes), nil
		},
	)

	t.RegisterWithSchema(
		"sed",
		"Performs sed-like string replacement",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"text":        map[string]interface{}{"type": "string", "description": "The text to process"},
				"pattern":     map[string]interface{}{"type": "string", "description": "The pattern to match"},
				"replacement": map[string]interface{}{"type": "string", "description": "The replacement string"},
				"global":      map[string]interface{}{"type": "boolean", "description": "Replace all occurrences"},
			},
			"required": []string{"text", "pattern", "replacement"},
		},
		func(args map[string]interface{}) (string, error) {
			text, _ := args["text"].(string)
			pattern, _ := args["pattern"].(string)
			replacement, _ := args["replacement"].(string)
			global, _ := args["global"].(bool)

			if text == "" || pattern == "" {
				return `{"error": "text, pattern, and replacement are required"}`, nil
			}

			re := regexp.MustCompile(pattern)
			if global {
				text = re.ReplaceAllString(text, replacement)
			} else {
				text = re.ReplaceAllString(text, replacement)
				text = re.ReplaceAllString(text, replacement)
			}
			return text, nil
		},
	)

	t.RegisterWithSchema(
		"get_last_commits_from_url",
		"Fetches recent git commits from a GitHub or GitLab URL",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url":   map[string]interface{}{"type": "string", "description": "GitHub or GitLab repository URL"},
				"count": map[string]interface{}{"type": "integer", "description": "Number of commits to fetch (default 5)"},
			},
			"required": []string{"url"},
		},
		func(args map[string]interface{}) (string, error) {
			url, _ := args["url"].(string)
			count := 5
			if c, ok := args["count"].(float64); ok {
				count = int(c)
			}

			if url == "" {
				return `{"error": "url is required"}`, nil
			}

			// Convert repo URL to API URL
			var apiURL string
			if strings.Contains(url, "github.com") {
				re := regexp.MustCompile(`github\.com[/:]([^/]+)/([^/]+)`)
				matches := re.FindStringSubmatch(url)
				if matches == nil {
					return `{"error": "invalid GitHub URL"}`, nil
				}
				apiURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=%d", matches[1], matches[2], count)
			} else if strings.Contains(url, "gitlab.com") {
				re := regexp.MustCompile(`gitlab\.com[/:]([^/]+)/([^/]+)`)
				matches := re.FindStringSubmatch(url)
				if matches == nil {
					return `{"error": "invalid GitLab URL"}`, nil
				}
				apiURL = fmt.Sprintf("https://gitlab.com/api/v4/projects/%s%%2F%s/repository/commits?per_page=%d", matches[1], matches[2], count)
			} else {
				return `{"error": "only GitHub and GitLab URLs supported"}`, nil
			}

			req, _ := http.NewRequest("GET", apiURL, nil)
			req.Header.Set("User-Agent", "ai-gateway")
			client := &http.Client{Timeout: 15 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Sprintf(`{"error": "%v"}`, err), nil
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			return string(body), nil
		},
	)

	t.RegisterWithSchema(
		"ssh_to_host",
		"Tests SSH connectivity to a host (just tests connection, does not execute commands)",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"host": map[string]interface{}{"type": "string", "description": "The SSH host"},
				"port": map[string]interface{}{"type": "integer", "description": "The SSH port (default 22)"},
				"user": map[string]interface{}{"type": "string", "description": "The SSH username"},
			},
			"required": []string{"host"},
		},
		func(args map[string]interface{}) (string, error) {
			host, _ := args["host"].(string)
			port := 22
			if p, ok := args["port"].(float64); ok {
				port = int(p)
			}

			if host == "" {
				return `{"error": "host is required"}`, nil
			}

			addr := fmt.Sprintf("%s:%d", host, port)
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				return fmt.Sprintf(`{"error": "SSH connection failed: %v"}`, err), nil
			}
			conn.Close()
			return `{"status": "SSH port reachable"}`, nil
		},
	)

	t.Register("echo", func(args map[string]interface{}) (string, error) {
		data, _ := json.Marshal(args)
		return string(data), nil
	})
}

func (t *ToolService) Register(name string, fn ToolFunc) {
	t.tools[name] = fn
}

func (t *ToolService) RegisterWithSchema(name, description string, parameters any, fn ToolFunc) {
	t.tools[name] = fn
	t.toolSchemas[name] = ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  parameters,
	}
}

func (t *ToolService) Execute(name string, args map[string]interface{}) (string, error) {
	fn, exists := t.tools[name]
	if !exists {
		errMsg, _ := json.Marshal(map[string]string{"error": "unknown tool: " + name})
		return string(errMsg), nil
	}
	return fn(args)
}

func (t *ToolService) ToolNames() []string {
	names := make([]string, 0, len(t.tools))
	for name := range t.tools {
		names = append(names, name)
	}
	return names
}

func (t *ToolService) GetToolDefinitions() []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(t.toolSchemas))
	for _, def := range t.toolSchemas {
		defs = append(defs, def)
	}
	return defs
}

func (t *ToolService) GetOpenAITools() []map[string]interface{} {
	tools := make([]map[string]interface{}, 0)
	for _, def := range t.toolSchemas {
		tools = append(tools, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        def.Name,
				"description": def.Description,
				"parameters":  def.Parameters,
			},
		})
	}
	return tools
}
