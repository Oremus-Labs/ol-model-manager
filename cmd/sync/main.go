// Package main boots the HuggingFace/vLLM synchronization service scaffold.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/oremus-labs/ol-model-manager/config"
	"github.com/oremus-labs/ol-model-manager/internal/syncsvc"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"
)

const syncVersion = "0.4.15-go"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Model Manager sync service v%s", syncVersion)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := config.Load()
	discovery := vllm.New(
		vllm.WithGitHubToken(cfg.GitHubToken),
		vllm.WithHuggingFaceToken(cfg.HuggingFaceToken),
		vllm.WithHuggingFaceCacheTTL(cfg.HuggingFaceCacheTTL),
		vllm.WithVLLMCacheTTL(cfg.VLLMCacheTTL),
	)

	service := syncsvc.New(syncsvc.Options{
		Discovery: discovery,
	})

	if err := service.Run(ctx); err != nil && err != context.Canceled {
		log.Printf("sync service stopped: %v", err)
		os.Exit(1)
	}
	log.Println("sync service exited cleanly")
}
