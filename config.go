package etcdircd

// config defines the server configuration for IRC.
type Config struct {
	NetworkName string
	HostName    string
	Motd        string

	MaxChannels uint
	MaxBans     uint
	NickLen     uint
	TopicLen    uint

	PinnedUserModes string
}

// NewConfig creates a configuration populated with some defaults
func NewConfig() (cfg Config) {
	cfg.NetworkName = "friendNET"
	cfg.HostName = "hostname"
	return cfg
}
