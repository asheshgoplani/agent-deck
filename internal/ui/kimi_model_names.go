package ui

var kimiModelNames = map[string]string{
	"kimi-code/k3.1-pro":                  "K3.1 Pro",
	"kimi-code/k3.1-pro-highspeed":        "K3.1 Pro Highspeed",
	"kimi-code/k3":                        "K3",
	"kimi-code/k3-256k":                   "K3 256K",
	"kimi-code/kimi-for-coding":           "K2.8",
	"kimi-code/kimi-for-coding-highspeed": "K2.7 Highspeed",
}

func modelDisplayName(tool, modelID string) string {
	if tool == "kimi" {
		if name := kimiModelNames[modelID]; name != "" {
			return name
		}
	}
	return modelID
}

func modelIDFromDisplay(tool, display string) string {
	if tool == "kimi" {
		for modelID, name := range kimiModelNames {
			if name == display {
				return modelID
			}
		}
	}
	return display
}
