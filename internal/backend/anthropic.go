package backend

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/connorhoulihan/rowtr/internal/router"
)

// Anthropic is the frontier Expert backend, using the official anthropic-sdk-go.
type Anthropic struct {
	Model     string
	MaxTokens int64
	client    anthropic.Client
}

// NewAnthropic constructs the backend. The client reads ANTHROPIC_API_KEY from
// the environment; if it's unset, Available reports that before we ever dispatch.
func NewAnthropic(model string) *Anthropic {
	return &Anthropic{
		Model:     model,
		MaxTokens: 1024,
		client:    anthropic.NewClient(),
	}
}

func (a *Anthropic) Name() string      { return "anthropic" }
func (a *Anthropic) Tier() router.Tier { return router.Frontier }

// Available checks for the API key without making a network call.
func (a *Anthropic) Available(_ context.Context) error {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set\n" +
			"  fix: export ANTHROPIC_API_KEY=sk-ant-...")
	}
	return nil
}

// Complete runs the prompt through the frontier model.
func (a *Anthropic) Complete(ctx context.Context, prompt string) (Response, error) {
	msg, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.Model),
		MaxTokens: a.MaxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return Response{}, fmt.Errorf("anthropic request failed: %w", err)
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(tb.Text)
		}
	}

	return Response{
		Text:         text.String(),
		Model:        string(msg.Model),
		InputTokens:  int(msg.Usage.InputTokens),
		OutputTokens: int(msg.Usage.OutputTokens),
	}, nil
}
