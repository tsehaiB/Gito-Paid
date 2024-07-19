package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ian-kent/gptchat/module"
	"github.com/ian-kent/gptchat/parser"
	"github.com/ian-kent/gptchat/ui"
	"github.com/ian-kent/gptchat/util"
	"github.com/sashabaranov/go-openai"
)

var (
	conversationMutex sync.Mutex
)

func processChat(userInput string) string {
	conversationMutex.Lock()
	defer conversationMutex.Unlock()
	appendMessage(openai.ChatMessageRoleUser, userInput)

	// Check for slash commands
	ok, result := parseSlashCommand(userInput)
	if ok {
		if result.resetConversation {
			resetConversation()
			initConversation()
			return "Conversation reset."
		}
		// Handle other slash commands
		if result.toggleDebugMode {
			cfg = cfg.WithDebugMode(!cfg.IsDebugMode())
			module.UpdateConfig(cfg)
			if cfg.IsDebugMode() {
				return "Debug mode is now enabled."
			} else {
				return "Debug mode is now disabled."
			}
		}
		if result.toggleSupervisedMode {
			cfg = cfg.WithSupervisedMode(!cfg.IsSupervisedMode())
			module.UpdateConfig(cfg)
			if cfg.IsSupervisedMode() {
				return "Supervised mode is now enabled."
			} else {
				return "Supervised mode is now disabled."
			}
		}
		// If there's a prompt to send to the AI, use it
		if result.prompt != "" {
			userInput = result.prompt
		}
	}

	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:    cfg.OpenAIAPIModel(),
			Messages: conversation,
		},
	)
	if err != nil {
		if strings.HasPrefix(err.Error(), "error, status code: 429") {
			time.Sleep(time.Second)
			resp, err = client.CreateChatCompletion(
				context.Background(),
				openai.ChatCompletionRequest{
					Model:    cfg.OpenAIAPIModel(),
					Messages: conversation,
				},
			)
			print(cfg.OpenAIAPIModel())
		}
		if err != nil {
			fmt.Println("Error processing chat:", err)
			return "Error processing chat"
		}
	}

	response := resp.Choices[0].Message.Content
	appendMessage(openai.ChatMessageRoleAssistant, response)

	parseResult := parser.Parse(response)
	if !cfg.IsDebugMode() && parseResult.Chat != "" {
		ui.PrintChat(ui.AI, parseResult.Chat)
	}

	for _, command := range parseResult.Commands {
		ok, result := module.ExecuteCommand(command.Command, command.Args, command.Body)
		if ok {
			if result.Error != nil {
				msg := fmt.Sprintf(`An error occurred executing your command.

The command was:
`+util.TripleQuote+`
%s
`+util.TripleQuote+`

The error was:
`+util.TripleQuote+`
%s
`+util.TripleQuote, command.String(), result.Error.Error())

				if result.Prompt != "" {
					msg += fmt.Sprintf(`

The command provided this additional output:
`+util.TripleQuote+`
%s
`+util.TripleQuote, result.Prompt)
				}

				appendMessage(openai.ChatMessageRoleSystem, msg)
				if cfg.IsDebugMode() {
					ui.PrintChatDebug(ui.Module, msg)
				}
				continue
			}

			commandResult := fmt.Sprintf(`Your command returned some output.

The command was:
`+util.TripleQuote+`
%s
`+util.TripleQuote+`

The output was:

%s`, command.String(), result.Prompt)
			appendMessage(openai.ChatMessageRoleSystem, commandResult)

			if cfg.IsDebugMode() {
				ui.PrintChatDebug(ui.Module, commandResult)
			}
			continue
		}
	}

	return response
}

func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		Message string `json:"message"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	response := processChat(input.Message)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"response": response})
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	conversationMutex.Lock()
	defer conversationMutex.Unlock()
	resetConversation()
	initConversation()
	w.WriteHeader(http.StatusOK)
}

func startServer() {
	http.HandleFunc("/chat", chatHandler)
	http.HandleFunc("/reset", resetHandler)
	fmt.Println("Starting server on :8080")
	http.ListenAndServe(":8080", nil)
}
