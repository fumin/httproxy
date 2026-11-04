package config

type Config struct {
	Addr string
}

func Prod() (Config, error) {
	cfg := Config{
		Addr: ":28877",
	}
	return cfg, nil
}

func Dev() (Config, error) {
	cfg := Config{
		Addr: ":28877",
	}
	return cfg, nil
}
