package services

import (
	"encoding/json"
	"time"
)

type ToolFunc func(args map[string]interface{}) (string, error)

type ToolService struct {
	tools map[string]ToolFunc
}

func NewToolService() *ToolService {
	ts := &ToolService{
		tools: make(map[string]ToolFunc),
	}
	ts.registerBuiltInTools()
	return ts
}

func (t *ToolService) registerBuiltInTools() {
	t.Register("get_time", func(args map[string]interface{}) (string, error) {
		return time.Now().Format("15:04:05"), nil
	})

	t.Register("get_date", func(args map[string]interface{}) (string, error) {
		return time.Now().Format("2006-01-02"), nil
	})

	t.Register("echo", func(args map[string]interface{}) (string, error) {
		data, _ := json.Marshal(args)
		return string(data), nil
	})
}

func (t *ToolService) Register(name string, fn ToolFunc) {
	t.tools[name] = fn
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
