package models

import (
	"fmt"
	"log"
	"strconv"
	"strings"
)

// Model represents a Gemini backend routing configuration.
// Mode corresponds to the Gemini upstream mode ID, Think specifies the default reasoning depth,
// and Extra contains index-based overrides for Gemini's sparse RPC request array.
type Model struct {
	Mode  int         `json:"mode"`
	Think int         `json:"think"`
	Desc  string      `json:"desc"`
	Extra map[int]any `json:"extra,omitempty"`
}

var MODELS = map[string]Model{
	"gemini-3.7-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "Latest all-around model (Gemini 3.7 Flash)",
	},
	"gemini-3.6-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "All-around model (Gemini 3.6 Flash)",
	},
	"gemini-3.5-flash": {
		Mode:  1,
		Think: 4,
		Desc:  "Alias for gemini-3.6-flash (backend upgraded)",
	},
	"gemini-3.5-flash-thinking": {
		Mode:  2,
		Think: 0,
		Desc:  "Deep thinking mode, longest output (~20k chars)",
	},
	"gemini-3.1-pro": {
		Mode:  3,
		Think: 4,
		Desc:  "Pro model (requires cookie for real routing)",
	},
	"gemini-3.1-pro-enhanced": {
		Mode:  3,
		Think: 4,
		Desc:  "Pro with enhanced output (experimental)",
		Extra: map[int]any{31: 2, 80: 3},
	},
	"gemini-auto": {
		Mode:  4,
		Think: 4,
		Desc:  "Auto model selection",
	},
	"gemini-3.5-flash-thinking-lite": {
		Mode:  5,
		Think: 0,
		Desc:  "Dynamic thinking with adaptive depth",
	},
	"gemini-flash-lite": {
		Mode:  6,
		Think: 4,
		Desc:  "Lightweight fast model",
	},
}

type Resolved struct {
	Name  string
	Mode  int
	Think int
	Extra map[int]any
}

const DefaultModelName = "gemini-3.6-flash"

// Resolve maps a requested model string to its corresponding backend configuration.
// It supports model name suffixes like "@think=N" (e.g. "gemini-3.6-flash@think=0")
// to dynamically override the model's default thinking depth without modifying the model mapping.
func Resolve(modelName, defaultName string) (Resolved, error) {
	if defaultName == "" {
		defaultName = DefaultModelName
	}

	var thinkOverride *int
	if idx := strings.LastIndex(modelName, "@think="); idx != -1 {
		thinkStr := modelName[idx+len("@think="):]
		modelName = modelName[:idx]
		val, err := strconv.Atoi(thinkStr)
		if err != nil {
			return Resolved{}, fmt.Errorf("Invalid think level: %s", thinkStr)
		}
		thinkOverride = &val
	}

	cfg, exists := MODELS[modelName]
	if !exists {
		log.Printf("Unknown model '%s', falling back to '%s'", modelName, defaultName)
		modelName = defaultName
		cfg = MODELS[defaultName]
	}

	thinkMode := cfg.Think
	if thinkOverride != nil {
		thinkMode = *thinkOverride
	}

	return Resolved{
		Name:  modelName,
		Mode:  cfg.Mode,
		Think: thinkMode,
		Extra: cfg.Extra,
	}, nil
}
