package transport

import (
	"fmt"

	"github.com/amohammedimran/shipit/internal/config"
)

// Open connects to a saved server. trust is asked the first time a machine is
// seen and may be nil when nobody can be asked.
func Open(s *config.Server, trust func(host, fingerprint string) bool) (Conn, error) {
	switch s.Type {
	case "local":
		return Local{}, nil
	case "ssh", "":
		kh, err := config.KnownHostsPath()
		if err != nil {
			return nil, err
		}
		return DialSSH(SSHOptions{
			Host: s.Host, Port: s.Port, User: s.User, KeyPath: s.Key,
			KnownHosts: kh, TrustNewHost: trust, Passphrase: AskPassphrase,
		})
	}
	return nil, fmt.Errorf("server %q has an unknown type %q", s.Name, s.Type)
}
