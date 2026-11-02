package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/sensahin/sheaf/internal/config"
)

const envUsage = `usage:
  sheaf env set KEY [VALUE] [--agent A]   without VALUE, type or pipe it in
  sheaf env ls [--agent A]                names only; values stay hidden
  sheaf env rm KEY [--agent A]
  sheaf env path [--agent A]`

// cmdEnv manages the credentials Sheaf adds to the agents it starts.
func cmdEnv(paths config.Paths, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", envUsage)
	}
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	agent := fs.String("agent", "", "only for this agent (default: every agent)")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	file := paths.EnvPath(*agent)
	switch args[0] {
	case "path":
		fmt.Println(file)
		return nil
	case "ls", "list":
		vars, err := config.ReadEnv(file)
		if err != nil {
			return err
		}
		if len(vars) == 0 {
			fmt.Printf("No variables in %s.\n", file)
			return nil
		}
		w := table()
		fmt.Fprintln(w, "KEY\tVALUE")
		for _, v := range vars {
			fmt.Fprintf(w, "%s\t%s\n", v.Key, mask(v.Value))
		}
		return w.Flush()
	case "set":
		if len(pos) < 1 {
			return fmt.Errorf("%s", envUsage)
		}
		key, value := pos[0], ""
		if k, v, ok := strings.Cut(key, "="); ok {
			key, value = k, v
		} else if len(pos) > 1 {
			value = strings.Join(pos[1:], " ")
		} else if value, err = readSecret(key); err != nil {
			return err
		}
		if err := config.SetEnv(file, key, value, false); err != nil {
			return err
		}
		fmt.Printf("Saved %s in %s. Agents started from now on get it; restart running agents to pick it up.\n", key, file)
		return nil
	case "rm", "remove", "unset":
		if len(pos) < 1 {
			return fmt.Errorf("%s", envUsage)
		}
		return config.SetEnv(file, pos[0], "", true)
	}
	return fmt.Errorf("unknown env command %q\n%s", args[0], envUsage)
}

// readSecret reads a value without echoing it when stdin is a terminal, so
// tokens stay out of shell history; otherwise it reads one line from stdin.
func readSecret(key string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "%s: ", key)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no value for %s on stdin", key)
	}
	return strings.TrimSpace(line), nil
}

func mask(v string) string {
	switch {
	case v == "":
		return "(empty)"
	case len(v) <= 8:
		return "****"
	}
	return v[:4] + "…" + fmt.Sprintf(" (%d chars)", len(v))
}
