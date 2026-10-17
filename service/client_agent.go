package service

import "strings"

// clientAgentMaxUserAgentLength bounds the raw User-Agent stored in the log's
// admin-only scope. It matches the audit log's own truncation limit.
const clientAgentMaxUserAgentLength = 512

// knownClientAgents maps lowercased User-Agent substrings to the product name
// shown in usage logs. Order matters: the first match wins, so specific tool
// identifiers must come before generic library names. Generic browsers are
// deliberately absent — their User-Agent says nothing about the calling agent,
// and the raw header stays available to administrators.
var knownClientAgents = []struct {
	match string
	name  string
}{
	{match: "claude-cli", name: "Claude Code"},
	{match: "claude-code", name: "Claude Code"},
	{match: "cursor/", name: "Cursor"},
	{match: "windsurf/", name: "Windsurf"},
	{match: "cline/", name: "Cline"},
	{match: "roo-code", name: "Roo Code"},
	{match: "roocode", name: "Roo Code"},
	{match: "kilo-code", name: "Kilo Code"},
	{match: "kilocode", name: "Kilo Code"},
	{match: "continue.dev", name: "Continue"},
	{match: "continue/", name: "Continue"},
	{match: "aider/", name: "Aider"},
	{match: "zed/", name: "Zed"},
	{match: "opencode/", name: "OpenCode"},
	{match: "gemini-cli", name: "Gemini CLI"},
	{match: "codex_cli_rs", name: "Codex CLI"},
	{match: "codex/", name: "Codex CLI"},
	{match: "qoder", name: "Qoder"},
	{match: "trae/", name: "Trae"},
	{match: "zcode", name: "ZCode"},
	{match: "vscode", name: "VS Code"},
	{match: "visual studio code", name: "VS Code"},
	{match: "jetbrains", name: "JetBrains IDE"},
	{match: "openai-python", name: "OpenAI Python SDK"},
	{match: "openai/python", name: "OpenAI Python SDK"},
	{match: "openai-node", name: "OpenAI Node SDK"},
	{match: "openai/node", name: "OpenAI Node SDK"},
	{match: "openai-dotnet", name: "OpenAI .NET SDK"},
	{match: "openai-java", name: "OpenAI Java SDK"},
	{match: "openai-go", name: "OpenAI Go SDK"},
	{match: "openai/", name: "OpenAI SDK"},
	{match: "anthropic-sdk-python", name: "Anthropic Python SDK"},
	{match: "anthropic/python", name: "Anthropic Python SDK"},
	{match: "anthropic/", name: "Anthropic SDK"},
	{match: "google-genai", name: "Google GenAI SDK"},
	{match: "google-generativeai", name: "Google Generative AI SDK"},
	{match: "langchain", name: "LangChain"},
	{match: "llamaindex", name: "LlamaIndex"},
	{match: "ai-sdk", name: "Vercel AI SDK"},
	{match: "dify", name: "Dify"},
	{match: "lobe-chat", name: "LobeChat"},
	{match: "lobechat", name: "LobeChat"},
	{match: "nextchat", name: "NextChat"},
	{match: "chatgpt-next-web", name: "NextChat"},
	{match: "open-webui", name: "Open WebUI"},
	{match: "librechat", name: "LibreChat"},
	{match: "chatbox", name: "Chatbox"},
	{match: "cherry studio", name: "Cherry Studio"},
	{match: "cherrystudio", name: "Cherry Studio"},
	{match: "sillytavern", name: "SillyTavern"},
	{match: "postmanruntime", name: "Postman"},
	{match: "insomnia", name: "Insomnia"},
	{match: "apifox", name: "Apifox"},
	{match: "httpie", name: "HTTPie"},
	{match: "curl/", name: "curl"},
	{match: "wget/", name: "Wget"},
	{match: "python-requests", name: "Python requests"},
	{match: "httpx/", name: "HTTPX"},
	{match: "aiohttp/", name: "AIOHTTP"},
	{match: "axios/", name: "Axios"},
	{match: "node-fetch", name: "node-fetch"},
	{match: "undici", name: "Undici"},
	{match: "go-http-client", name: "Go HTTP Client"},
	{match: "okhttp/", name: "OkHttp"},
}

// DetectClientAgent normalizes a request User-Agent into a readable product
// name and the version published next to it. Both values are empty when the
// header is missing or no known agent matches.
func DetectClientAgent(userAgent string) (name string, version string) {
	trimmed := strings.TrimSpace(userAgent)
	if trimmed == "" {
		return "", ""
	}
	lower := strings.ToLower(trimmed)
	for _, agent := range knownClientAgents {
		index := strings.Index(lower, agent.match)
		if index < 0 {
			continue
		}
		rest := trimmed[index+len(agent.match):]
		// A trailing slash belongs to the identifier itself ("cursor/"), so the
		// version follows immediately; otherwise the text continues with the
		// product's own name ("Anthropic/Python 0.45.0").
		if strings.HasSuffix(agent.match, "/") {
			rest = strings.TrimLeft(rest, " ")
		} else {
			rest = strings.TrimPrefix(strings.TrimLeft(rest, " "), "/")
		}
		return agent.name, clientAgentVersion(rest)
	}
	return "", ""
}

// clientAgentVersion returns the first version-shaped token in the remainder,
// so "Anthropic/Python 0.45.0" still reports 0.45.0. Tokens that merely start
// with a digit but carry other characters are ignored.
func clientAgentVersion(rest string) string {
	for token := range strings.FieldsSeq(rest) {
		token = strings.Trim(token, "(),;:/")
		if token == "" {
			continue
		}
		valid := true
		for _, char := range token {
			if (char < '0' || char > '9') && char != '.' {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		version := strings.Trim(token, ".")
		if version != "" && strings.ContainsAny(version, "0123456789") {
			return version
		}
	}
	return ""
}

// ClientAgentLabel renders the display label stored in usage logs. It keeps the
// product name alone when no version was published.
func ClientAgentLabel(userAgent string) string {
	name, version := DetectClientAgent(userAgent)
	if name == "" {
		return ""
	}
	if version == "" {
		return name
	}
	return name + " " + version
}
