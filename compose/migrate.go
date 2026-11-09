package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joho/godotenv"
)

// MigrateEnv applies instance configuration migrations atomically.
func MigrateEnv(dataDir string) error {
	path := filepath.Join(dataDir, ".env")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	env, err := godotenv.Unmarshal(string(body))
	if err != nil {
		return err
	}
	if _, exists := env["MAX_CONCURRENT_CONTAINERS"]; !exists {
		return nil
	}
	if _, exists := env["MAX_CONCURRENT_AGENTS"]; exists {
		return fmt.Errorf("%s contains both agent limit keys; keep only MAX_CONCURRENT_AGENTS", path)
	}
	key := regexp.MustCompile(`(?m)^[\t ]*(export[\t ]+)?MAX_CONCURRENT_CONTAINERS[\t ]*=`)
	matches := key.FindAllIndex(body, -1)
	if len(matches) != 1 {
		return fmt.Errorf("%s must contain one MAX_CONCURRENT_CONTAINERS assignment", path)
	}
	match := matches[0]
	renamed := strings.Replace(string(body[match[0]:match[1]]), "MAX_CONCURRENT_CONTAINERS", "MAX_CONCURRENT_AGENTS", 1)
	return writeEnvAtomic(dataDir, path, string(body[:match[0]])+renamed+string(body[match[1]:]))
}
