package cmd

import (
	"fmt"
	"os"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	publishRegistry       string
	publishBranch         string
	publishTag            string
	publishMessage        string
	publishAuthor         string
	publishEmail          string
	publishDryRun         bool
	publishNoCreateBranch bool
	publishNoScan         bool
)

var publishCmd = &cobra.Command{
	Use:   "publish [path]",
	Short: "Publish a local skill to a registry",
	Long: `Clone the target registry into a temp directory, copy the local skill into
skills/<name>/, commit and push. Validates the skill before touching the registry.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPublish,
}

func init() {
	publishCmd.Flags().StringVar(&publishRegistry, "registry", "", "target registry (defaults to default_registry config)")
	publishCmd.Flags().StringVar(&publishBranch, "branch", "", "target branch (defaults to registry default)")
	publishCmd.Flags().StringVar(&publishTag, "tag", "", "annotated tag to create on the new commit (e.g. v1.2.0)")
	publishCmd.Flags().StringVarP(&publishMessage, "message", "m", "", "commit message")
	publishCmd.Flags().StringVar(&publishAuthor, "author", "", "commit author name")
	publishCmd.Flags().StringVar(&publishEmail, "email", "", "commit author email")
	publishCmd.Flags().BoolVar(&publishDryRun, "dry-run", false, "validate and stage without pushing")
	publishCmd.Flags().BoolVar(&publishNoCreateBranch, "no-create-branch", false, "refuse to create --branch if it doesn't already exist on origin")
	publishCmd.Flags().BoolVar(&publishNoScan, "no-scan", false, "skip the security scan that normally gates publishes (override security.scan_on_install)")
	rootCmd.AddCommand(publishCmd)
}

func runPublish(cmd *cobra.Command, args []string) error {
	path := "."
	if len(args) == 1 {
		path = args[0]
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	// Security gate. Scan the local skill BEFORE we touch the registry so a
	// blocked publish never leaves a partially-staged clone behind. Runs in
	// dry-run too — the gate is part of publishability, not push-side.
	cfg, cerr := config.Load()
	if cerr == nil {
		// Resolve the path to a skill dir using the publisher's own discovery
		// rules so `qvr publish .` from a parent dir of a single-skill repo
		// still scans the right tree.
		scanPath, _, derr := resolveSkillDir(path)
		if derr != nil || scanPath == "" {
			scanPath = path
		}
		gate, gerr := ScanAndGate(cmd.Context(), scanPath, cfg, scanGateOptions{
			Disabled: publishNoScan,
			Action:   "publish",
			Subject:  scanPath,
		})
		if gerr != nil {
			printer.Warning(fmt.Sprintf("publish: scan failed (%v); proceeding — rerun `qvr scan %s` to retry", gerr, scanPath))
		} else if gate.Blocked {
			return fmt.Errorf("publish: scan blocked (max severity %s ≥ threshold %s); upstream not touched — see findings above or pass --no-scan to override",
				gate.Result.Summary.MaxSeverity(), gate.Threshold)
		}
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	result, err := p.Publish(cmd.Context(), skill.PublishRequest{
		LocalPath:      path,
		Registry:       publishRegistry,
		Branch:         publishBranch,
		Tag:            publishTag,
		Message:        publishMessage,
		Author:         publishAuthor,
		AuthorEmail:    publishEmail,
		DryRun:         publishDryRun,
		NoCreateBranch: publishNoCreateBranch,
	})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}
	if result.DryRun {
		tagSuffix := ""
		if result.Tag != "" {
			tagSuffix = fmt.Sprintf(" (tag %s)", result.Tag)
		}
		printer.Info(fmt.Sprintf("Dry run OK: %s would be published to %s@%s%s", result.Skill, result.Registry, result.Branch, tagSuffix))
		return nil
	}
	shortCommit := result.Commit
	if len(shortCommit) >= 7 {
		shortCommit = shortCommit[:7]
	} else if shortCommit == "" {
		shortCommit = "<unknown>"
	}
	msg := fmt.Sprintf("Published %s to %s@%s (%s)", result.Skill, result.Registry, result.Branch, shortCommit)
	if result.Tag != "" {
		msg += fmt.Sprintf(", tagged %s", result.Tag)
	}
	printer.Success(msg)
	return nil
}
