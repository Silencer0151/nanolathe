package launcher

import (
	"encoding/json"
	"errors"
	"os"
)

// Channel is the stream of builds a launcher follows.
type Channel string

const (
	// Stable follows the published vX.Y.Z releases, which are code signed.
	Stable Channel = "stable"
	// Latest follows every push to main. Its builds are unsigned, so Smart
	// App Control can block them.
	Latest Channel = "latest"
)

// Settings are the launcher's own preferences, kept apart from the game's
// settings.json so neither program rewrites the other's file.
type Settings struct {
	Channel Channel `json:"channel"`
}

func (in Install) settingsFile() string { return in.Path("launcher.json") }

// LoadSettings reads launcher.json. A missing or unreadable file gives the
// defaults; ok reports whether the player has chosen anything yet.
func (in Install) LoadSettings() (s Settings, ok bool) {
	s = Settings{Channel: Stable}
	data, err := os.ReadFile(in.settingsFile())
	if err != nil {
		return s, false
	}
	var saved Settings
	if json.Unmarshal(data, &saved) != nil {
		return s, false
	}
	if saved.Channel == Stable || saved.Channel == Latest {
		s.Channel = saved.Channel
	}
	return s, true
}

// SaveSettings writes launcher.json.
func (in Install) SaveSettings(s Settings) error {
	if s.Channel != Stable && s.Channel != Latest {
		return errors.New("nanolathe: unknown launcher channel " + string(s.Channel))
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(in.settingsFile(), append(data, '\n'))
}
