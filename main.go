package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "serve":
		port := 8000
		if len(args) > 1 {
			p, err := strconv.Atoi(args[1])
			if err != nil {
				log.Fatalf("invalid port: %s", args[1])
			}
			port = p
		}
		if err := runServer(port); err != nil {
			log.Fatal(err)
		}

	case "accounts":
		mgr := newAccountManager()
		if err := mgr.loadFromFile(); err != nil && !os.IsNotExist(err) {
			log.Fatal(err)
		}
		if len(mgr.accounts) == 0 {
			fmt.Println("No accounts configured. Create accounts.json manually.")
			fmt.Println("See accounts.example.json for format.")
			return
		}
		for i, acc := range mgr.accounts {
			marker := "  "
			if i == mgr.current {
				marker = "* "
			}
			fmt.Printf("%s%s (uid: %s)\n", marker, acc.Name, acc.UserID)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"total":   len(mgr.accounts),
			"current": mgr.accounts[mgr.current].Name,
		}, "", "  ")
		fmt.Println(string(data))

	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`zed2api - Zed LLM API Proxy (Go)

Usage:
  zed2api serve [port]    Start API server (default: 8000)
  zed2api accounts        List configured accounts

Endpoints:
  POST /v1/chat/completions   OpenAI compatible
  POST /v1/messages           Anthropic native
  GET  /v1/models             List models
  GET  /zed/accounts          List accounts
  GET  /                      Web UI

accounts.json format: see accounts.example.json
`)
}
