package app

import (
	appconfig "FFCode/internal/config"
	"FFCode/internal/prompt"
	jsonlui "FFCode/internal/ui/jsonl"
	"FFCode/internal/ui/terminal"
	workspacepath "FFCode/internal/workspace"
	"context"
	"fmt"
	"io"
	"os"
)

func Run(arguments []string, stdout, stderr io.Writer, version string) int {
	if len(arguments) > 0 {
		switch arguments[0] {
		case "--help", "-h":
			if err := validateStandaloneOption(arguments, "help"); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 2
			}
			printUsage(stdout)
			return 0
		case "--version", "-v":
			if err := validateStandaloneOption(arguments, "version"); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 2
			}
			fmt.Fprintf(stdout, "FFCode %s\n", version)
			return 0
		}
	}

	options, err := parseOptions(arguments)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		printUsage(stderr)
		return 2
	}
	workspace, err := workspacepath.Current(options.Workspace)
	if err != nil {
		fmt.Fprintf(stderr, "workspace 解析失败: %v\n", err)
		return 1
	}
	config, err := appconfig.Load(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "配置加载失败: %v\n", err)
		return 1
	}
	systemPrompt, err := prompt.BuildSystemPromptForWorkspace(workspace)
	if err != nil {
		fmt.Fprintf(stderr, "消息初始化失败: %v\n", err)
		return 1
	}

	ctx := context.Background()
	runtime, err := bootstrap(ctx, config, workspace, systemPrompt)
	if err != nil {
		fmt.Fprintf(stderr, "应用初始化失败: %v\n", err)
		return 1
	}
	defer runtime.cleanup()

	onSessionChange := func(_ string) {
		runtime.runner.SetContextManager(runtime.contextManager)
	}
	if options.OutputFormat == OutputJSONL {
		err = jsonlui.Run(ctx, jsonlui.Runtime{
			In: os.Stdin, Out: stdout, Runner: runtime.runner, Sessions: runtime.sessions,
			OnSessionChange: onSessionChange,
		})
	} else {
		err = terminal.Run(terminal.Runtime{
			ModelName:       config.Model.Name,
			Workspace:       workspace,
			Runner:          runtime.runner,
			Sessions:        runtime.sessions,
			OnSessionChange: onSessionChange,
		})
	}
	if err != nil {
		fmt.Fprintf(stderr, "终端执行失败: %v\n", err)
		return 1
	}
	return 0
}
