package main

import (
	"github.com/ian-kent/gptchat/config"

	"github.com/sashabaranov/go-openai"
)

var client *openai.Client
var cfg = config.New()
