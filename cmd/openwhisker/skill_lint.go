package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/scarletmu/openwhisker/internal/profile"
	schedulerpkg "github.com/scarletmu/openwhisker/internal/scheduler"
)

// runSkillLint implements `openwhisker skill lint <path>`. <path> may be:
//   - a Phase 6 agent Skill directory containing SKILL.md (and optionally SCHEDULE.md)
//   - a SKILL.md file (lifted to its parent dir)
//   - a SCHEDULE.md file (lifted to its parent dir)
//
// Exit codes follow the spec:
//   0 — clean (no errors, no warnings; or warnings without --strict)
//   1 — at least one error
//   2 — only warnings AND --strict was set
func runSkillLint(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("skill lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile (controls read_only_vault_roots)")
	strict := fs.Bool("strict", false, "promote style warnings to a non-zero exit code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker skill lint [--strict] <path>")
	}
	target := fs.Arg(0)
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("stat %q: %w", target, err)
	}
	if !info.IsDir() {
		// File path → use its parent as the Skill directory. Reject anything
		// that isn't SKILL.md / SCHEDULE.md so the user gets a clear pointer.
		base := filepath.Base(target)
		if base != schedulerpkg.SkillFileName && base != schedulerpkg.ScheduleFileName {
			return fmt.Errorf("expected SKILL.md, SCHEDULE.md, or a Skill directory, got %q", target)
		}
		target = filepath.Dir(target)
	}

	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	vaultProfile := profile.NewConfiguredVaultProfile(conventions)

	skill, issues := schedulerpkg.LintAgentSkillDir(target, vaultProfile)
	// Body-has-H1 check: the registry loader does not currently require an
	// H1, but the spec calls for it as a soft check (used as the outbox
	// title fallback). Inline here so the rule lives next to the message.
	if bodyHasNoH1(skill.Body) {
		issues = append(issues, schedulerpkg.LintIssue{
			Severity: "warning",
			Path:     target,
			Message:  "SKILL.md body lacks an H1 heading (used as outbox title fallback)",
		})
	}

	if len(issues) == 0 {
		fmt.Fprintln(stdout, "ok: no issues")
		return nil
	}
	errCount, warnCount := 0, 0
	for _, m := range issues {
		fmt.Fprintf(stdout, "%s: %s: %s\n", m.Severity, m.Path, m.Message)
		switch m.Severity {
		case "error":
			errCount++
		case "warning":
			warnCount++
		}
	}
	fmt.Fprintf(stdout, "summary: %d error(s), %d warning(s)\n", errCount, warnCount)
	if errCount > 0 {
		return &exitErr{code: 1, msg: "lint reported errors"}
	}
	if warnCount > 0 && *strict {
		return &exitErr{code: 2, msg: "lint reported warnings in --strict mode"}
	}
	return nil
}

func bodyHasNoH1(body string) bool {
	if strings.TrimSpace(body) == "" {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			return false
		}
	}
	return true
}

// exitErr lets the lint command surface a specific exit code without
// stomping on the human-readable lint output already written.
type exitErr struct {
	code int
	msg  string
}

func (e *exitErr) Error() string { return e.msg }
func (e *exitErr) ExitCode() int { return e.code }
