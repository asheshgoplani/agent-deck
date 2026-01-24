package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// SavedLayoutNode represents a layout tree node (matches JS LayoutNode type)
type SavedLayoutNode struct {
	Type      string             `json:"type"`                // "pane" or "split"
	ID        string             `json:"id,omitempty"`        // Pane ID (for type="pane")
	Direction string             `json:"direction,omitempty"` // "horizontal" or "vertical" (for type="split")
	Ratio     float64            `json:"ratio,omitempty"`     // Split ratio 0.0-1.0 (for type="split")
	Children  []*SavedLayoutNode `json:"children,omitempty"`  // Two children (for type="split")
}

// SavedLayout represents a saved layout template
type SavedLayout struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Layout    *SavedLayoutNode `json:"layout"`
	Shortcut  string           `json:"shortcut,omitempty"`  // Optional keyboard shortcut (e.g., "cmd+shift+1")
	CreatedAt int64            `json:"createdAt"`           // Unix timestamp
	UpdatedAt int64            `json:"updatedAt,omitempty"` // Unix timestamp
}

// SavedLayoutsFile represents the layouts.json file structure
type SavedLayoutsFile struct {
	Layouts []SavedLayout `json:"layouts"`
}

// SavedLayoutsManager manages saved layout templates
type SavedLayoutsManager struct {
	layoutsPath string
}

// NewSavedLayoutsManager creates a new saved layouts manager
func NewSavedLayoutsManager() *SavedLayoutsManager {
	home, _ := os.UserHomeDir()
	return &SavedLayoutsManager{
		layoutsPath: filepath.Join(home, ".agent-deck", "desktop", "layouts.json"),
	}
}

// loadLayouts loads all saved layouts from disk
func (slm *SavedLayoutsManager) loadLayouts() (*SavedLayoutsFile, error) {
	data, err := os.ReadFile(slm.layoutsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &SavedLayoutsFile{Layouts: []SavedLayout{}}, nil
		}
		return nil, err
	}

	var file SavedLayoutsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return &SavedLayoutsFile{Layouts: []SavedLayout{}}, nil
	}

	return &file, nil
}

// saveLayouts saves all layouts to disk
func (slm *SavedLayoutsManager) saveLayouts(file *SavedLayoutsFile) error {
	// Ensure directory exists
	dir := filepath.Dir(slm.layoutsPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(slm.layoutsPath, data, 0600)
}

// GetSavedLayouts returns all saved layouts
func (slm *SavedLayoutsManager) GetSavedLayouts() ([]SavedLayout, error) {
	file, err := slm.loadLayouts()
	if err != nil {
		return nil, err
	}
	return file.Layouts, nil
}

// SaveLayout saves a new layout or updates an existing one
func (slm *SavedLayoutsManager) SaveLayout(layout SavedLayout) (*SavedLayout, error) {
	file, err := slm.loadLayouts()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()

	// Check if updating existing layout
	for i, existing := range file.Layouts {
		if existing.ID == layout.ID {
			// Update existing
			layout.CreatedAt = existing.CreatedAt
			layout.UpdatedAt = now
			file.Layouts[i] = layout
			if err := slm.saveLayouts(file); err != nil {
				return nil, err
			}
			return &layout, nil
		}
	}

	// Create new layout
	if layout.ID == "" {
		layout.ID = uuid.New().String()
	}
	layout.CreatedAt = now
	layout.UpdatedAt = now
	file.Layouts = append(file.Layouts, layout)

	if err := slm.saveLayouts(file); err != nil {
		return nil, err
	}

	return &layout, nil
}

// DeleteLayout removes a layout by ID
func (slm *SavedLayoutsManager) DeleteLayout(id string) error {
	file, err := slm.loadLayouts()
	if err != nil {
		return err
	}

	// Filter out the layout to delete
	newLayouts := make([]SavedLayout, 0, len(file.Layouts))
	for _, layout := range file.Layouts {
		if layout.ID != id {
			newLayouts = append(newLayouts, layout)
		}
	}

	file.Layouts = newLayouts
	return slm.saveLayouts(file)
}

// GetLayoutByID returns a single layout by ID
func (slm *SavedLayoutsManager) GetLayoutByID(id string) (*SavedLayout, error) {
	file, err := slm.loadLayouts()
	if err != nil {
		return nil, err
	}

	for _, layout := range file.Layouts {
		if layout.ID == id {
			return &layout, nil
		}
	}

	return nil, nil
}
