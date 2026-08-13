package access

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/zippo1908/agentcell/configs"
)

// Devbox images, as data.
//
// The create-a-project form used to ask for an image by typing a registry
// path. That is a question only the operator can answer and only they can
// answer correctly — one typo produces a Cell that never starts, and the
// error surfaces as ImagePullBackOff two layers away from where it was made.
type Devbox struct {
	Name        string `yaml:"name" json:"name"`
	DisplayName string `yaml:"display_name" json:"displayName"`
	Image       string `yaml:"image" json:"image"`
	Description string `yaml:"description" json:"description"`
	Size        string `yaml:"size" json:"size"`
}

type devboxFile struct {
	Version  int      `yaml:"version"`
	Devboxes []Devbox `yaml:"devboxes"`
}

// LoadDevboxes returns the built-in catalogue merged with overlays; a later
// overlay replaces an entry with the same name, so an operator can repoint
// "slim" at their own registry without forking the list.
func LoadDevboxes(overlays ...[]byte) ([]Devbox, error) {
	out := []Devbox{}
	for _, raw := range append([][]byte{configs.DevboxesYAML}, overlays...) {
		var f devboxFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("devboxes: %w", err)
		}
		for _, d := range f.Devboxes {
			if d.Name == "" || d.Image == "" {
				return nil, fmt.Errorf("devbox entry needs a name and an image")
			}
			replaced := false
			for i := range out {
				if out[i].Name == d.Name {
					out[i] = d
					replaced = true
					break
				}
			}
			if !replaced {
				out = append(out, d)
			}
		}
	}
	return out, nil
}
