package command

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cadenya/cadenya-config-loader/internal/config"
	"github.com/cadenya/cadenya-config-loader/internal/reconcile"
	"github.com/urfave/cli/v3"
	cadenya "go.cadenya.com/cadenya-go"
)

// New constructs a fresh command, including fresh flag state, for each invocation.
func New(version string, out, errOut io.Writer) *cli.Command {
	return &cli.Command{
		Name: "cadenya-config", Usage: "Reconcile a Cadenya workspace from source-controlled YAML", Version: version,
		Writer: out, ErrWriter: errOut,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "directory", Aliases: []string{"C"}, Value: ".", Usage: "Repository root containing cadenya.yaml and .cadenya"},
			&cli.StringFlag{Name: "config", Usage: "Settings file, relative to --directory (default: cadenya.yaml if present)"},
			&cli.StringFlag{Name: "resource-dir", Usage: "Resource directory, relative to --directory (default: .cadenya)"},
			&cli.StringFlag{Name: "bundle-key", Sources: cli.EnvVars("CADENYA_BUNDLE_KEY"), Usage: "Stable ownership label shared by resources in this bundle"},
			&cli.StringFlag{Name: "workspace-id", Sources: cli.EnvVars("CADENYA_WORKSPACE_ID"), Usage: "Workspace ID or alias, e.g. development"},
			&cli.StringFlag{Name: "base-url", Sources: cli.EnvVars("CADENYA_BASE_URL"), Usage: "API origin (default: https://api.cadenya.com; omit /v1)"},
			&cli.StringFlag{Name: "api-key", Sources: cli.EnvVars("CADENYA_API_KEY"), HideDefault: true, Usage: "API key; prefer CADENYA_API_KEY"},
			&cli.StringFlag{Name: "output", Value: "text", Usage: "Output format: text or json"},
			&cli.DurationFlag{Name: "timeout", Value: 60 * time.Second, Usage: "Timeout for each HTTP request"},
			&cli.DurationFlag{Name: "operation-timeout", Value: 10 * time.Minute, Usage: "Deadline for the complete plan or apply"},
			&cli.IntFlag{Name: "retries", Value: 2, Usage: "Retries for idempotent API requests (0–10); creates and updates are not retried"},
			&cli.StringFlag{Name: "report-file", Usage: "Write a JSON result atomically to this path, including failures"},
			&cli.StringFlag{Name: "log-level", Value: "info", Sources: cli.EnvVars("CADENYA_LOG_LEVEL"), Usage: "Log level on stderr: debug, info, warn, or error"},
			&cli.StringFlag{Name: "log-format", Value: "text", Sources: cli.EnvVars("CADENYA_LOG_FORMAT"), Usage: "Log format on stderr: text or json"},
		},
		// main owns the exit code. Without this, urfave/cli calls os.Exit itself
		// for some errors (an unknown help topic exits 3).
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		OnUsageError:   usageErrorHandler,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return usagef("unknown command %q (expected validate, plan, or apply)", cmd.Args().First())
			}
			return cli.ShowRootCommandHelp(cmd)
		},
		Commands: []*cli.Command{
			{Name: "validate", Usage: "Validate local YAML and references without API access", Action: run, OnUsageError: usageErrorHandler},
			{Name: "plan", Usage: "Read remote resources and print the proposed operations", Action: run, OnUsageError: usageErrorHandler},
			{Name: "apply", Usage: "Delete missing bundle resources, then create or update local resources", Flags: []cli.Flag{
				&cli.BoolFlag{Name: "dry-run", Usage: "Print the plan without writing to the API"},
				&cli.BoolFlag{Name: "allow-empty", Usage: "Allow an empty resource directory to delete the entire bundle"},
			}, Action: run, OnUsageError: usageErrorHandler},
		},
	}
}

// usageErrorHandler marks flag parsing errors so they exit ExitUsage.
func usageErrorHandler(_ context.Context, _ *cli.Command, err error, _ bool) error {
	return usageError{err}
}

func execute(ctx context.Context, cmd *cli.Command, result *Report) error {
	log, err := newLogger(cmd.String("log-level"), cmd.String("log-format"), cmd.Root().ErrWriter)
	if err != nil {
		return err
	}
	log = log.With("command", cmd.Name)
	if cmd.Args().Len() != 0 {
		return usagef("unexpected positional arguments: %s", cmd.Args().First())
	}
	if cmd.Duration("timeout") <= 0 || cmd.Duration("operation-timeout") <= 0 {
		return usagef("--timeout and --operation-timeout must be positive")
	}
	if cmd.Int("retries") < 0 || cmd.Int("retries") > 10 {
		return usagef("--retries must be between 0 and 10")
	}
	ctx, cancel := context.WithTimeout(ctx, cmd.Duration("operation-timeout"))
	defer cancel()
	root, err := filepath.Abs(cmd.String("directory"))
	if err != nil {
		return err
	}
	settingsPath := cmd.String("config")
	if settingsPath == "" {
		settingsPath = "cadenya.yaml"
	}
	if !filepath.IsAbs(settingsPath) {
		settingsPath = filepath.Join(root, settingsPath)
	}
	s, err := config.ReadSettings(settingsPath, !cmd.IsSet("config"))
	if err != nil {
		return err
	}
	for _, field := range []struct {
		flag   string
		target *string
	}{{"bundle-key", &s.BundleKey}, {"workspace-id", &s.WorkspaceID}, {"base-url", &s.BaseURL}, {"resource-dir", &s.ResourceDir}} {
		if cmd.IsSet(field.flag) {
			*field.target = cmd.String(field.flag)
		}
	}
	if s.ResourceDir == "" {
		s.ResourceDir = ".cadenya"
	}
	if s.BaseURL == "" {
		s.BaseURL = "https://api.cadenya.com"
	}
	if !filepath.IsAbs(s.ResourceDir) {
		s.ResourceDir = filepath.Join(root, s.ResourceDir)
	}
	if err := config.ValidateBundleKey(s.BundleKey); err != nil {
		return usagef("set bundleKey in cadenya.yaml or --bundle-key: %w", err)
	}
	b, err := config.Load(s.ResourceDir, s.BundleKey)
	if err != nil {
		return err
	}
	log.Info("loaded bundle", "bundle", s.BundleKey, "resources", len(b.Resources), "dir", s.ResourceDir)
	if cmd.Name == "validate" {
		valid, count := true, len(b.Resources)
		result.Valid, result.Resources = &valid, &count
		result.Plan = &reconcile.Plan{BundleKey: s.BundleKey, Operations: []*reconcile.Operation{}}
		return nil
	}
	if strings.TrimSpace(s.WorkspaceID) == "" {
		return usagef("set workspaceId in cadenya.yaml, --workspace-id, or CADENYA_WORKSPACE_ID")
	}
	if strings.TrimSpace(cmd.String("api-key")) == "" {
		return usagef("set CADENYA_API_KEY or --api-key for plan/apply")
	}
	if len(b.Resources) == 0 && cmd.Name == "apply" && !cmd.Bool("dry-run") && !cmd.Bool("allow-empty") {
		return usagef("bundle is empty; use --allow-empty to delete all resources with bundle_key=%s", s.BundleKey)
	}
	client, err := cadenya.NewClient(cadenya.WithAPIKey(cmd.String("api-key")), cadenya.WithBaseURL(s.BaseURL), cadenya.WithWorkspaceID(s.WorkspaceID), cadenya.WithHTTPClient(&http.Client{Timeout: cmd.Duration("timeout")}), cadenya.WithMaxRetries(int(cmd.Int("retries"))))
	if err != nil {
		return err
	}
	log.Debug("connecting to Cadenya", "base_url", s.BaseURL, "workspace", s.WorkspaceID)
	plan, err := reconcile.Build(ctx, client, b, s.BundleKey, s.WorkspaceID, log)
	if err != nil {
		return err
	}
	result.Plan = plan
	if cmd.Name == "apply" && !cmd.Bool("dry-run") {
		return plan.Apply(ctx)
	}
	if cmd.Name == "apply" {
		log.Info("dry run; nothing was written", "bundle", s.BundleKey)
	}
	return nil
}
