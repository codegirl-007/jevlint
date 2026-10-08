package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	defaultConfigFile = ".jevlint.json"
	legacyConfigFile  = "jevlint.json"
)

// resolveConfigPath maps the --config flag to a config file. When the flag is
// the default, an existing .jevlint.json wins over jevlint.json.
func resolveConfigPath(flagPath string) (string, error) {
	if flagPath != defaultConfigFile {
		return filepath.Abs(flagPath)
	}
	return resolveDefaultConfigPath()
}

// configPathForInit returns where init should write the config file.
func configPathForInit(flagPath string) (string, error) {
	if flagPath != defaultConfigFile {
		return filepath.Abs(flagPath)
	}
	primary, _, err := projectDefaultConfigPaths()
	return primary, err
}

// existingConfigPaths lists config files that already exist for init. For the
// default flag, both .jevlint.json and jevlint.json are checked.
func existingConfigPaths(flagPath string) ([]string, error) {
	if flagPath != defaultConfigFile {
		absolute, err := filepath.Abs(flagPath)
		if err != nil {
			return nil, err
		}
		if exists, err := configFileExists(absolute); err != nil {
			return nil, err
		} else if exists {
			return []string{absolute}, nil
		}
		return nil, nil
	}
	primary, legacy, err := projectDefaultConfigPaths()
	if err != nil {
		return nil, err
	}
	var existing []string
	for _, path := range []string{primary, legacy} {
		ok, err := configFileExists(path)
		if err != nil {
			return nil, err
		}
		if ok {
			existing = append(existing, path)
		}
	}
	return existing, nil
}

// prepareDoctorConfig resolves the config path for doctor and, when the default
// flag is used, renames jevlint.json to .jevlint.json if only the legacy file
// exists.
func prepareDoctorConfig(flagPath string) (absolute, migrationNote string, err error) {
	if flagPath != defaultConfigFile {
		absolute, err = resolveConfigPath(flagPath)
		return absolute, "", err
	}
	primary, legacy, err := projectDefaultConfigPaths()
	if err != nil {
		return "", "", err
	}
	if exists, err := configFileExists(primary); err != nil {
		return "", "", err
	} else if exists {
		return primary, "", nil
	}
	legacyExists, err := configFileExists(legacy)
	if err != nil {
		return "", "", err
	}
	if !legacyExists {
		return primary, "", nil
	}
	if err := os.Rename(legacy, primary); err != nil {
		return "", "", err
	}
	note := fmt.Sprintf("renamed %s to %s", legacyConfigFile, defaultConfigFile)
	return primary, note, nil
}

// resolveDefaultConfigPath picks .jevlint.json, then jevlint.json, else the
// dotfile path when neither exists yet.
func resolveDefaultConfigPath() (string, error) {
	primary, legacy, err := projectDefaultConfigPaths()
	if err != nil {
		return "", err
	}
	if exists, err := configFileExists(primary); err != nil {
		return "", err
	} else if exists {
		return primary, nil
	}
	if exists, err := configFileExists(legacy); err != nil {
		return "", err
	} else if exists {
		return legacy, nil
	}
	return primary, nil
}

func projectDefaultConfigPaths() (primary, legacy string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(cwd, defaultConfigFile), filepath.Join(cwd, legacyConfigFile), nil
}

func configFileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
