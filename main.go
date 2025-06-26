// main.go
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time" // Add this line

	"github.com/ian-kent/gptchat/module"
	"github.com/ian-kent/gptchat/module/memory"
	"github.com/ian-kent/gptchat/module/plugin"
	"github.com/ian-kent/gptchat/ui"
)

func init() {
	// Directly assign the API key
	openaiAPIKey := strings.TrimSpace(cfg.OpenAIAPIKey()) // Replace with your actual API key
	cfg = cfg.WithOpenAIAPIKey(openaiAPIKey)

	// Ensure the model is set to GPT-4o
	openaiAPIModel := "deepseek-ai/DeepSeek-V3"
	cfg = cfg.WithOpenAIAPIModel(openaiAPIModel)
	// if openaiAPIModel == "" {
	// 	ui.Warn("You haven't configured an OpenAI API model, defaulting to GPT4")
	// 	openaiAPIModel = openai.GPT40314
	// }

	cfg = cfg.WithOpenAIAPIModel(openaiAPIModel)
	geminiKey := strings.TrimSpace(cfg.GeminiAPIKey())
        cfg = cfg.WithGeminiAPIKey(geminiKey)
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

	client = &http.Client{Timeout: time.Minute} // Initialize HTTP client

	// Load modules with a nil client (if the client isn't used)
	module.Load(cfg, nil, []module.Module{
		&memory.Module{},
		&plugin.Module{},
	}...)

	// Load compiled plugins
	if err := module.LoadCompiledPlugins(); err != nil {
		ui.Warn(fmt.Sprintf("error loading compiled plugins: %s", err))
	}

}
func main() {
	// initConversation()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Application encountered a critical error: %v", r)
			dumpConversations() // Dump conversations to a file
			//sendErrorEmail(fmt.Sprintf("Application encountered a critical error: %v", r))
			os.Exit(1) // Exit with a non-zero status to indicate failure
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		log.Printf("Received signal: %v", sig)
		dumpConversations() // Dump conversations to a file
		os.Exit(0)          // Exit gracefully
	}()
	if _, err := os.Stat("conversation.json"); err == nil {
		log.Println("conversation.json found, importing conversations.")
		importConversations()
                log.Println("Importing sessions")
                restoreSessionsFromConversations()
	} else {
		log.Println("conversation.json not found, initializing new conversation.")
		initConversation("Config")
	}
	startServer()
}
