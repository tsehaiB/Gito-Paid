// main.go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ian-kent/gptchat/module"
	"github.com/ian-kent/gptchat/module/memory"
	"github.com/ian-kent/gptchat/module/plugin"
	"github.com/ian-kent/gptchat/ui"
	openai "github.com/sashabaranov/go-openai"
)

func init() {
	// Directly assign the API key
	openaiAPIKey := "sk-proj-PK9VxsilcXQbNaG542njT3BlbkFJRmMNidriFDnlnyC61lAg"

	cfg = cfg.WithOpenAIAPIKey(openaiAPIKey)

	openaiAPIModel := strings.TrimSpace(os.Getenv("OPENAI_API_MODEL"))

	if openaiAPIModel == "" {
		ui.Warn("You haven't configured an OpenAI API model, defaulting to GPT4")
		openaiAPIModel = openai.GPT4
	}

	cfg = cfg.WithOpenAIAPIModel(openaiAPIModel)

	supervisorMode := os.Getenv("GPTCHAT_SUPERVISOR")
	switch strings.ToLower(supervisorMode) {
	case "disabled":
		ui.Warn("Supervisor mode is disabled")
		cfg = cfg.WithSupervisedMode(false)
	default:
	}

	debugEnv := os.Getenv("GPTCHAT_DEBUG")
	if debugEnv != "" {
		v, err := strconv.ParseBool(debugEnv)
		if err != nil {
			ui.Warn(fmt.Sprintf("error parsing GPT_DEBUG: %s", err.Error()))
		} else {
			cfg = cfg.WithDebugMode(v)
		}
	}

	client = openai.NewClient(openaiAPIKey)

	module.Load(cfg, client, []module.Module{
		&memory.Module{},
		&plugin.Module{},
	}...)

	if err := module.LoadCompiledPlugins(); err != nil {
		ui.Warn(fmt.Sprintf("error loading compiled plugins: %s", err))
	}
}

func main() {
	initConversation()
	startServer()
}
