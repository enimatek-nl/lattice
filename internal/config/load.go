// Package config: convenience loader used by the entrypoint and tests.
package config

// LoadForRun is a small helper used by cmd/lattice and tests: it loads a config
// file (may be empty) and applies the given overrides.
func LoadForRun(path string, serverName, clientAddr, federationAddr string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	if serverName != "" {
		cfg.ServerName = serverName
	}
	if clientAddr != "" {
		cfg.HTTP.ClientAddr = clientAddr
	}
	if federationAddr != "" {
		cfg.HTTP.FederationAddr = federationAddr
	}
	if err := cfg.applyDerivedDefaults(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
