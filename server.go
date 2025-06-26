package main

import (
	"bytes"
	"html"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
	"regexp"
	"strings"
	"github.com/russross/blackfriday/v2"
	"github.com/ian-kent/gptchat/module"
	"github.com/sashabaranov/go-openai"
)

var (
	conversationMutex sync.Mutex
)

// Define the types globally
type GenerateRequest struct {
	Contents []Content `json:"contents"`
}
type Content struct {
	Parts []Part `json:"parts"`
}
type Part struct {
	Text string `json:"text"`
}
type GenerateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type GPTRequest struct {
	Model       string                         `json:"model"`
	Messages    []openai.ChatCompletionMessage `json:"messages"`
	Temperature float32                        `json:"temperature,omitempty"`
}

type GPTResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int                          `json:"index"`
		Message      openai.ChatCompletionMessage `json:"message"`
		FinishReason string                       `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}
func stripMarkdown(text string) string {
	// Render Markdown as plaintext (no HTML tags)
	plaintext := string(blackfriday.Run([]byte(text), blackfriday.WithExtensions(blackfriday.CommonExtensions)))
	return plaintext

}
func normalizeWhitespace(text string) string {
	// Replace newlines/tabs with spaces
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	// Collapse multiple spaces
	spaceRegex := regexp.MustCompile(`\s+`)
	return spaceRegex.ReplaceAllString(text, " ")
}
func removeEmojis(text string) string {
	// Regex for common emoji ranges
	emojiRegex := regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{1F1E0}-\x{1F1FF}\x{2600}-\x{26FF}\x{2700}-\x{27BF}]`)
	return emojiRegex.ReplaceAllString(text, "")
	}
func stripHTML(text string) string {
	// Remove HTML tags
	text = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(text, "")
	// Unescape HTML entities
	return html.UnescapeString(text)
}

func cleanContent(content string) string {
        content = stripMarkdown(content)
        content = removeEmojis(content)
        content = normalizeWhitespace(content)
        content = stripHTML(content)
        fmt.Println("all done cleaning!")
        return content
}
func reformatTextWithGemini(input string) (string, error) {
	// Define the Gemini API endpoint and API key
	geminiAPIKey := "AIzaSyCYCs1nbDHOjTMvEI46V6hyEqIiJcwbKTY" // Replace with your actual API key
	geminiAPIURL := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-preview-05-20:generateContent"

	// Prepare the prompt with the input
	prompt := `You are a reformatter. You take inputs, and you remove all em dashes, and any other form of dash. and in the LEAST rewriting possible, you replace them with simpler punctuation. You do not change the wording of things, but you can add new words to remove an em dash. When removing a transition word that was placed for emphasis, use transitional words to rewrite the phrasing, but keep the original words.

Let no en dash or em dash escape. Remove them all! Never say anything before or after the rewritten text. Just respond with the rewritten text.

Here is the text to reformat: ` + input

	// Prepare the request body
	reqBody := struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}{
		Contents: []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		}{
			{
				Parts: []struct {
					Text string `json:"text"`
				}{
					{Text: prompt},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("error marshaling request body: %v", err)
	}

	// Create the HTTP request
	req, err := http.NewRequest("POST", geminiAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Add API key as URL parameter
	q := req.URL.Query()
	q.Add("key", geminiAPIKey)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Content-Type", "application/json")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Check for API errors
	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned non-200 status: %s, body: %s", resp.Status, string(errorBody))
	}

	// Decode the response
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", fmt.Errorf("error decoding response: %v", err)
	}

	// Extract the response text
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no content in response")
	}

	// Return the reformatted text
	return geminiResp.Candidates[0].Content.Parts[0].Text, nil
}


func callGemini(message string) (string, error) {
	// Define the valid tags
	validTags := []string{"Investigating model", "Asking for advice", "Talking about Time"}

	// Define the Gemini API endpoint and API key
	geminiAPIKey := "AIzaSyCYCs1nbDHOjTMvEI46V6hyEqIiJcwbKTY" // Replace with your actual API key
	geminiAPIURL := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-preview-05-20:generateContent"
	geminiprompt := fmt.Sprintf(`You are a tag generator. Your task is to analyze the following message and determine if it matches any of these tags: "Investigating model", "Asking for advice", "Talking about Time". 
- Only reply with tags if they are completely relevant to the message.
- If no tags are relevant, reply with "NONE".
- Format the tags as a comma-separated list without spaces or newlines (e.g., "Investigating model,Asking for advice").
Here is the message: %s`, message)
	// Prepare the request body
	reqBody := GenerateRequest{
		Contents: []Content{
			{
				Parts: []Part{
					{Text: fmt.Sprintf(`%s%s`, geminiprompt, message)},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("error marshaling request body: %v", err)
	}

	// Create the HTTP request
	req, err := http.NewRequest("POST", geminiAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("error creating request: %v", err)
	}

	// Add API key as URL parameter
	q := req.URL.Query()
	q.Add("key", geminiAPIKey)
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Content-Type", "application/json")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Check for API errors
	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned non-200 status: %s, body: %s", resp.Status, string(errorBody))
	}

	// Decode the response
	var geminiResp GenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", fmt.Errorf("error decoding response: %v", err)
	}

	// Log the full response for debugging
	responseBytes, _ := json.MarshalIndent(geminiResp, "", "  ")
	fmt.Printf("Full Gemini API response: %s\n", string(responseBytes))

	// Extract the response text
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no content in response")
	}
	responseText := geminiResp.Candidates[0].Content.Parts[0].Text

	// Normalize the response text (remove newlines, trim spaces, etc.)
	responseText = strings.TrimSpace(responseText)
	responseText = strings.ReplaceAll(responseText, "\n", ", ") // Replace newlines with commas
	responseText = strings.ReplaceAll(responseText, "  ", " ")  // Remove extra spaces

	// Split the response into individual tags
	tags := strings.Split(responseText, ", ")

	// Check if any of the tags are valid
	for _, tag := range tags {
		for _, validTag := range validTags {
			if strings.EqualFold(tag, validTag) { // Case-insensitive comparison
				return tag, nil // Return the valid tag
			}
		}
	}

	// If no valid tag is found, return an empty string
	return "", nil
}
func processChat(userID, userInput string) string {
	conversationMutex.Lock()
	defer conversationMutex.Unlock()

	// Check for slash commands anywhere in the input
	ok, result := parseSlashCommand(userInput)
	if ok {
		// Handle reset command
		if result.resetConversation {
			resetConversation(userID)
			initConversation("Config")
			return "Conversation reset."
		}
		// Handle toggle debug mode command
		if result.toggleDebugMode {
			cfg = cfg.WithDebugMode(!cfg.IsDebugMode())
			module.UpdateConfig(cfg)
			if cfg.IsDebugMode() {
				return "Debug mode is now enabled."
			} else {
				return "Debug mode is now disabled."
			}
		}
		// Handle toggle supervised mode command
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
		} else {
			// If the command does not result in a prompt, return immediately
			return "Command executed."
		}
	}

	// If no command was detected, or we have a prompt to send to the AI, proceed with normal processing
	appendMessage(userID, openai.ChatMessageRoleUser, userInput)
	fmt.Println("Appended message to user: ", userID)
	if strings.Contains(userID, "decision") {
		usermemID := strings.Replace(userID, "decision#", "", 1)
		appendMessage(usermemID, openai.ChatMessageRoleUser, strings.Split(userInput, "\n")[0])
		fmt.Println("Appended message to user", usermemID, "not responding tho:", strings.Split(userInput, "\n")[0])
	}
	var combinedSlice []openai.ChatCompletionMessage
	if strings.Contains(userID, "monitor") {
		// If userID starts with "monitor", combinedSlice is only conversations[userID]
		combinedSlice = conversations[userID]
	} else if strings.Contains(userID, "decision") {
		// If userID starts with "instruct", combinedSlice is the appended slice of conversations["nolist"] and conversations[userID]
		combinedSlice = append(conversations["decision"], conversations[userID]...)
	} else {
		// Otherwise, combinedSlice is the appended slice of conversations["Config"] and conversations[userID]
		combinedSlice = append(conversations["Config"], conversations[userID]...)
	}
	if (false) {
		// Call Gemini to get tags for the user input
		tags, err := callGemini(userInput)
		if err != nil {
			fmt.Printf("Error calling Gemini: %v\n", err)
		} else if tags != "" {
			// Split the tags into a slice (if multiple tags are returned)
			tagList := strings.Split(tags, ", ") // Adjust the delimiter if needed

			// Iterate over the tags
			for _, tag := range tagList {
				fmt.Printf("Gemini tag: %s\n", tag)

				// Check if the tag exists in conversations
				if conv, ok := conversations[tag]; ok {
					// Append the conversation history for the tag to combinedSlice
					fmt.Printf("Appending this tag: %s\n", tag)
					combinedSlice = append(combinedSlice, conv...)
				}
			}
		} else {
			fmt.Println("No valid tags found for the message.")
		}
	}
	// Call TogetherAI's API directly
	// Call TogetherAI's API directly
	reqBody := struct {
		Model    string                         `json:"model"`
		Messages []openai.ChatCompletionMessage `json:"messages"`
		Stream   bool                           `json:"stream"`
	}{
		Model:    cfg.OpenAIAPIModel(), // Use the model from cfg
		Messages: combinedSlice,
		Stream:   false, // Set to true if you want streaming
	}
	bodyBytes, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "https://api.together.ai/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+cfg.OpenAIAPIKey()) // Use the API key from cfg
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			time.Sleep(time.Second) // Retry after rate limit
			resp, err = http.DefaultClient.Do(req)
		}
		if err != nil {
			fmt.Println("Aww shoot! Be back in like an hour, gotta check on something.", err)
			return "Aww shoot! Be back in like an hour, gotta check on something."
		}
	}
	defer resp.Body.Close()

	// Decode the response
	var apiResult struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResult); err != nil {
		return "Error decoding API response"
	}

	if len(apiResult.Choices) == 0 {
		return "No response from AI"
	}

	// Append AI response to conversation
	response := apiResult.Choices[0].Message.Content
	formResponse, err := reformatTextWithGemini(response)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	}
	fmt.Println("Reformatted text:", formResponse)
	response = formResponse
	appendMessage(userID, openai.ChatMessageRoleAssistant, cleanContent(response))

	return response
}
func handleAPIError(err error) string {
	if strings.Contains(err.Error(), "429") {
		return "Rate limit exceeded - please try again shortly"
	}
	log.Printf("API error: %v", err)
	return "Aww shoot! Be back in like an hour, gotta check on something."
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

	// Extract userID and userMessage from the input message
	messageParts := strings.SplitN(input.Message, ": ", 2)
	if len(messageParts) != 2 {
		http.Error(w, "Invalid message format", http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(strings.TrimPrefix(messageParts[0], "User"))
	userMessage := strings.TrimSpace(messageParts[1])

	response := processChat(userID, userMessage)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"response": response})
}

func resetHandler(w http.ResponseWriter, r *http.Request) {
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

	// Extract userID from the input message
	messageParts := strings.SplitN(input.Message, ": ", 2)
	if len(messageParts) < 1 {
		http.Error(w, "Invalid message format", http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(strings.TrimPrefix(messageParts[0], "User"))

	resetConversation(userID)
	initConversation("Config")
	w.WriteHeader(http.StatusOK)
}

func configHandler(w http.ResponseWriter, r *http.Request) {
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

	// Extract userID and system message from the input message
	messageParts := strings.SplitN(input.Message, ": ", 2)
	if len(messageParts) != 2 {
		http.Error(w, "Invalid message format", http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(strings.TrimPrefix(messageParts[0], "User"))
	systemMessage := strings.TrimSpace(messageParts[1])

	// Append the system message for the user
	appendMessage(userID, openai.ChatMessageRoleSystem, systemMessage)

	// Respond with success
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "Message appended successfully"})
}

type ErrorResponse struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    string  `json:"code"`
	} `json:"error"`
}

// New handler for /perplex endpoint
func perplexHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	// // Step 1: Validate API key
	// apiKey := r.Header.Get("Authorization")
	// expectedAPIKey := "tgp_v1__-HQ6sSoDvqekjFYTBud0VWCG9H5VGmDZbGvTCiPLQI" // Replace with your actual API key
	// if apiKey == "" || !strings.HasPrefix(apiKey, "Bearer ") || apiKey[7:] != expectedAPIKey {
	// 	errorResponse := ErrorResponse{
	// 		Error: struct {
	// 			Message string  `json:"message"`
	// 			Type    string  `json:"type"`
	// 			Param   *string `json:"param"`
	// 			Code    string  `json:"code"`
	// 		}{
	// 			Message: "Invalid API key provided. You can find your API key at https://api.together.xyz/settings/api-keys.",
	// 			Type:    "invalid_request_error",
	// 			Param:   nil,
	// 			Code:    "invalid_api_key",
	// 		},
	// 	}
	// 	w.Header().Set("Content-Type", "application/json")
	// 	w.WriteHeader(http.StatusUnauthorized)
	// 	json.NewEncoder(w).Encode(errorResponse)
	// 	return
	// }

	// Step 2: Parse the request body
	var gptReq GPTRequest
	if err := json.NewDecoder(r.Body).Decode(&gptReq); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Step 3: Call TogetherAI's API directly
	reqBody := struct {
		Model    string                         `json:"model"`
		Messages []openai.ChatCompletionMessage `json:"messages"`
		Stream   bool                           `json:"stream"`
	}{
		Model:    gptReq.Model, // Use the model from the request
		Messages: gptReq.Messages,
		Stream:   false, // Set to true if you want streaming
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		http.Error(w, "Error marshaling request body", http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequest("POST", "https://api.together.ai/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "Error creating request", http.StatusInternalServerError)
		return
	}

	req.Header.Set("Authorization", "Bearer tgp_v1__-HQ6sSoDvqekjFYTBud0VWCG9H5VGmDZbGvTCiPLQI") // Use the API key from cfg
	req.Header.Set("Content-Type", "application/json")
	log.Println(gptReq.Messages)
	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			time.Sleep(time.Second) // Retry after rate limit
			resp, err = client.Do(req)
		}
		if err != nil {
			http.Error(w, "Aww shoot! Be back in like an hour, gotta check on something.", http.StatusInternalServerError)
			return
		}
	}
	defer resp.Body.Close()

	// Check for API errors
	if resp.StatusCode != http.StatusOK {
		var errorResponse ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errorResponse); err != nil {
			http.Error(w, "Error decoding error response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(errorResponse)
		return
	}

	// Decode the response
	var apiResult struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int                          `json:"index"`
			Message      openai.ChatCompletionMessage `json:"message"`
			FinishReason string                       `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResult); err != nil {
		http.Error(w, "Error decoding API response", http.StatusInternalServerError)
		return
	}

	// Step 4: Return the response in the same JSON format as the regular GPT API
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiResult)
}

func startServer() {
	http.HandleFunc("/chat", chatHandler)
	http.HandleFunc("/reset", resetHandler)
	http.HandleFunc("/config", configHandler)
	http.HandleFunc("/perplex/chat/completions", perplexHandler) // Add the new endpoint
	fmt.Println("Starting server on :8087")
	http.ListenAndServe(":8087", nil)
}
