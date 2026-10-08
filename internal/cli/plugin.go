package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/packs"
)

func executePlugin(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	userCacheDir func() (string, error),
) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, pluginUsage())
		return exitUsageError
	}
	switch args[0] {
	case "init":
		return pluginInit(args[1:], stdout, stderr)
	case "install":
		return pluginInstall(args[1:], stdout, stderr, userCacheDir)
	case "list":
		return pluginList(args[1:], stdout, stderr, userCacheDir)
	case "update":
		return pluginUpdate(args[1:], stdout, stderr, userCacheDir)
	case "remove":
		return pluginRemove(args[1:], stdout, stderr)
	case "-h", "--help":
		fmt.Fprint(stderr, pluginUsage())
		return exitSuccess
	default:
		fmt.Fprintf(stderr, "jevlint: unknown plugin command %q\n\n%s", args[0], pluginUsage())
		return exitUsageError
	}
}

func pluginInstall(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	userCacheDir func() (string, error),
) int {
	configPath, specs, exitCode := parsePluginArgs(args, 1, stderr)
	if exitCode != 0 {
		return exitCode
	}
	if len(specs) != 1 {
		fmt.Fprintln(stderr, "jevlint: plugin install requires a pack source")
		return exitUsageError
	}
	cfg, absolute, exitCode := loadProjectFile(configPath, stderr)
	if exitCode != 0 {
		return exitCode
	}
	ref, err := packs.Install(specs[0], userCacheDir)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	cfg.Packs = upsertPack(cfg.Packs, ref)
	if err := config.Write(absolute, cfg); err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	fmt.Fprintf(stdout, "installed %s@%s\n", ref.ID, ref.SHA)
	return exitSuccess
}

func pluginList(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	userCacheDir func() (string, error),
) int {
	configPath, leftover, exitCode := parsePluginArgs(args, 0, stderr)
	if exitCode != 0 {
		return exitCode
	}
	if len(leftover) > 0 {
		fmt.Fprintln(stderr, "jevlint: plugin list does not take arguments")
		return exitUsageError
	}
	cfg, _, exitCode := loadProjectFile(configPath, stderr)
	if exitCode != 0 {
		return exitCode
	}
	userCache, err := userCacheDir()
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	if len(cfg.Packs) == 0 {
		fmt.Fprintln(stdout, "no packs")
		return exitSuccess
	}
	for _, ref := range cfg.Packs {
		cacheDir, err := packs.CacheDir(userCache, ref.SHA, ref.ID)
		if err != nil {
			fmt.Fprintf(stderr, "jevlint: %v\n", err)
			return exitUsageError
		}
		status := "missing"
		if _, statErr := os.Stat(filepath.Join(cacheDir, packs.ManifestFile)); statErr == nil {
			status = "cached"
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %s\n", ref.ID, ref.SHA, status, ref.Source)
	}
	return exitSuccess
}

func pluginUpdate(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	userCacheDir func() (string, error),
) int {
	configPath, leftover, exitCode := parsePluginArgs(args, 0, stderr)
	if exitCode != 0 {
		return exitCode
	}
	cfg, absolute, exitCode := loadProjectFile(configPath, stderr)
	if exitCode != 0 {
		return exitCode
	}
	ids := leftover
	if len(ids) == 0 {
		for _, ref := range cfg.Packs {
			ids = append(ids, ref.ID)
		}
	}
	for _, id := range ids {
		ref, ok := packByID(cfg.Packs, id)
		if !ok {
			fmt.Fprintf(stderr, "jevlint: unknown pack %q\n", id)
			return exitUsageError
		}
		spec := ref.Source
		if ref.Ref != "" {
			spec += "@" + ref.Ref
		}
		if ref.Path != "" {
			spec += "#" + ref.Path
		}
		updated, err := packs.Install(spec, userCacheDir)
		if err != nil {
			fmt.Fprintf(stderr, "jevlint: %v\n", err)
			return exitUsageError
		}
		if updated.ID != ref.ID {
			fmt.Fprintf(
				stderr,
				"jevlint: pack %q changed its id to %q; refusing to update\n",
				ref.ID,
				updated.ID,
			)
			return exitUsageError
		}
		cfg.Packs = upsertPack(cfg.Packs, updated)
		fmt.Fprintf(stdout, "updated %s@%s\n", updated.ID, updated.SHA)
	}
	if err := config.Write(absolute, cfg); err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	return exitSuccess
}

func pluginRemove(args []string, stdout io.Writer, stderr io.Writer) int {
	configPath, leftover, exitCode := parsePluginArgs(args, 1, stderr)
	if exitCode != 0 {
		return exitCode
	}
	if len(leftover) != 1 {
		fmt.Fprintln(stderr, "jevlint: plugin remove requires a pack id")
		return exitUsageError
	}
	cfg, absolute, exitCode := loadProjectFile(configPath, stderr)
	if exitCode != 0 {
		return exitCode
	}
	next := make([]config.PackRef, 0, len(cfg.Packs))
	found := false
	for _, ref := range cfg.Packs {
		if ref.ID == leftover[0] {
			found = true
			continue
		}
		next = append(next, ref)
	}
	if !found {
		fmt.Fprintf(stderr, "jevlint: unknown pack %q\n", leftover[0])
		return exitUsageError
	}
	cfg.Packs = next
	if err := config.Write(absolute, cfg); err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	fmt.Fprintf(stdout, "removed %s\n", leftover[0])
	return exitSuccess
}

func pluginInit(args []string, stdout io.Writer, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprint(stderr, pluginUsage())
			return exitSuccess
		}
	}
	options, exitCode := parsePluginInitArgs(args, stderr)
	if exitCode != exitSuccess {
		return exitCode
	}
	dir, created, err := scaffoldPack(options)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return exitUsageError
	}
	fmt.Fprintf(stdout, "created pack %s in %s\n", options.id, dir)
	for _, path := range created {
		fmt.Fprintf(stdout, "  %s\n", path)
	}
	fmt.Fprintf(
		stdout,
		"\nNext: commit the pack to a git repository and install it from your project:\n"+
			"  jevlint plugin install <git-url-or-path>#<path-to-pack>\n",
	)
	return exitSuccess
}

func parsePluginInitArgs(args []string, stderr io.Writer) (pluginInitOptions, int) {
	options := pluginInitOptions{languages: []string{"go"}}
	positional := make([]string, 0, 2)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--force":
			options.force = true
		case arg == "--dir":
			if index+1 >= len(args) {
				fmt.Fprintln(stderr, "jevlint: --dir requires a path")
				return pluginInitOptions{}, exitUsageError
			}
			index++
			options.dir = args[index]
		case strings.HasPrefix(arg, "--dir="):
			options.dir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--languages":
			if index+1 >= len(args) {
				fmt.Fprintln(stderr, "jevlint: --languages requires a value")
				return pluginInitOptions{}, exitUsageError
			}
			index++
			languages, err := splitLanguages(args[index])
			if err != nil {
				fmt.Fprintf(stderr, "jevlint: %v\n", err)
				return pluginInitOptions{}, exitUsageError
			}
			options.languages = languages
		case strings.HasPrefix(arg, "--languages="):
			languages, err := splitLanguages(strings.TrimPrefix(arg, "--languages="))
			if err != nil {
				fmt.Fprintf(stderr, "jevlint: %v\n", err)
				return pluginInitOptions{}, exitUsageError
			}
			options.languages = languages
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < 1 || len(positional) > 2 {
		fmt.Fprint(stderr, pluginUsage())
		return pluginInitOptions{}, exitUsageError
	}
	options.id = positional[0]
	if len(positional) == 2 {
		options.dir = positional[1]
	}
	return options, exitSuccess
}

func splitLanguages(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	languages := make([]string, 0, len(parts))
	for _, part := range parts {
		language := strings.TrimSpace(part)
		if language == "" {
			continue
		}
		languages = append(languages, language)
	}
	if len(languages) == 0 {
		return nil, fmt.Errorf("--languages cannot be empty")
	}
	return languages, nil
}

func parsePluginArgs(args []string, minPositional int, stderr io.Writer) (string, []string, int) {
	configPath := defaultConfigFile
	positional := make([]string, 0)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--config":
			if index+1 >= len(args) {
				fmt.Fprintln(stderr, "jevlint: --config requires a path")
				return "", nil, exitUsageError
			}
			index++
			configPath = args[index]
		case len(arg) > 9 && arg[:9] == "--config=":
			configPath = arg[9:]
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < minPositional {
		fmt.Fprint(stderr, pluginUsage())
		return "", nil, exitUsageError
	}
	return configPath, positional, exitSuccess
}

func loadProjectFile(configPath string, stderr io.Writer) (config.Config, string, int) {
	absolute, err := resolveConfigPath(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: resolve config path: %v\n", err)
		return config.Config{}, "", exitUsageError
	}
	cfg, err := config.Load(absolute)
	if err != nil {
		fmt.Fprintf(stderr, "jevlint: %v\n", err)
		return config.Config{}, "", exitUsageError
	}
	return cfg, absolute, exitSuccess
}

func upsertPack(refs []config.PackRef, next config.PackRef) []config.PackRef {
	for index, ref := range refs {
		if ref.ID == next.ID {
			refs[index] = next
			return refs
		}
	}
	return append(refs, next)
}

func packByID(refs []config.PackRef, id string) (config.PackRef, bool) {
	for _, ref := range refs {
		if ref.ID == id {
			return ref, true
		}
	}
	return config.PackRef{}, false
}
