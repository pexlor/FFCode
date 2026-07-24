package app

import (
	"MyCode/internal/agent"
	appconfig "MyCode/internal/config"
	contextmanager "MyCode/internal/context"
	session "MyCode/internal/conversation"
	"MyCode/internal/llm"
	"MyCode/internal/storage/fileconversation"
	"context"
	"path/filepath"
)

type runtime struct {
	runner         *agent.Agent
	contextManager *contextmanager.ContextManager
	sessions       *session.Service
	cleanup        func()
}

func bootstrap(ctx context.Context, config appconfig.Config, workspace, systemPrompt string) (*runtime, error) {
	client, err := llm.NewClient(modelParameters(config.Model))
	if err != nil {
		return nil, err
	}

	tools, cleanup, err := createTools(ctx, workspace)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*runtime, error) {
		cleanup()
		return nil, err
	}

	runner, err := agent.NewAgent(ctx, client, tools)
	if err != nil {
		return fail(err)
	}
	store, err := fileconversation.New(filepath.Join(workspace, ".context", "sessions"))
	if err != nil {
		return fail(err)
	}

	var primary contextmanager.Summarizer
	if config.Summary.Model != "" {
		summaryClient, summaryErr := llm.NewClient(&llm.ModelParm{
			Protocol:  config.Model.Protocol,
			BaseURL:   config.Summary.BaseURL,
			APIKey:    config.Summary.APIKey,
			ModelName: config.Summary.Model,
			MaxToken:  int64(config.Model.MaxTokens),
		})
		if summaryErr != nil {
			return fail(summaryErr)
		}
		primary = contextmanager.LLMSummarizer{Client: summaryClient}
	}

	contextManager, err := contextmanager.NewContextManager(contextmanager.ContextManagerConfig{
		Store: store, Estimator: contextmanager.ConservativeEstimator{}, Policy: contextmanager.DefaultPolicy(),
		Model: contextmanager.ModelContextSpec{
			ModelName: config.Model.Name, ContextWindow: config.Context.Window,
			MaxOutputTokens: config.Context.OutputReserve,
		},
		Workspace: workspace,
		Primary:   primary,
		Fallback:  contextmanager.LLMSummarizer{Client: client},
	})
	if err != nil {
		return fail(err)
	}

	sessions, err := session.NewService(store, workspace, systemPrompt)
	if err != nil {
		return fail(err)
	}
	return &runtime{runner: runner, contextManager: contextManager, sessions: sessions, cleanup: cleanup}, nil
}

func modelParameters(model appconfig.ModelConfig) *llm.ModelParm {
	return &llm.ModelParm{
		Protocol:       model.Protocol,
		BaseURL:        model.BaseURL,
		APIKey:         model.APIKey,
		ModelName:      model.Name,
		MaxToken:       int64(model.MaxTokens),
		EnableThinking: model.EnableThinking,
	}
}
