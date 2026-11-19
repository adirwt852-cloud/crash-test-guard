package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/adirwt852-cloud/crash-test-guard/internal/config"
	"github.com/adirwt852-cloud/crash-test-guard/internal/executor"
	"github.com/adirwt852-cloud/crash-test-guard/internal/orchestrator"
	"github.com/adirwt852-cloud/crash-test-guard/internal/protocol"
	"github.com/adirwt852-cloud/crash-test-guard/internal/reporter"
	"github.com/spf13/cobra"
)

var version = "dev"

var exitCode int

func main() {
	rootCmd := &cobra.Command{
		Use:   "crash-test-guard",
		Short: "CI/CD reliability gate for stateful AI agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true

	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(testCmd())
	rootCmd.AddCommand(completionCmd(rootCmd))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(orchestrator.ExitError)
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version)
		},
	}
}

func completionCmd(rootCmd *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:                   "completion [bash|zsh|fish|powershell]",
		Short:                 "Generate the autocompletion script for the specified shell",
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
}

func testCmd() *cobra.Command {
	var (
		configPath   string
		reportFormat string
		verbose      bool
	)

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run crash test scenarios",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			if reportFormat != "" {
				format := strings.ToLower(strings.TrimSpace(reportFormat))
				switch format {
				case "junit", "json", "human":
					cfg.Reporting.Format = format
				default:
					return fmt.Errorf("invalid --report-format %q (expected junit, json, or human)", reportFormat)
				}
			}
			if cmd.Flags().Changed("verbose") {
				cfg.Reporting.Verbose = verbose
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			var ex executor.Executor
			switch {
			case cfg.Agent.KubernetesEnabled():
				ex = executor.NewKubernetesExecutor(
					executor.WithKubeconfig(cfg.Agent.Kubeconfig),
					executor.WithKubernetesNamespace(cfg.Agent.Namespace),
					executor.WithPodName(cfg.Agent.PodName),
					executor.WithLabelSelector(cfg.Agent.LabelSelector),
					executor.WithKubernetesCrashType(executor.CrashPodDelete),
				)
			case strings.TrimSpace(cfg.Agent.Image) != "":
				ex = executor.NewDockerExecutor(
					executor.WithCrashType(executor.CrashContainerRestart),
				)
			default:
				ex = executor.NewLocalExecutor()
			}
			defer ex.Cleanup(context.Background())

			client := protocol.NewClient(protocol.ClientConfig{
				BaseURL:        cfg.Agent.Endpoint,
				ConnectTimeout: cfg.Agent.StartupTimeout.Duration,
				RequestTimeout: cfg.Agent.ShutdownTimeout.Duration,
			})

			engine := orchestrator.NewEngine(cfg, ex, client)
			result, code := engine.Run(ctx)
			exitCode = code

			if err := writeReport(cfg.Reporting, result); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "path to crash-test configuration file (required)")
	cmd.Flags().StringVar(&reportFormat, "report-format", "", "report format: junit, json, or human (overrides config)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose report output")
	_ = cmd.MarkFlagRequired("config")

	return cmd
}

func writeReport(cfg config.ReportingConfig, result *orchestrator.TestResult) error {
	switch cfg.Format {
	case "junit":
		r, err := reporter.NewJUnit(cfg)
		if err != nil {
			return err
		}
		defer r.Close()
		return r.Report(result)
	case "json":
		return writeJSONReport(cfg, result)
	default:
		r, err := reporter.New(cfg)
		if err != nil {
			return err
		}
		defer r.Close()
		return r.Report(result)
	}
}

type jsonReport struct {
	Scenarios  []jsonScenario `json:"scenarios"`
	Overall    string         `json:"overall"`
	DurationMS int64          `json:"duration_ms"`
	Config     string         `json:"config,omitempty"`
}

type jsonScenario struct {
	Scenario   string `json:"scenario"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func writeJSONReport(cfg config.ReportingConfig, result *orchestrator.TestResult) error {
	if result == nil {
		result = &orchestrator.TestResult{Overall: orchestrator.StatusError}
	}

	report := jsonReport{
		Overall:    string(result.Overall),
		DurationMS: result.Duration.Milliseconds(),
		Config:     result.Config,
	}
	for _, sc := range result.Scenarios {
		entry := jsonScenario{
			Scenario:   sc.Scenario,
			Type:       sc.Type,
			Status:     string(sc.Status),
			DurationMS: sc.Duration.Milliseconds(),
		}
		if sc.Error != nil {
			entry.Error = sc.Error.Error()
		}
		report.Scenarios = append(report.Scenarios, entry)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json report: %w", err)
	}
	data = append(data, '\n')

	if cfg.Output == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("write json report: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(cfg.Output, data, 0o644); err != nil {
		return fmt.Errorf("write json report %q: %w", cfg.Output, err)
	}
	return nil
}
