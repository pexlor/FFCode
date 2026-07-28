package app

import (
	"MyCode/internal/agent"
	appconfig "MyCode/internal/config"
	contextmanager "MyCode/internal/context"
	session "MyCode/internal/conversation"
	"MyCode/internal/hook"
	"MyCode/internal/llm"
	"MyCode/internal/memory"
	"MyCode/internal/skill"
	"MyCode/internal/storage/filecheckpoint"
	"MyCode/internal/storage/fileconversation"
	filememory "MyCode/internal/storage/filememory"
	"MyCode/internal/subagent"
	"MyCode/internal/tool"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type runtime struct {
	runner         *agent.Agent
	contextManager *contextmanager.ContextManager
	sessions       *session.Service
	cleanup        func()
}

func bootstrap(ctx context.Context, config appconfig.Config, workspace, systemPrompt string) (*runtime, error) {
	hookDispatcher, err := loadHooks(config, workspace)
	if err != nil {
		return nil, err
	}
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
	if err := registerSubagentTool(tools, client, hookDispatcher); err != nil {
		return fail(err)
	}

	runner, err := agent.NewAgent(ctx, client, tools)
	if err != nil {
		return fail(err)
	}
	runner.SetHookDispatcher(hookDispatcher)
	checkpointStore, err := filecheckpoint.New(filepath.Join(workspace, ".context", "checkpoints"))
	if err != nil {
		return fail(err)
	}
	runner.CheckpointStore = checkpointStore
	registry := skill.NewRegistry(defaultSkillSources(workspace))
	if err := registry.Reload(); err != nil {
		return fail(err)
	}
	skillManager := skill.NewManager(registry, func(name string) bool {
		for _, schema := range tools.BuildAllSchemas() {
			if strings.EqualFold(schema.Name, name) {
				return true
			}
		}
		return false
	})
	tools.RegisterTool(&skill.LoadTool{Manager: skillManager})
	runner.SetSkillManager(skillManager)
	store, err := fileconversation.New(filepath.Join(workspace, ".context", "sessions"))
	if err != nil {
		return fail(err)
	}
	memoryRoot := config.Memory.Root
	if strings.TrimSpace(memoryRoot) == "" {
		memoryRoot = ".ffcode/memory"
	}
	if !filepath.IsAbs(memoryRoot) {
		memoryRoot = filepath.Join(workspace, memoryRoot)
	}
	memoryStore, err := filememory.New(memoryRoot)
	if err != nil {
		return fail(err)
	}
	minIdle, err := time.ParseDuration(config.Memory.MinSessionIdle)
	if err != nil {
		return fail(err)
	}
	extractModel := config.Memory.ExtractModel
	if extractModel == "" {
		extractModel = config.Model.Name
	}
	consolidationModel := config.Memory.ConsolidationModel
	if consolidationModel == "" {
		consolidationModel = config.Model.Name
	}
	memoryService := &memory.Service{
		Store: memoryStore, Source: store, OwnerID: "mycode-memory", Workspace: workspace,
		Extractor:    memory.LLMExtractor{Client: client, Model: extractModel, PromptVersion: 1},
		Consolidator: memory.LLMConsolidator{Client: client, Model: consolidationModel, Fallback: memory.DeterministicConsolidator{}},
		MinIdle:      minIdle, MaxSessions: config.Memory.MaxSessionsPerRun, Concurrency: config.Memory.ExtractionConcurrency,
	}
	var memoryCancel func()
	if config.Memory.Generate {
		memoryCancel = memoryService.Start(ctx)
	} else {
		memoryCancel = func() {}
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
		Hooks:     hookDispatcher,
	})
	if err != nil {
		return fail(err)
	}

	sessions, err := session.NewService(store, workspace, session.SessionContext{
		SystemPrompt: systemPrompt,
		Memory:       memoryStore,
		UseMemory:    config.Memory.Use,
		Hooks:        hookDispatcher,
	})
	if err != nil {
		memoryCancel()
		return fail(err)
	}
	return &runtime{runner: runner, contextManager: contextManager, sessions: sessions, cleanup: func() { memoryCancel(); cleanup() }}, nil
}

func registerSubagentTool(tools *tool.ToolsManager, client llm.LLMClient, hooks *hook.Dispatcher) error {
	manager, err := subagent.NewManager(client, hooks, subagent.Config{})
	if err != nil {
		return err
	}
	tools.RegisterTool(subagent.NewDelegateTool(manager))
	return nil
}

func loadHooks(config appconfig.Config, workspace string) (*hook.Dispatcher, error) {
	if !config.Hooks.Enabled && strings.TrimSpace(os.Getenv("MYCODE_HOOK_CONFIG")) == "" {
		return hook.New(hook.DefaultConfig()), nil
	}
	return hook.LoadWorkspace(workspace)
}

func defaultSkillSources(workspace string) []skill.Source {
	sources := []skill.Source{{Scope: skill.Project, Root: filepath.Join(workspace, ".agent", "skills")}}
	if userConfig, err := os.UserConfigDir(); err == nil {
		sources = append(sources, skill.Source{Scope: skill.User, Root: filepath.Join(userConfig, "mycode", "skills")})
	}
	if executable, err := os.Executable(); err == nil {
		sources = append(sources, skill.Source{Scope: skill.Builtin, Root: filepath.Join(filepath.Dir(executable), "skills")})
	}
	return sources
}

func modelParameters(model appconfig.ModelConfig) *llm.ModelParm {
	return &llm.ModelParm{
		Protocol:       model.Protocol,
		BaseURL:        model.BaseURL,
		APIKey:         model.APIKey,
		ModelName:      model.Name,
		MaxToken:       int64(model.MaxTokens),
		EnableThinking: model.EnableThinking,
		ThinkingEffort: llm.ThinkingEffort(model.ThinkingEffort),
		ThinkingBudget: model.ThinkingBudget,
	}
}
