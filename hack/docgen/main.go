package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/t4db/t4/internal/cli"
)

const (
	envBlockStart = "<!-- BEGIN GENERATED: cli-env-vars -->"
	envBlockEnd   = "<!-- END GENERATED: cli-env-vars -->"
)

type envRef struct {
	Command string
	Env     string
	Flag    string
}

func main() {
	block := renderEnvReference(cli.NewRootCmd())
	for _, path := range []string{
		"docs/configuration.md",
		"website/src/content/docs/configuration.md",
	} {
		if err := replaceGeneratedBlock(path, envBlockStart, envBlockEnd, block); err != nil {
			fmt.Fprintf(os.Stderr, "generate configuration docs: %v\n", err)
			os.Exit(1)
		}
	}
}

func renderEnvReference(root *cobra.Command) string {
	refs := collectEnvRefs(root)
	var b bytes.Buffer
	b.WriteString(envBlockStart)
	b.WriteString("\n")
	b.WriteString("| Command | Environment variable | Equivalent flag |\n")
	b.WriteString("|---------|----------------------|-----------------|\n")
	for _, ref := range refs {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n", ref.Command, ref.Env, ref.Flag)
	}
	b.WriteString(envBlockEnd)
	b.WriteString("\n")
	return b.String()
}

func collectEnvRefs(root *cobra.Command) []envRef {
	envPattern := regexp.MustCompile(`\(env: (T4_[A-Z0-9_]+)\)`)
	var refs []envRef

	var walk func(*cobra.Command, []string)
	walk = func(cmd *cobra.Command, path []string) {
		cmdPath := strings.Join(append(path, cmd.Name()), " ")
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			m := envPattern.FindStringSubmatch(flag.Usage)
			if m == nil {
				return
			}
			refs = append(refs, envRef{
				Command: cmdPath,
				Env:     m[1],
				Flag:    "--" + flag.Name,
			})
		})
		for _, child := range cmd.Commands() {
			walk(child, append(path, cmd.Name()))
		}
	}
	walk(root, nil)

	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Command != refs[j].Command {
			return refs[i].Command < refs[j].Command
		}
		if refs[i].Env != refs[j].Env {
			return refs[i].Env < refs[j].Env
		}
		return refs[i].Flag < refs[j].Flag
	})
	return refs
}

func replaceGeneratedBlock(path, start, end, replacement string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(body)
	startIdx := strings.Index(text, start)
	if startIdx < 0 {
		return fmt.Errorf("%s: missing %q marker", path, start)
	}
	endIdx := strings.Index(text[startIdx:], end)
	if endIdx < 0 {
		return fmt.Errorf("%s: missing %q marker", path, end)
	}
	endIdx += startIdx + len(end)
	if endIdx < len(text) && text[endIdx] == '\n' {
		endIdx++
	}
	next := text[:startIdx] + replacement + text[endIdx:]
	if next == text {
		return nil
	}
	return os.WriteFile(path, []byte(next), 0o644)
}
