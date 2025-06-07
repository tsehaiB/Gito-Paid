package main

import (
	"github.com/ian-kent/gptchat/config"
	"net/http"
)

var (
    client *http.Client // Change from *openai.Client to *http.Client
)
var cfg = config.New()
