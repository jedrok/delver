package main

import (
	"log"

	"github.com/jedrok/delver/internal/config"
	"github.com/joho/godotenv"
	//"github.com/gin-gonic/gin"
)

func main() {

	err := godotenv.Load()

	if err != nil {
		log.Fatalf("Failed to load env variables: %v", err)
	}

	cfg, err := config.Load()
	_ = cfg
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

}
