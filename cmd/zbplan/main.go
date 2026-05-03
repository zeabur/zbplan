package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider/compat"
)

type GeneratedDockerfile struct {
	Dockerfile string `json:"dockerfile"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	model := compat.Chat("glm-5",
		compat.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
		compat.WithBaseURL(os.Getenv("OPENAI_BASE_URL")),
	)

	result, err := goai.GenerateObject[GeneratedDockerfile](ctx, model,
		goai.WithPrompt("Generate a dockerfile based on "),
	)
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Text)
	fmt.Printf("Tokens: %d in, %d out\n",
		result.TotalUsage.InputTokens, result.TotalUsage.OutputTokens)
}
