package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	endpoint := "http://127.0.0.1:8080/readyz"
	if len(os.Args) == 2 {
		endpoint = os.Args[1]
	}
	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
