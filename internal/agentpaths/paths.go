package agentpaths

import "fmt"

const AppDirName = "agent-deck"

func LegacyDir() (string, error)                         { return "", fmt.Errorf("not implemented") }
func ConfigDir() (string, error)                         { return "", fmt.Errorf("not implemented") }
func DataDir() (string, error)                           { return "", fmt.Errorf("not implemented") }
func CacheDir() (string, error)                          { return "", fmt.Errorf("not implemented") }
func EffectiveConfigPath(name string) (string, error)    { return "", fmt.Errorf("not implemented") }
func EffectiveDataDir(markers ...string) (string, error) { return "", fmt.Errorf("not implemented") }
