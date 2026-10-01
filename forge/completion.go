package forge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/style"
)

// AddCompletionCommand adds cobra's "completion" command to root, plus
// "completion install" and "completion uninstall", which set up completion in
// the user's shell startup file. Call it after the other subcommands are added.
func AddCompletionCommand(root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	completion, _, err := root.Find([]string{"completion"})
	if err != nil || completion == root {
		return
	}
	name := root.Name()
	completion.Long = fmt.Sprintf(`Generate the autocompletion script for %[1]s for the specified shell.

To set up completion for every new shell, run:

  %[1]s completion install

It edits your shell's startup file and is safe to re-run. See each shell's
sub-command help to load the script by hand instead.`, name)
	for _, c := range completion.Commands() {
		if c.Name() == "zsh" {
			c.RunE = func(cmd *cobra.Command, _ []string) error {
				noDesc, _ := cmd.Flags().GetBool("no-descriptions")
				return genZshCompletion(cmd.Root(), cmd.OutOrStdout(), noDesc || cmd.Root().CompletionOptions.DisableDescriptions)
			}
		}
	}
	completion.AddCommand(completionInstallCommand(name), completionUninstallCommand(name))
}

// zshCompinitGuard loads zsh's completion system when the shell hasn't.
// Without it the script fails with "command not found: compdef" for anyone
// whose .zshrc doesn't call compinit (oh-my-zsh and similar call it for you).
const zshCompinitGuard = "(( $+functions[compdef] )) || { autoload -Uz compinit && compinit; }\n"

func genZshCompletion(root *cobra.Command, w io.Writer, noDesc bool) error {
	var buf bytes.Buffer
	gen := root.GenZshCompletion
	if noDesc {
		gen = root.GenZshCompletionNoDesc
	}
	if err := gen(&buf); err != nil {
		return err
	}
	// The guard goes after the "#compdef" line, which must stay first for the
	// script to work when installed in $fpath.
	first, rest, _ := strings.Cut(buf.String(), "\n")
	_, err := io.WriteString(w, first+"\n"+zshCompinitGuard+rest)
	return err
}

var completionShells = []string{"bash", "zsh", "fish"}

func completionInstallCommand(name string) *cobra.Command {
	return &cobra.Command{
		Use:   "install [bash|zsh|fish]",
		Short: "Set up completion for every new shell (safe to re-run)",
		Long: fmt.Sprintf(`Set up %[1]s completion for every new shell. The shell defaults to $SHELL.

For bash and zsh this adds a block that loads "%[1]s completion <shell>" to
your startup file (~/.zshrc, or ~/.bash_profile on macOS and ~/.bashrc
elsewhere). Any earlier setup in that file is removed first, including lines
such as "source <(%[1]s completion zsh)" added by hand, so running it again
never duplicates anything. For fish it writes
~/.config/fish/completions/%[1]s.fish.

Bash completion also needs the bash-completion package
(on macOS: brew install bash-completion@2).`, name),
		Example:               fmt.Sprintf("  %[1]s completion install\n  %[1]s completion install zsh", name),
		Args:                  cobra.MaximumNArgs(1),
		ValidArgs:             completionShells,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			shell, err := completionShell(args)
			if err != nil {
				return err
			}
			path, err := completionTarget(name, shell)
			if err != nil {
				return err
			}
			var replaced bool
			if shell == "fish" {
				var script bytes.Buffer
				if err := cmd.Root().GenFishCompletion(&script, true); err != nil {
					return err
				}
				_, statErr := os.Stat(path)
				replaced = statErr == nil
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := writeFileAtomic(path, script.Bytes()); err != nil {
					return err
				}
			} else {
				replaced, err = editStartupFile(path, name, func(s string) string {
					return appendBlock(s, completionBlock(name, shell))
				})
				if err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			p := style.For(out)
			verb := "Added"
			if replaced {
				verb = "Replaced"
			}
			fmt.Fprintf(out, "%s %s completion in %s\n", verb, shell, p.Bold(tildePath(path)))
			fmt.Fprintf(out, "%s\n", p.Dim(fmt.Sprintf("Open a new terminal (or run \"exec %s\") to use it.", shell)))
			return nil
		},
	}
}

func completionUninstallCommand(name string) *cobra.Command {
	return &cobra.Command{
		Use:                   "uninstall [bash|zsh|fish]",
		Short:                 `Remove the completion setup added by "completion install"`,
		Long:                  fmt.Sprintf("Remove %[1]s completion from your shell's startup file, including lines\nsuch as \"source <(%[1]s completion zsh)\" added by hand. The shell defaults to $SHELL.", name),
		Args:                  cobra.MaximumNArgs(1),
		ValidArgs:             completionShells,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			shell, err := completionShell(args)
			if err != nil {
				return err
			}
			path, err := completionTarget(name, shell)
			if err != nil {
				return err
			}
			var removed bool
			if shell == "fish" {
				err = os.Remove(path)
				removed = err == nil
				if errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			} else {
				removed, err = editStartupFile(path, name, func(s string) string {
					if s = strings.TrimRight(s, "\n"); s != "" {
						s += "\n"
					}
					return s
				})
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if removed {
				fmt.Fprintf(out, "Removed %s completion from %s\n", shell, style.For(out).Bold(tildePath(path)))
			} else {
				fmt.Fprintf(out, "No %s completion setup found in %s\n", shell, tildePath(path))
			}
			return nil
		},
	}
}

func completionShell(args []string) (string, error) {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	} else if s := os.Getenv("SHELL"); s != "" {
		shell = filepath.Base(s)
	}
	for _, s := range completionShells {
		if shell == s {
			return shell, nil
		}
	}
	if len(args) == 0 {
		return "", fmt.Errorf("can't tell your shell from $SHELL (%q); pass one of: %s", os.Getenv("SHELL"), strings.Join(completionShells, ", "))
	}
	return "", fmt.Errorf("unsupported shell %q; pass one of: %s (for powershell, see \"completion powershell --help\")", shell, strings.Join(completionShells, ", "))
}

// completionTarget is the file that "completion install" edits (bash, zsh)
// or writes (fish).
func completionTarget(name, shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch shell {
	case "zsh":
		if dir := os.Getenv("ZDOTDIR"); dir != "" {
			home = dir
		}
		return filepath.Join(home, ".zshrc"), nil
	case "bash":
		// macOS terminals start login shells, which read .bash_profile, not .bashrc.
		if runtime.GOOS == "darwin" {
			return filepath.Join(home, ".bash_profile"), nil
		}
		return filepath.Join(home, ".bashrc"), nil
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "fish", "completions", name+".fish"), nil
}

func completionBlock(name, shell string) string {
	// eval rather than "source <(...)", which silently does nothing in bash 3.2 (macOS).
	load := fmt.Sprintf(`eval "$(%s completion bash)"`, name)
	if shell == "zsh" {
		load = fmt.Sprintf("source <(%s completion zsh)", name)
	}
	return fmt.Sprintf("%s\nif command -v %s >/dev/null 2>&1; then\n  %s\nfi\n%s\n",
		blockStart(name), name, load, blockEnd(name))
}

func blockStart(name string) string { return "# >>> " + name + " completion >>>" }
func blockEnd(name string) string   { return "# <<< " + name + " completion <<<" }

// removeCompletionSetup strips the block written by "completion install" and
// unindented lines that load the completion script directly, such as
// `source <(forge completion zsh)`. Indented lines are left alone, since
// removing one could leave an empty if block behind.
func removeCompletionSetup(s, name string) string {
	n := regexp.QuoteMeta(name)
	block := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(blockStart(name)) + `$.*?^` + regexp.QuoteMeta(blockEnd(name)) + `$\n?`)
	line := regexp.MustCompile(`(?m)^(source|\.|eval) [^\n]*\b` + n + ` completion (bash|zsh)\b[^\n]*\n?`)
	return line.ReplaceAllString(block.ReplaceAllString(s, ""), "")
}

func appendBlock(s, block string) string {
	if s = strings.TrimRight(s, "\n"); s != "" {
		s += "\n\n"
	}
	return s + block
}

// editStartupFile removes any completion setup from the file at path, applies
// then, and writes the result back. It reports whether a setup was removed.
func editStartupFile(path, name string, then func(string) string) (removed bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	cleaned := removeCompletionSetup(string(data), name)
	updated := then(cleaned)
	if updated == string(data) {
		return cleaned != string(data), nil
	}
	return cleaned != string(data), writeFileAtomic(path, []byte(updated))
}

// writeFileAtomic replaces path's contents without leaving a half-written
// file behind. Symlinks (common for dotfiles) are followed, not replaced.
func writeFileAtomic(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return path
}
