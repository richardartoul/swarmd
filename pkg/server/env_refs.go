package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
)

func ValidateReferencedAgentConfigEnv(specs []AgentSpec, lookupEnv func(string) string) error {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	missingByEnv := make(map[string][]string)
	for _, spec := range specs {
		for i, mount := range spec.Mounts {
			envVar := strings.TrimSpace(mount.Source.EnvVar)
			if envVar == "" || strings.TrimSpace(lookupEnv(envVar)) != "" {
				continue
			}
			location := fmt.Sprintf("%s/%s mounts[%d]", spec.NamespaceID, spec.AgentID, i)
			missingByEnv[envVar] = toolscommon.AppendUniqueString(missingByEnv[envVar], location)
		}
		for i, header := range spec.HTTP.Headers {
			envVar := strings.TrimSpace(header.EnvVar)
			if envVar == "" || strings.TrimSpace(lookupEnv(envVar)) != "" {
				continue
			}
			location := fmt.Sprintf("%s/%s http.headers[%d]", spec.NamespaceID, spec.AgentID, i)
			missingByEnv[envVar] = toolscommon.AppendUniqueString(missingByEnv[envVar], location)
		}
	}
	if len(missingByEnv) == 0 {
		return nil
	}
	envVars := make([]string, 0, len(missingByEnv))
	for envVar := range missingByEnv {
		envVars = append(envVars, envVar)
	}
	sort.Strings(envVars)
	parts := make([]string, 0, len(envVars))
	for _, envVar := range envVars {
		parts = append(parts, fmt.Sprintf("%s referenced by %s", envVar, strings.Join(missingByEnv[envVar], ", ")))
	}
	return fmt.Errorf("missing required environment variables for agent config references: %s", strings.Join(parts, "; "))
}

func managedAgentRuntimeEnvHash(configJSON string, lookupEnv func(string) string) string {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return "decode_error:" + err.Error()
	}
	envVars := managedAgentRuntimeEnvVars(config)
	if len(envVars) == 0 {
		return ""
	}
	hasher := sha256.New()
	for _, envVar := range envVars {
		_, _ = hasher.Write([]byte(envVar))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(lookupEnv(envVar)))
		_, _ = hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func managedAgentRuntimeEnvVars(config managedAgentRuntimeConfig) []string {
	seen := make(map[string]struct{})
	var envVars []string
	for _, mount := range config.Mounts {
		envVar := strings.TrimSpace(mount.Source.EnvVar)
		if envVar == "" {
			continue
		}
		if _, ok := seen[envVar]; ok {
			continue
		}
		seen[envVar] = struct{}{}
		envVars = append(envVars, envVar)
	}
	for _, envVar := range httpHeaderEnvVars(config.HTTP.Headers) {
		if _, ok := seen[envVar]; ok {
			continue
		}
		seen[envVar] = struct{}{}
		envVars = append(envVars, envVar)
	}
	sort.Strings(envVars)
	return envVars
}
