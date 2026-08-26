// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Control is the steering file, re-read between rounds so a human can watch
// the log and intervene without restarting the run.
type Control struct {
	// Stop ends the loop after the current round.
	Stop bool `yaml:"stop"`
	// Pin forces one old topic ("area/sub") into the named subject.
	Pin map[string]string `yaml:"pin"`
	// Merge folds subjects together; each list merges into its FIRST name.
	Merge [][]string `yaml:"merge"`
	// Split re-clusters the named subjects at a tighter threshold.
	Split []string `yaml:"split"`
	// Keep exempts subjects from the small-tail folding.
	Keep []string `yaml:"keep"`
}

// LoadControl reads the steering file; a missing file means no wishes, not
// an error — the file exists only while someone is steering.
func LoadControl(path string) (Control, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Control{}, nil
	}
	if err != nil {
		return Control{}, err
	}
	var c Control
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Control{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// KeepSet renders the keep list as a lookup.
func (c Control) KeepSet() map[string]bool {
	out := map[string]bool{}
	for _, k := range c.Keep {
		out[k] = true
	}
	return out
}
