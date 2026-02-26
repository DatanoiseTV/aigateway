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

	"golang.org/x/crypto/ssh"
)

type ToolFunc func(args map[string]interface{}) (string, error)

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ToolService struct {
	tools        map[string]ToolFunc
	toolSchemas  map[string]ToolDefinition
	enabledTools map[string]bool
}

func NewToolService(enabledTools []string) *ToolService {
	ts := &ToolService{
		tools:        make(map[string]ToolFunc),
		toolSchemas:  make(map[string]ToolDefinition),
		enabledTools: make(map[string]bool),
	}

	// Mark all tools as enabled by default, then disable the ones not in the list
	ts.registerBuiltInTools()

	// If specific tools are provided, only enable those
	if len(enabledTools) > 0 {
		// First disable all
		for name := range ts.tools {
			ts.enabledTools[name] = false
		}
		// Then enable the specified ones
		for _, name := range enabledTools {
			ts.enabledTools[name] = true
		}
	} else {
		// Enable all by default
		for name := range ts.tools {
			ts.enabledTools[name] = true
		}
	}

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
				"params": map[string]interface{}{"description": "Optional query parameters", "type": "object"},
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
				"body":    map[string]interface{}{"description": "JSON body to send", "type": "object"},
				"headers": map[string]interface{}{"description": "Optional headers", "type": "object"},
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
		"ssh_exec",
		"Executes a command on a remote host via SSH",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"host":        map[string]interface{}{"type": "string", "description": "The SSH host"},
				"port":        map[string]interface{}{"type": "integer", "description": "The SSH port (default 22)"},
				"user":        map[string]interface{}{"type": "string", "description": "The SSH username"},
				"password":    map[string]interface{}{"type": "string", "description": "SSH password (if using password auth)"},
				"private_key": map[string]interface{}{"type": "string", "description": "SSH private key (if using key auth)"},
				"command":     map[string]interface{}{"type": "string", "description": "The command to execute"},
				"timeout":     map[string]interface{}{"type": "integer", "description": "Timeout in seconds (default 30)"},
			},
			"required": []string{"host", "command"},
		},
		func(args map[string]interface{}) (string, error) {
			host, _ := args["host"].(string)
			port := 22
			if p, ok := args["port"].(float64); ok {
				port = int(p)
			}
			user, _ := args["user"].(string)
			password, _ := args["password"].(string)
			privateKey, _ := args["private_key"].(string)
			command, _ := args["command"].(string)
			timeout := 30
			if t, ok := args["timeout"].(float64); ok {
				timeout = int(t)
			}

			if host == "" || command == "" {
				return `{"error": "host and command are required"}`, nil
			}

			var authMethods []ssh.AuthMethod
			if privateKey != "" {
				signer, err := ssh.ParsePrivateKey([]byte(privateKey))
				if err != nil {
					return fmt.Sprintf(`{"error": "failed to parse private key: %v"}`, err), nil
				}
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			}
			if password != "" {
				authMethods = append(authMethods, ssh.Password(password))
			}

			if len(authMethods) == 0 {
				return `{"error": "either password or private_key is required"}`, nil
			}

			config := &ssh.ClientConfig{
				User:            user,
				Auth:            authMethods,
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Timeout:         time.Duration(timeout) * time.Second,
			}

			addr := fmt.Sprintf("%s:%d", host, port)
			client, err := ssh.Dial("tcp", addr, config)
			if err != nil {
				return fmt.Sprintf(`{"error": "SSH connection failed: %v"}`, err), nil
			}
			defer client.Close()

			session, err := client.NewSession()
			if err != nil {
				return fmt.Sprintf(`{"error": "failed to create SSH session: %v"}`, err), nil
			}
			defer session.Close()

			var stdout, stderr bytes.Buffer
			session.Stdout = &stdout
			session.Stderr = &stderr

			err = session.Run(command)
			result := map[string]interface{}{
				"command":   command,
				"exit_code": 0,
				"stdout":    stdout.String(),
				"stderr":    stderr.String(),
			}
			if err != nil {
				if exitErr, ok := err.(*ssh.ExitError); ok {
					result["exit_code"] = exitErr.ExitStatus()
				} else {
					result["error"] = err.Error()
				}
			}

			jsonBytes, _ := json.Marshal(result)
			return string(jsonBytes), nil
		},
	)

	t.RegisterWithSchema(
		"ssh_to_host",
		"Connects to a host via SSH and optionally executes commands",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"host":        map[string]interface{}{"type": "string", "description": "The SSH host"},
				"port":        map[string]interface{}{"type": "integer", "description": "The SSH port (default 22)"},
				"user":        map[string]interface{}{"type": "string", "description": "The SSH username"},
				"password":    map[string]interface{}{"type": "string", "description": "SSH password (if using password auth)"},
				"private_key": map[string]interface{}{"type": "string", "description": "SSH private key (if using key auth)"},
				"command":     map[string]interface{}{"type": "string", "description": "Optional command to execute"},
			},
			"required": []string{"host"},
		},
		func(args map[string]interface{}) (string, error) {
			host, _ := args["host"].(string)
			port := 22
			if p, ok := args["port"].(float64); ok {
				port = int(p)
			}
			user, _ := args["user"].(string)
			password, _ := args["password"].(string)
			privateKey, _ := args["private_key"].(string)
			command, _ := args["command"].(string)

			if host == "" {
				return `{"error": "host is required"}`, nil
			}

			var authMethods []ssh.AuthMethod
			if privateKey != "" {
				signer, err := ssh.ParsePrivateKey([]byte(privateKey))
				if err != nil {
					return fmt.Sprintf(`{"error": "failed to parse private key: %v"}`, err), nil
				}
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			}
			if password != "" {
				authMethods = append(authMethods, ssh.Password(password))
			}

			if len(authMethods) == 0 {
				return `{"error": "either password or private_key is required"}`, nil
			}

			config := &ssh.ClientConfig{
				User:            user,
				Auth:            authMethods,
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Timeout:         10 * time.Second,
			}

			addr := fmt.Sprintf("%s:%d", host, port)
			client, err := ssh.Dial("tcp", addr, config)
			if err != nil {
				return fmt.Sprintf(`{"error": "SSH connection failed: %v"}`, err), nil
			}
			defer client.Close()

			result := map[string]interface{}{
				"status": "connected",
				"host":   host,
				"port":   port,
				"user":   user,
			}

			if command != "" {
				session, err := client.NewSession()
				if err != nil {
					return fmt.Sprintf(`{"error": "failed to create session: %v"}`, err), nil
				}
				defer session.Close()

				var stdout, stderr bytes.Buffer
				session.Stdout = &stdout
				session.Stderr = &stderr

				err = session.Run(command)
				result["command"] = command
				result["stdout"] = stdout.String()
				result["stderr"] = stderr.String()
				if err != nil {
					if exitErr, ok := err.(*ssh.ExitError); ok {
						result["exit_code"] = exitErr.ExitStatus()
					} else {
						result["error"] = err.Error()
					}
				} else {
					result["exit_code"] = 0
				}
			}

			jsonBytes, _ := json.Marshal(result)
			return string(jsonBytes), nil
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
				"invert":      map[string]interface{}{"type": "boolean", "description": "Show lines that DON'T match"},
			},
			"required": []string{"pattern", "text"},
		},
		func(args map[string]interface{}) (string, error) {
			pattern, _ := args["pattern"].(string)
			text, _ := args["text"].(string)
			ignoreCase, _ := args["ignore_case"].(bool)
			invert, _ := args["invert"].(bool)

			if pattern == "" || text == "" {
				return `{"error": "pattern and text are required"}`, nil
			}

			flags := ""
			if ignoreCase {
				flags += "i"
			}
			re, err := regexp.Compile("(? " + flags + ")" + pattern)
			if err != nil {
				return fmt.Sprintf(`{"error": "invalid pattern: %v"}`, err), nil
			}

			lines := strings.Split(text, "\n")
			var matches []string
			for i, line := range lines {
				found := re.MatchString(line)
				if invert {
					found = !found
				}
				if found {
					matches = append(matches, fmt.Sprintf("%d:%s", i+1, line))
				}
			}

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
				"pattern":     map[string]interface{}{"type": "string", "description": "The pattern to match (regex)"},
				"replacement": map[string]interface{}{"type": "string", "description": "The replacement string"},
				"global":      map[string]interface{}{"type": "boolean", "description": "Replace all occurrences (default true)"},
			},
			"required": []string{"text", "pattern", "replacement"},
		},
		func(args map[string]interface{}) (string, error) {
			text, _ := args["text"].(string)
			pattern, _ := args["pattern"].(string)
			replacement, _ := args["replacement"].(string)
			global := true
			if g, ok := args["global"].(bool); ok {
				global = g
			}

			if text == "" || pattern == "" {
				return `{"error": "text, pattern, and replacement are required"}`, nil
			}

			re := regexp.MustCompile(pattern)
			if global {
				text = re.ReplaceAllString(text, replacement)
			} else {
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

			var apiURL string
			if strings.Contains(url, "github.com") {
				re := regexp.MustCompile(`github\.com[/:]([^/]+)/([^/]+)`)
				matches := re.FindStringSubmatch(url)
				if matches == nil {
					return `{"error": "invalid GitHub URL"}`, nil
				}
				apiURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=%d", matches[1], matches[2], count)
			} else if strings.Contains(url, "gitlab.com") {
				re := regexp.MustCompile(`gitlab\.com[/:](([^/]+))/([^/]+)`)
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
	if !t.enabledTools[name] {
		errMsg, _ := json.Marshal(map[string]string{"error": "tool disabled: " + name})
		return string(errMsg), nil
	}

	fn, exists := t.tools[name]
	if !exists {
		errMsg, _ := json.Marshal(map[string]string{"error": "unknown tool: " + name})
		return string(errMsg), nil
	}
	return fn(args)
}

func (t *ToolService) IsEnabled(name string) bool {
	return t.enabledTools[name]
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
	for name, def := range t.toolSchemas {
		if !t.enabledTools[name] {
			continue
		}
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
