package kha_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	kha "github.com/MoonshotAI/kha"
)

// Create a client, start a Session, send a message, and wait for the turn to finish.
func Example() {
	ctx := context.Background()

	client, err := kha.New("") // Reads KIMI_API_KEY when apiKey is empty.
	if err != nil {
		log.Fatal(err)
	}

	sessionResult, err := client.Sessions.Create(ctx, kha.CreateSessionHTTPRequest{
		Body: &kha.CreateSessionRequest{
			AgentID:       "agent_...",
			EnvironmentID: "env_...",
			Title:         new("Summarize repository docs"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	session := sessionResult.Body

	result, err := client.Sessions.SendMessageAndWait(ctx, session.ID, "Summarize README.md")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.StopReason)
}

// Fetch a single page of Agents, or iterate lazily across all pages with All.
func Example_pagination() {
	ctx := context.Background()
	client, err := kha.New("")
	if err != nil {
		log.Fatal(err)
	}

	// Option 1: Fetch a single page and inspect NextPageToken manually.
	page, err := client.Agents.List(ctx, kha.ListAgentsRequest{})
	if err != nil {
		log.Fatal(err)
	}
	for _, agent := range page.Body.Items {
		if agent != nil {
			fmt.Println(agent.ID, agent.Name)
		}
	}
	_ = page.Body.NextPageToken

	// Option 2: Lazily iterate across all pages.
	for agent, err := range client.Agents.All(ctx, kha.ListAgentsRequest{}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(agent.ID, agent.Name)
	}
}

// Stream a Session's live events and dispatch on the event variant.
func Example_events() {
	ctx := context.Background()
	client, err := kha.New("")
	if err != nil {
		log.Fatal(err)
	}

	for ev, err := range client.Sessions.Events(ctx, "sesn_...") {
		if err != nil {
			log.Fatal(err) // Terminal error: iteration has stopped.
		}
		if msg := ev.AsAgentMessageEvent(); msg != nil {
			fmt.Println(msg.ID)
		}
	}
}

// Answer custom tool calls (and optional always_ask confirmations) from the Session event stream.
func Example_runTools() {
	ctx := context.Background()
	client, err := kha.New("")
	if err != nil {
		log.Fatal(err)
	}

	err = client.Sessions.RunTools(ctx, "sesn_...",
		kha.WithCustomTool("get_weather", func(ctx context.Context, call *kha.AgentCustomToolUseEvent) ([]*kha.ToolResultContent, error) {
			_ = ctx
			city, _ := call.Data.Input["city"].(string)
			return []*kha.ToolResultContent{kha.NewToolResultContentTextContentBlock(&kha.TextContentBlock{
				Type: kha.TextContentBlockTypeText, Text: city,
			})}, nil
		}),
		kha.WithToolConfirmation(func(ctx context.Context, call *kha.AgentToolUseEvent) (kha.ToolConfirmation, error) {
			_ = ctx
			return kha.ToolConfirmation{Allow: call.Data.ToolUse.Name != "bash", DenyMessage: "not allowed"}, nil
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
}

// Classify non-2xx responses with status helpers, Error, and HTTPError.
func Example_errorHandling() {
	var err error // Replace with an SDK call that returned an error.
	switch {
	case kha.IsNotFound(err):
		// The resource does not exist or is not visible to this credential.
	default:
		if apiErr, ok := errors.AsType[*kha.Error](err); ok {
			fmt.Println(apiErr.StatusCode(), apiErr.Body.Error.Type, apiErr.Body.Error.Message)
		} else if responseErr, ok := errors.AsType[kha.HTTPError](err); ok {
			fmt.Println(responseErr.StatusCode(), responseErr.Header().Get("Msh-Request-Id"))
		}
	}
}

// Configure a client explicitly without product defaults or environment fallbacks.
func ExampleNewClient() {
	client, err := kha.NewClient("https://api.moonshot.cn",
		kha.WithBearerToken("sk-..."),
		kha.WithHeader("Kimi-Api-Version", kha.DefaultAPIVersion),
		kha.WithMaxRetries(0),
	)
	if err != nil {
		log.Fatal(err)
	}

	result, err := client.Agents.Get(context.Background(), kha.GetAgentRequest{ID: "agent_..."})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Body.Name)
}

// Extract structured Hosted Agents error details alongside HTTP metadata.
func ExampleErrorDetails() {
	var err error // Replace with an SDK call that returned an error.

	if details, ok := kha.ErrorDetails(err); ok {
		fmt.Println(details.Type, details.Message)
	}
	if responseErr, ok := errors.AsType[kha.HTTPError](err); ok {
		fmt.Println(responseErr.StatusCode(), responseErr.Header().Get("Msh-Request-Id"))
	}
}

// Verify an incoming Trigger webhook delivery against the Vault's webhook_secret.
func ExampleVerifyWebhookSignature() {
	err := kha.VerifyWebhookSignature(
		"whsec_...",
		"msg_2x...",
		"1760000000",
		"v1,base64signature...",
		[]byte(`{"type":"trigger.run.completed"}`),
	)
	if verifyErr, ok := errors.AsType[*kha.WebhookVerificationError](err); ok {
		fmt.Println("invalid webhook:", verifyErr.Reason)
	}
}
