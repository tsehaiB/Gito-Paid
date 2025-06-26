
package main


import (
    "strconv"

    "net/url"
    "unicode"
    "unicode/utf8"
    "context"
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "sync"
    "time"
    "regexp"
    "strings"
    "github.com/go-shiori/go-readability"
    "github.com/sashabaranov/go-openai"
)
var (
	conversationMutex sync.Mutex
)
// --- Session Management ---
type UserSession struct {
    ID                string    // Format: "User123#S<timestamp>-<sequence>"
    Active            bool
    Timer             *time.Timer
    UserTotalSessions int       // Total sessions for this user
    LastSessionTime   int64     // Unix timestamp of last session creation
}
var (
    sessions   = make(map[string]*UserSession) // userID → UserSession
    sessionMux sync.Mutex
)

const (
    SessionTimeout = 1 * time.Minute // Test with 3 minutes
)
var sentenceAbbrevs = map[string]bool{
        "vs.": true, "e.g.": true, "i.e.": true, "Dr.": true,
        "Prof.": true, "Mr.": true, "Mrs.": true, "Ms.": true,
        "U.S.": true, "Ph.D.": true, "A.D.": true, "B.C.": true,
        "etc.": true, "cf.": true, "ex.": true, "approx.": true,
    }

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
func restoreSessionsFromConversations() {
    conversationMutex.Lock()
    defer conversationMutex.Unlock()
    sessionMux.Lock()
    defer sessionMux.Unlock()

    // Clear existing sessions to avoid duplicates
    sessions = make(map[string]*UserSession)

    for key := range conversations {
        // Skip summary entries and system config
        if strings.HasPrefix(key, "SUM_") || key == "Config" {
            continue
        }

        // Extract userID from conversation key (format: "userID#Stimestamp-seq")
        parts := strings.Split(key, "#S")
        if len(parts) != 2 {
            continue // Invalid format
        }
        userID := parts[0]

        // Extract timestamp and sequence from the session part
        sessionParts := strings.Split(parts[1], "-")
        if len(sessionParts) != 2 {
            continue // Invalid format
        }

        timestamp, err := strconv.ParseInt(sessionParts[0], 10, 64)
        if err != nil {
            continue // Invalid timestamp
        }

        seq, err := strconv.Atoi(sessionParts[1])
        if err != nil {
            continue // Invalid sequence
        }

        // Get or create user session
        session, exists := sessions[userID]
        if !exists {
            session = &UserSession{
                ID:                key,
                Active:            false, // Restored sessions start inactive
                UserTotalSessions: seq,
                LastSessionTime:   timestamp,
            }
            sessions[userID] = session
        } else {
            // Update if this is a more recent session
            if seq > session.UserTotalSessions {
                session.UserTotalSessions = seq
                session.ID = key
                session.LastSessionTime = timestamp
            }
        }
    }

    // Log restoration results
    log.Printf("Restored %d user sessions from conversations", len(sessions))
}


// Called on every AI response

func startSessionTimer(userID string) {
    sessionMux.Lock()
    defer sessionMux.Unlock()

    session, exists := sessions[userID]
    if !exists {
        // Initialize new user session with timestamped ID
        session = &UserSession{
            ID:               fmt.Sprintf("%s#S%d-1", userID, time.Now().Unix()), // "User123#S1624291200-1"
            Active:           true,
            UserTotalSessions: 1,
            LastSessionTime:   time.Now().Unix(),
        }
        sessions[userID] = session
        fmt.Printf("[DEBUG] New session %s for %s (Total: %d)\n", 
            session.ID, userID, session.UserTotalSessions)
    } else {
        // Existing user: Only increment if session was inactive
        if !session.Active {
	    session.Active = true 
            session.UserTotalSessions++
            session.ID = fmt.Sprintf("%s#S%d-%d", 
                userID, 
                time.Now().Unix(), 
                session.UserTotalSessions) // "User123#S1624291300-2"
            session.LastSessionTime = time.Now().Unix()
            fmt.Printf("[DEBUG] New session %s for %s (Total: %d)\n", 
                session.ID, userID, session.UserTotalSessions)
        }
    }

    // Reset timer (existing logic)
    if session.Timer != nil {
        session.Timer.Stop()
    }
    session.Timer = time.AfterFunc(SessionTimeout, func() {
        expireSession(userID)
    })
    fmt.Printf("[DEBUG] Timer reset for %s (Active: %v)\n", 
        userID, session.Active)
}

func expireSession(userID string) {
    // Lock ONLY session state
    sessionMux.Lock()
    session := sessions[userID]
    session.Active = false
    sessionMux.Unlock() // Release early!

    // Prepare summary outside the lock
    summaryKey := fmt.Sprintf("SUM_%s", session.ID)
    placeholder := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleSystem, Content: "[PENDING_SUMMARY]"},
    }

    // Lock conversations ONLY for the write
    conversationMutex.Lock()
    conversations[summaryKey] = placeholder
    conversationMutex.Unlock()

    rawMessages := conversations[session.ID] // Copy to avoid mutex issues
    go summarizeSession(session.ID, rawMessages)


    fmt.Printf("[ARCHIVE] Stored summary for %s (Mutex safe)\n", summaryKey)
}
func buildContext(userID string) []openai.ChatCompletionMessage {
    // First get session info with session lock
    sessionMux.Lock()
    session, exists := sessions[userID]
    if !exists {
        sessionMux.Unlock()
        return []openai.ChatCompletionMessage{}
    }
    totalSessions := session.UserTotalSessions
    currentSessionID := session.ID
    sessionMux.Unlock()

    // Now lock conversations for building context
    conversationMutex.Lock()
    defer conversationMutex.Unlock()

    ctx := []openai.ChatCompletionMessage{}

    // 1. Always add Config first
    if config, exists := conversations["Config"]; exists {
        ctx = append(ctx, config...)
    }

    // 2. Add summaries for previous sessions (up to N-1)
    for i := 1; i < totalSessions; i++ {
        // Construct summary key pattern
        sumKey := fmt.Sprintf("SUM_%s#S", userID)
        fmt.Printf(sumKey)
        for key, messages := range conversations {
            if strings.HasPrefix(key, sumKey) {
                // Extract sequence number from key like "SUM_user#Stimestamp-seq"
                parts := strings.Split(key, "-")
                if len(parts) == 2 {
                    seq, err := strconv.Atoi(parts[1])
                    if err == nil && seq == i {
                        ctx = append(ctx, messages...)
                        break
                    }
                }
            }
        }
    }

    // 3. Add previous session (N-1) if exists
    if totalSessions > 1 {
        prevKey := fmt.Sprintf("%s#S", userID)
        fmt.Printf(prevKey)
        for key, messages := range conversations {
            if strings.HasPrefix(key, prevKey) && !strings.HasPrefix(key, "SUM_") {
                // Extract sequence number
                parts := strings.Split(key, "-")
                if len(parts) == 2 {
                    seq, err := strconv.Atoi(parts[1])
                    if err == nil && seq == totalSessions-1 {
                        ctx = append(ctx, messages...)
                        break
                    }
                }
            }
        }
    }

    // 4. Add current session messages
    if currentMessages, exists := conversations[currentSessionID]; exists {
        ctx = append(ctx, currentMessages...)
    }

    return ctx
}
func callGeminiAPI(prompt string) (string, error) {
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

    bodyBytes, _ := json.Marshal(reqBody)
    geminiEndpoint := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-preview-05-20:generateContent"
    req, _ := http.NewRequest("POST", geminiEndpoint, bytes.NewReader(bodyBytes))
    req.Header.Set("Content-Type", "application/json")
    q := req.URL.Query()
    q.Add("key", cfg.GeminiAPIKey())
    req.URL.RawQuery = q.Encode()

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var result struct {
        Candidates []struct {
            Content struct {
                Parts []struct {
                    Text string `json:"text"`
                } `json:"parts"`
            } `json:"content"`
        } `json:"candidates"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return "", err
    }

    if len(result.Candidates) == 0 {
        return "", fmt.Errorf("no Gemini response")
    }

    return result.Candidates[0].Content.Parts[0].Text, nil
}
func buildSummaryPrompt(messages []openai.ChatCompletionMessage) string {
    var sb strings.Builder
    sb.WriteString("Summarize this conversation in 1-2 sentences for future context. Focus on:\n")
    sb.WriteString("- Key user requests/questions\n")
    sb.WriteString("- Technical details (e.g., code snippets)\n")
    sb.WriteString("- Preferences/tone\n\n")
    sb.WriteString("Conversation:\n")
    
    for _, msg := range messages {
        sb.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
    }
    
    sb.WriteString("\nRespond ONLY with the summary, no prefixes.")
    return sb.String()
}
// Handles async summarization and placeholder replacement
func summarizeSession(sessionID string, rawMessages []openai.ChatCompletionMessage) {
    go func() {
        // 1. Call Gemini
        prompt := buildSummaryPrompt(rawMessages) // (See prompt template below)
        summary, err := callGeminiAPI(prompt)
        if err != nil {
            log.Printf("[GEMINI] Summarization failed: %v", err)
            summary = fmt.Sprintf("[ERROR_SUMMARY] %s", err.Error())
        }

        // 2. Atomic replacement
        conversationMutex.Lock()
        defer conversationMutex.Unlock()
        conversations["SUM_"+sessionID] = []openai.ChatCompletionMessage{
            {
                Role:    "system",
                Content: summary,
            },
        }
        fmt.Printf("[GEMINI] Replaced summary for %s\n", sessionID)
    }()
}
func callSearxng(query string, links []string) ([]string, error) {
    var allContents []string
    
    // 1. First perform the regular web search
    searxngURL := "https://lina.infogito.com/search"
    req, err := http.NewRequest("GET", searxngURL, nil)
    if err != nil {
        return nil, fmt.Errorf("error creating request: %v", err)
    }

    q := req.URL.Query()
    q.Add("q", query)
    q.Add("format", "json")
    q.Add("limit", "5")
    req.URL.RawQuery = q.Encode()

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("error calling SearxNG: %v", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("error reading response: %v", err)
    }

    var result struct {
        Results []struct {
            Content string `json:"content"`
        } `json:"results"`
    }

    if err := json.Unmarshal(body, &result); err != nil {
        return nil, fmt.Errorf("error parsing JSON: %v", err)
    }

    // Add regular search results
    for i, res := range result.Results {
        if i >= 5 {
            break
        }
        allContents = append(allContents, fmt.Sprintf("Search result %d: %s", i+1, res.Content))
    }

    // 2. Then fetch content from any provided links
    if len(links) > 0 {
        fmt.Println("We have links!")
        for _, link := range links {
            content, err := fetchURLContent(link)
            if err != nil {
                log.Printf("Failed to fetch URL %s: %v", link, err)
                continue
            }
            allContents = append(allContents, fmt.Sprintf("Content from link %s:\n%s", link, content))
        }
    }

    return allContents, nil
}
func fetchURLContent(urlStr string) (string, error) {
    // First parse the URL string into a *url.URL
    parsedURL, err := url.Parse(urlStr)
    if err != nil {
        return "", fmt.Errorf("failed to parse URL: %v", err)
    }

    resp, err := http.Get(urlStr)
    if err != nil {
        return "", fmt.Errorf("failed to fetch URL: %v", err)
    }
    defer resp.Body.Close()

    // Now pass the parsed *url.URL instead of the string
    article, err := readability.FromReader(resp.Body, parsedURL)
    if err != nil {
        return "", fmt.Errorf("failed to parse content: %v", err)
    }

    return fmt.Sprintf("Title: %s\nContent: %s", article.Title, article.TextContent), nil
}

func shouldUseOriginal(original, modified string) bool {
    // 1. Empty check
    if original == "" || modified == "" {
        return true
    }

    // 2. Length check (skip if you don't care)
    lenOrig := utf8.RuneCountInString(original)
    lenMod := utf8.RuneCountInString(modified)
    if abs(lenOrig-lenMod) > int(0.1*float64(lenOrig)) {
        return true
    }

    // 3. Lightning-fast alphabet-only frequency check
    var freqDiff [256]int // Covers all ASCII letters (A-Za-z)

    // Count original letters
    for _, c := range original {
        if c >= 'a' && c <= 'z' {
            freqDiff[c-'a']++
        } else if c >= 'A' && c <= 'Z' {
            freqDiff[c-'A'+26]++ // Uppercase in second half
        }
    }

    // Count modified letters and check diffs
    for _, c := range modified {
        if c >= 'a' && c <= 'z' {
            idx := c - 'a'
            freqDiff[idx]--
            if freqDiff[idx] < -10 {
                return true
            }
        } else if c >= 'A' && c <= 'Z' {
            idx := c - 'A' + 26
            freqDiff[idx]--
            if freqDiff[idx] < -10 {
                return true
            }
        }
    }

    // 4. Final sum check (if needed)
    totalDiff := 0
    for _, diff := range freqDiff {
        if abs(diff) > 10 {
            return true
        }
        totalDiff += abs(diff)
    }
    return totalDiff > 5*52 // 26 letters × 2 cases
}

func abs(x int) int {
    if x < 0 { return -x }
    return x
}
func reformatTextWithGemini(input string) (string, error) {
    sentences := splitIntoSentences(input)

    // Filter sentences that need processing
    var sentencesToProcess []struct {
        index int
        text  string
        punct rune
    }
    for i, s := range sentences {
        punct := rune('.') // Default punctuation
        if len(s) > 0 {
            lastChar := rune(s[len(s)-1])
            if lastChar == '.' || lastChar == '!' || lastChar == '?' {
                punct = lastChar
            }
        }
        
        if strings.Contains(s, "—") || strings.Contains(s, "–") {
            sentencesToProcess = append(sentencesToProcess, struct {
                index int
                text  string
                punct rune
            }{i, s, punct})
        }
    }

    // If no sentences need processing, return early
    if len(sentencesToProcess) == 0 {
        return input, nil
    } else {
       // Extract just the text fields for logging
        var texts []string
        for _, stp := range sentencesToProcess {
            texts = append(texts, stp.text)
        }
        fmt.Printf("Sentences sent to gemini: %s\n", strings.Join(texts, "\n"))
    }
    geminiAPIURL := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-preview-05-20:generateContent"
    prompt := `You are a reformatter. You take inputs, and you remove all en dashes and em dashes, you maintain the content but rewrite the structure so it does not need any em dashes, you prioritize simpler punctuation. You do not change the meaning of your input, but you can add new words to remove an em dash. When removing a em dash that was placed for emphasis, use transitional words to rewrite the phrasing, but keep the original words. Let no en dash or em dash escape. Remove them all! However, never touch a hyphen, those are fine. Hyphens should not be reformatted. When you a repunctuating an em dash, replace it with commas and periods over colons and semi colons. Never change formatting unrelated to the em dash. Never say anything before or after the rewritten text. Just respond with the rewritten text.`

    // Create a worker pool with limited concurrency (adjust as needed)
    maxConcurrent := 5 // Don't overwhelm the API
    sem := make(chan struct{}, maxConcurrent)
    results := make(chan struct {
        index int
        text  string
        err   error
    }, len(sentencesToProcess))

    var wg sync.WaitGroup
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Process sentences in parallel
    for _, stp := range sentencesToProcess {
        wg.Add(1)
        go func(idx int, sentence string, punct rune) {
            defer wg.Done()

            select {
            case sem <- struct{}{}:
                defer func() { <-sem }()
            case <-ctx.Done():
                return
            }

            // Prepare and send request
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
                            {Text: prompt + sentence},
                        },
                    },
                },
            }

            bodyBytes, err := json.Marshal(reqBody)
            if err != nil {
                results <- struct {
                    index int
                    text  string
                    err   error
                }{idx, "", fmt.Errorf("error marshaling request body: %v", err)}
                return
            }

            req, err := http.NewRequest("POST", geminiAPIURL, bytes.NewReader(bodyBytes))
            if err != nil {
                results <- struct {
                    index int
                    text  string
                    err   error
                }{idx, "", fmt.Errorf("error creating request: %v", err)}
                return
            }

            q := req.URL.Query()
            q.Add("key", cfg.GeminiAPIKey())
            req.URL.RawQuery = q.Encode()
            req.Header.Set("Content-Type", "application/json")

            client := &http.Client{Timeout: 30 * time.Second}
            resp, err := client.Do(req)
            if err != nil {
                results <- struct {
                    index int
                    text  string
                    err   error
                }{idx, "", fmt.Errorf("error sending request: %v", err)}
                return
            }
            defer resp.Body.Close()

            if resp.StatusCode != http.StatusOK {
                results <- struct {
                    index int
                    text  string
                    err   error
                }{idx, "", fmt.Errorf("API returned non-200 status: %s", resp.Status)}
                return
            }

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
                results <- struct {
                    index int
                    text  string
                    err   error
                }{idx, "", fmt.Errorf("error decoding response: %v", err)}
                return
            }

            if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
                results <- struct {
                    index int
                    text  string
                    err   error
                }{idx, "", fmt.Errorf("no content in response")}
                return
            }

            // Remove any trailing punctuation from the processed text
            processedText := strings.TrimRight(geminiResp.Candidates[0].Content.Parts[0].Text, ".!?")
            // Add back the original punctuation
            finalText := processedText + string(punct)
            
            results <- struct {
                index int
                text  string
                err   error
            }{idx, finalText, nil}
        }(stp.index, stp.text, stp.punct)
    }

    // Close results channel when all workers are done
    go func() {
        wg.Wait()
        close(results)
    }()

    // Collect results
    processed := make(map[int]string)
    var firstErr error
    for result := range results {
        if result.err != nil && firstErr == nil {
            firstErr = result.err
            cancel() // Cancel remaining requests
            continue
        }
        processed[result.index] = result.text
    }

    if firstErr != nil {
        return "", firstErr
    }

    // Rebuild the text with processed sentences
   // Rebuild the text with processed sentences while preserving newlines
    // Rebuild the text with proper spacing
    var result strings.Builder
    for i, s := range sentences {
        // Use processed version if available, otherwise original
        if processedText, exists := processed[i]; exists {
            result.WriteString(processedText)
        } else {
            result.WriteString(s)
        }

        // Handle spacing between sentences
        if i < len(sentences)-1 {
            next := sentences[i+1]
            currentEndsWithSpace := len(s) > 0 && unicode.IsSpace(rune(s[len(s)-1]))
            nextStartsWithSpace := len(next) > 0 && unicode.IsSpace(rune(next[0]))
            
            // Add space only if:
            // 1. Current doesn't end with space AND
            // 2. Next doesn't start with space AND 
            // 3. Next isn't a newline
            if !currentEndsWithSpace && !nextStartsWithSpace && next != "\n" {
                result.WriteString(" ")
            }
        }
    }
    return result.String(), nil

}
func splitIntoSentences(text string) []string {

    var sentences []string
    start := 0
    inQuote := false

    for i := 0; i < len(text); i++ {
        currChar := text[i]

        // Track quote state
        if currChar == '"' || currChar == '\'' {
            inQuote = !inQuote
            continue
        }

        // Handle standalone newlines
        if currChar == '\n' && !inQuote {
            // Add content before newline if it exists
            if i > start {
                sentence := text[start:i]
                if strings.TrimSpace(sentence) != "" {
                    sentences = append(sentences, sentence)
                }
            }
            // Add the newline as a separate "sentence"
            sentences = append(sentences, "\n")
            start = i + 1
            continue
        }

        // Check for sentence boundaries
        if i > 0 {
            prevChar := text[i-1]
            if (prevChar == '.' || prevChar == '!' || prevChar == '?') && !inQuote {
                if unicode.IsSpace(rune(currChar)) {
                    isAbbreviation := false
                    for abbr := range sentenceAbbrevs {
                        abbrLen := len(abbr)
                        if i >= abbrLen && strings.HasSuffix(text[start:i], abbr) {
                            isAbbreviation = true
                            break
                        }
                    }
                    if !isAbbreviation {
                        sentence := text[start:i]
                        if strings.TrimSpace(sentence) != "" {
                            sentences = append(sentences, sentence)
                        }
                        start = i
                    }
                }
            }
        }
    }

    // Add remaining text
    if start < len(text) {
        remaining := text[start:]
        if strings.TrimSpace(remaining) != "" {
            sentences = append(sentences, remaining)
        }
    }

    return sentences
}
func requiresWebSearch(userID, query string) (mode string, payload string, links []string, err error) {
    // Get base prompt from config
    basePrompt := cfg.GeminiPrompt()

    // Build indexed summary list for this user
    type summaryEntry struct {
        ID      string
        Content string
    }
    var userSummaries []summaryEntry

    conversationMutex.Lock()
    for key, messages := range conversations {
        if strings.HasPrefix(key, "SUM_"+userID) {
            cleanID := strings.TrimPrefix(key, "SUM_")
            userSummaries = append(userSummaries, summaryEntry{
                ID:      cleanID,
                Content: messages[0].Content,
            })
        }
    }
    conversationMutex.Unlock()

    // Format summaries with explicit IDs
    var summaryStrings []string
    for _, entry := range userSummaries {
        summaryStrings = append(summaryStrings, 
            fmt.Sprintf("Summary ID: %s\nContent: %s", entry.ID, entry.Content))
    }

    enhancedPrompt := fmt.Sprintf(`%s

RESPONSE FORMATS (MUST USE ONE):
1. Web search required:
<question>your_rephrased_query</question>
<links>optional_url1.com\noptional_url2.com</links>

2. Memory recall required:
<memory>EXACT_SUMMARY_ID_HERE</memory>

3. No search needed:
not_needed

USER'S AVAILABLE SUMMARIES:
%s

CURRENT DATE: %s
QUERY: %s`,
        basePrompt,
        strings.Join(summaryStrings, "\n\n---\n"),
        time.Now().Format("2006-01-02"),
        query)
    // Prepare Gemini request
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
                    {Text: enhancedPrompt},
                },
            },
        },
    }

    bodyBytes, err := json.Marshal(reqBody)
    if err != nil {
        return "", "", nil, fmt.Errorf("error marshaling request: %v", err)
    }

    // Send request
    geminiAPIURL := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-preview-05-20:generateContent"
    req, err := http.NewRequest("POST", geminiAPIURL, bytes.NewReader(bodyBytes))
    if err != nil {
        return "", "", nil, fmt.Errorf("error creating request: %v", err)
    }

    q := req.URL.Query()
    q.Add("key", cfg.GeminiAPIKey())
    req.URL.RawQuery = q.Encode()
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return "", "", nil, fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    // Parse response
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
        return "", "", nil, fmt.Errorf("error decoding response: %v", err)
    }

    if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
        return "", "", nil, fmt.Errorf("empty response from Gemini")
    }

    responseText := geminiResp.Candidates[0].Content.Parts[0].Text

    // Validate and parse response
    switch {
    case strings.Contains(responseText, "<memory>"):
        requestedID := extractXMLTag(responseText, "memory")
        // Verify the ID exists for this user
        for _, s := range userSummaries {
            if s.ID == requestedID {
                return "memory", requestedID, nil, nil
            }
        }
        return "", "", nil, fmt.Errorf("invalid summary ID: %s", requestedID)

    case strings.Contains(responseText, "<question>"):
        return "internet", 
               extractXMLTag(responseText, "question"), 
               parseLinks(responseText), 
               nil

    case strings.TrimSpace(responseText) == "not_needed":
        return "none", "", nil, nil

    default:
        return "", "", nil, fmt.Errorf("invalid response format")
    }
}

// Helper functions
func extractXMLTag(input, tag string) string {
    re := regexp.MustCompile(fmt.Sprintf(`<%s>(.*?)</%s>`, tag, tag))
    matches := re.FindStringSubmatch(input)
    if len(matches) > 1 {
        return strings.TrimSpace(matches[1])
    }
    return ""
}

func parseLinks(response string) []string {
    linksRegex := regexp.MustCompile(`(?s)<links>(.*?)</links>`)
    linksMatch := linksRegex.FindStringSubmatch(response)
    if len(linksMatch) < 2 {
        return nil
    }
    return strings.Split(strings.TrimSpace(linksMatch[1]), "\n")
}
func parseGeminiResponse(response string) (string, []string) {
    // Debug print raw response

    // Extract <question> block
    questionRegex := regexp.MustCompile(`(?s)<question>(.*?)</question>`)
    questionMatch := questionRegex.FindStringSubmatch(response)
    var question string
    if len(questionMatch) >= 2 {
        question = strings.TrimSpace(questionMatch[1])
    } else {
        return "not_needed", nil
    }

    // Debug parsed question

    // Extract <links> block (if exists)
    linksRegex := regexp.MustCompile(`(?s)<links>(.*?)</links>`)
    linksMatch := linksRegex.FindStringSubmatch(response)
    var links []string
    
    if len(linksMatch) >= 2 {
        rawLinks := strings.TrimSpace(linksMatch[1])
        links = strings.Split(rawLinks, "\n")
        // Clean up each link
        for i, link := range links {
            links[i] = strings.TrimSpace(link)
        }
    }


    return question, links
}

func processChat(userID, userInput string) string {
    sessionMux.Lock()
    session, exists := sessions[userID]
    if !exists || !session.Active {
        // Create new session
        now := time.Now().Unix()
        seq := 1
        if exists {
            seq = session.UserTotalSessions + 1
        }
        session = &UserSession{
            ID:                fmt.Sprintf("%s#S%d-%d", userID, now, seq),
            Active:            true,
            UserTotalSessions: seq,
            LastSessionTime:   now,
        }
        sessions[userID] = session
    }
    // Reset timer
    if session.Timer != nil {
        session.Timer.Stop()
    }
    session.Timer = time.AfterFunc(SessionTimeout, func() {
        expireSession(userID)
    })
    sessionMux.Unlock()

    // 2. Store message with conversation mutex
    conversationMutex.Lock()
    conversations[session.ID] = append(conversations[session.ID], openai.ChatCompletionMessage{
        Role:    openai.ChatMessageRoleUser,
        Content: userInput,
    })
    conversationMutex.Unlock()

    ctx := buildContext(userID)

    // 3. Handle decision/monitor logic with session.ID
    // 5. Handle decision/monitor logic if needed
    if strings.Contains(userID, "decision") {
        usermemID := strings.Replace(userID, "decision#", "", 1)
        appendMessage(usermemID, openai.ChatMessageRoleUser, strings.Split(userInput, "\n")[0])
    }
     
    // 4. Build context using base userID (extracted from session.ID)



        internetContext := ""

        mode, payload, links, err := requiresWebSearch(userID, userInput)
        fmt.Println(mode)
        if err != nil {
                log.Printf("Search decision failed: %v", err)
                // internetContext remains empty
        } else {
                switch mode {
                case "internet":
                        // Get search results
                        searchContents, err := callSearxng(payload, links)
                        if err != nil {
                                log.Printf("Search failed: %v", err)
                        } else {
                                // Format results into internetContext
                                var builder strings.Builder
                                builder.WriteString("Web search results:\n")
                                for i, content := range searchContents {
                                        builder.WriteString(fmt.Sprintf("%d. %s\n", i+1, content))
                                }
                                internetContext = builder.String()
                        }
                case "memory":
                        conversationMutex.Lock()
                        if messages, exists := conversations[payload]; exists {
                                var builder strings.Builder
                                builder.WriteString("Complete conversation history:\n")
                                for _, msg := range messages {
                                        builder.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, msg.Content))
                                }
                                internetContext = builder.String()
                        }
                        conversationMutex.Unlock()
                case "none":
                        log.Println("No search needed for this query")
                default:
                        log.Printf("Unknown search mode: %s", mode)
                }
        }
	// Call TogetherAI's API directly
	messages := make([]openai.ChatCompletionMessage, len(ctx))
	copy(messages, ctx)

	if internetContext != "" {
		messages = append(
			[]openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: internetContext,
				},
			},
			messages...,
		)
	}

	reqBody := struct {
		Model    string                         `json:"model"`
		Messages []openai.ChatCompletionMessage `json:"messages"`
		Stream   bool                           `json:"stream"`
	}{
		Model:    cfg.OpenAIAPIModel(),
		Messages: messages,
		Stream:   false,
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
	fmt.Println("Made it 3")
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
	fmt.Println("Made it 4")
// Append AI response to conversation
        response := apiResult.Choices[0].Message.Content
	fmt.Printf("[DEBUG] Timer reset after AI reply to %s\n", userID)
        // Try to reformat, with multiple fallback checks
        var finalResponse string
        formResponse, err := reformatTextWithGemini(response)
        switch {
        case err != nil:
            log.Printf("Reformatting error: %v - Using original text", err)
            finalResponse = response
        case len(strings.TrimSpace(formResponse)) == 0:
            log.Printf("Reformatting returned empty - Using original text")
            finalResponse = response
        case shouldUseOriginal(response, formResponse): // 80% similarity threshold
            log.Printf("Reformatting changed meaning Using original text")
            fmt.Println("The modified was...: ", formResponse)
            finalResponse = response
        default:
            finalResponse = formResponse
        }

        // Clean and append (applies to both reformatted and fallback responses)
        //cleanResponse := cleanContent(finalResponse)
    // 8. Store AI response (with proper mutex)
        conversationMutex.Lock()
	conversations[session.ID] = append(conversations[session.ID], openai.ChatCompletionMessage{
        	Role:    openai.ChatMessageRoleAssistant,
        	Content: finalResponse,
    	})
    	conversationMutex.Unlock()
	return finalResponse
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

func startServer() {
	http.HandleFunc("/chat", chatHandler)
	http.HandleFunc("/reset", resetHandler)
	http.HandleFunc("/config", configHandler)
	fmt.Println("Starting server on :8087")
	http.ListenAndServe(":8087", nil)
}
