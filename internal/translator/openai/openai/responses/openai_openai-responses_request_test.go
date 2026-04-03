package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AcceptsMessagesCompat(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello world"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", inputJSON, false)
	outStr := string(out)

	if got := gjson.Get(outStr, "messages.0.role").String(); got != "system" {
		t.Fatalf("messages.0.role = %q, want system", got)
	}
	if got := gjson.Get(outStr, "messages.0.content").String(); got != "You are a helpful assistant." {
		t.Fatalf("messages.0.content = %q", got)
	}
	if got := gjson.Get(outStr, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user", got)
	}
	if got := gjson.Get(outStr, "messages.1.content").String(); got != "Hello world" {
		t.Fatalf("messages.1.content = %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesBuiltinToolsAndHints(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.4",
		"input": "search latest claude news",
		"tools": [
			{"type": "web_search_preview"}
		],
		"system_hints": ["search"],
		"skills": ["connector:connector_openai_quizgpt_v2"]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", inputJSON, false)
	outStr := string(out)

	if got := gjson.Get(outStr, "tools.0.type").String(); got != "web_search_preview" {
		t.Fatalf("tools.0.type = %q, want web_search_preview", got)
	}
	if got := gjson.Get(outStr, "system_hints.0").String(); got != "search" {
		t.Fatalf("system_hints.0 = %q, want search", got)
	}
	if got := gjson.Get(outStr, "skills.0").String(); got != "connector:connector_openai_quizgpt_v2" {
		t.Fatalf("skills.0 = %q", got)
	}
}
