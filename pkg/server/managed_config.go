// managed_config.go decodes the per-agent runtime configuration that the
// spec sync embeds in the store (tools, mounts, network, filesystem kind,
// capabilities).

package server

import (
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

const (
	defaultAgentMemoryDir            = ".memory"
	defaultAgentMemoryRootFile       = "ROOT.md"
	capabilityAllowMessageSend       = "allow_message_send"
	managedAgentFilesystemKindDisk   = "disk"
	managedAgentFilesystemKindMemory = "memory"
)

type managedAgentRuntimeConfig struct {
	Capabilities             map[string]any               `json:"capabilities,omitempty"`
	Tools                    []agent.ConfiguredTool       `json:"tools,omitempty"`
	Filesystem               managedAgentFilesystemConfig `json:"filesystem,omitempty"`
	Memory                   AgentMemorySpec              `json:"memory"`
	Mounts                   []managedAgentMount          `json:"mounts,omitempty"`
	Network                  managedAgentNetworkConfig    `json:"network,omitempty"`
	HTTP                     managedAgentHTTPConfig       `json:"http,omitempty"`
	OutputFileThresholdBytes int                          `json:"output_file_threshold_bytes,omitempty"`
}

type managedAgentFilesystemConfig struct {
	Kind string `json:"kind,omitempty"`
}

func (c managedAgentRuntimeConfig) memorySettings() AgentMemorySpec {
	return c.Memory
}

func (c managedAgentRuntimeConfig) filesystemSettings() managedAgentFilesystemConfig {
	return c.Filesystem.normalized()
}

func (c managedAgentRuntimeConfig) mountSettings() []managedAgentMount {
	return append([]managedAgentMount(nil), c.Mounts...)
}

func (c managedAgentRuntimeConfig) capabilities() map[string]any {
	if len(c.Capabilities) == 0 {
		return nil
	}
	capabilities := make(map[string]any, len(c.Capabilities))
	for key, value := range c.Capabilities {
		capabilities[key] = value
	}
	return capabilities
}

func (c managedAgentRuntimeConfig) toolSettings() []agent.ConfiguredTool {
	return append([]agent.ConfiguredTool(nil), c.Tools...)
}

func (c managedAgentRuntimeConfig) networkSettings() managedAgentNetworkConfig {
	return managedAgentNetworkConfig{
		ReachableHosts: append([]managedAgentHostMatcher(nil), c.Network.ReachableHosts...),
	}
}

func (c managedAgentRuntimeConfig) httpHeaderSettings() []managedAgentHTTPHeader {
	return append([]managedAgentHTTPHeader(nil), c.HTTP.Headers...)
}

func (c managedAgentRuntimeConfig) outputFileThresholdBytes() int {
	return c.OutputFileThresholdBytes
}

func loadManagedAgentRuntimeConfig(configJSON string) (managedAgentRuntimeConfig, error) {
	if strings.TrimSpace(configJSON) == "" {
		return managedAgentRuntimeConfig{}, nil
	}
	var config managedAgentRuntimeConfig
	if err := cpstore.DecodeEnvelopeInto(configJSON, &config); err != nil {
		return managedAgentRuntimeConfig{}, err
	}
	return config, nil
}

func loadAgentFilesystemSettings(configJSON string) (managedAgentFilesystemConfig, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return managedAgentFilesystemConfig{}, err
	}
	return config.filesystemSettings(), nil
}

func loadAgentMountSettings(configJSON string) ([]managedAgentMount, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return config.mountSettings(), nil
}

func loadAgentToolSettings(configJSON string) ([]agent.ConfiguredTool, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return config.toolSettings(), nil
}

func loadAgentNetworkSettings(configJSON string) (managedAgentNetworkConfig, error) {
	config, err := loadManagedAgentRuntimeConfig(configJSON)
	if err != nil {
		return managedAgentNetworkConfig{}, err
	}
	return config.networkSettings(), nil
}

func (c managedAgentFilesystemConfig) normalized() managedAgentFilesystemConfig {
	return managedAgentFilesystemConfig{Kind: c.kind()}
}

func (c managedAgentFilesystemConfig) kind() string {
	switch strings.TrimSpace(c.Kind) {
	case "", managedAgentFilesystemKindDisk:
		return managedAgentFilesystemKindDisk
	case managedAgentFilesystemKindMemory:
		return managedAgentFilesystemKindMemory
	default:
		return strings.TrimSpace(c.Kind)
	}
}
